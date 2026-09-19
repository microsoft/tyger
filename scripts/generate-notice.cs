#!/usr/bin/env -S dotnet --

// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

// Generates NOTICE.md from the production dependency graphs of the shipped Go commands and .NET services.
// Go legal files come from go-licenses, while NuGet notices come from ClearlyDefined. NOTICE.md also serves as a
// cache: hidden metadata identifies each dependency and source file, and fenced blocks preserve the legal text.
// Unchanged ecosystem/name/version entries are reused, --refresh recollects selected entries, and --check is
// does not do any network requests.

using System.Diagnostics;
using System.Net;
using System.Runtime.CompilerServices;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;
using System.Xml.Linq;

return await NoticeGenerator.RunAsync(args);

internal static class NoticeGenerator
{
    private const int SchemaVersion = 1;
    private const string GoEcosystem = "go";
    private const string NuGetEcosystem = "nuget";
    private const string GoLicensesVersion = "v1.6.0";
    private const string Usage = "Usage: scripts/generate-notice.cs [--check] [--refresh <ecosystem:name@version>|all]";

    private static readonly string[] s_licenseFileNames =
    [
        "LICENCE",
        "LICENSE",
        "LICENSES",
        "UNLICENSE",
    ];

    private static readonly string[] s_otherLegalFileNames =
    [
        "COPYING",
        "COPYRIGHT",
        "NOTICE",
        "PATENTS",
        "THIRD-PARTY-NOTICES",
        "THIRD_PARTY_NOTICES",
    ];

    private static readonly IReadOnlyDictionary<string, string> s_goComponents = new Dictionary<string, string>(StringComparer.Ordinal)
    {
        ["buffer-copier"] = "./cmd/buffer-copier",
        ["buffer-sidecar"] = "./cmd/buffer-sidecar",
        ["loader"] = "./cmd/loader",
        ["tyger"] = "./cmd/tyger",
        ["tyger-proxy"] = "./cmd/tyger-proxy",
        ["worker-waiter"] = "./cmd/worker-waiter",
    };

    private static readonly IReadOnlyDictionary<string, string> s_nuGetComponents = new Dictionary<string, string>(StringComparer.Ordinal)
    {
        ["control-plane"] = "server/ControlPlane/packages.lock.json",
        ["data-plane"] = "server/DataPlane/packages.lock.json",
    };

    private static readonly HttpClient s_httpClient = CreateHttpClient();

    public static async Task<int> RunAsync(string[] args)
    {
        try
        {
            Options options = ParseOptions(args);
            if (options.ShowHelp)
            {
                Console.WriteLine(Usage);
                return 0;
            }

            string repositoryRoot = GetRepositoryRoot();
            string noticePath = Path.Combine(repositoryRoot, "NOTICE.md");
            Dictionary<string, InventoryDependency> inventory = await BuildInventoryAsync(repositoryRoot);
            Dictionary<string, NoticeDependency> cachedDependencies = LoadCachedDependencies(noticePath);
            HashSet<string> refreshKeys = ResolveRefreshKeys(options.RefreshTargets, inventory);

            var generatedDependencies = new List<NoticeDependency>(inventory.Count);
            var unresolvedGoDependencies = new List<InventoryDependency>();
            var unresolvedNuGetDependencies = new List<InventoryDependency>();
            int reusedCount = 0;

            foreach (InventoryDependency dependency in inventory.Values)
            {
                string key = GetKey(dependency.Ecosystem, dependency.Name, dependency.Version);
                if (!refreshKeys.Contains(key) && cachedDependencies.TryGetValue(key, out NoticeDependency? cachedDependency))
                {
                    ValidateNotices(cachedDependency);
                    generatedDependencies.Add(CreateDependency(dependency, cachedDependency.Notices));
                    reusedCount++;
                    continue;
                }

                if (dependency.Ecosystem == GoEcosystem)
                {
                    unresolvedGoDependencies.Add(dependency);
                }
                else
                {
                    unresolvedNuGetDependencies.Add(dependency);
                }
            }

            Console.WriteLine($"Found {inventory.Count} production dependencies; reusing {reusedCount} cached entries.");

            if (options.Check && (unresolvedGoDependencies.Count > 0 || unresolvedNuGetDependencies.Count > 0))
            {
                foreach (InventoryDependency dependency in unresolvedGoDependencies.Concat(unresolvedNuGetDependencies))
                {
                    Console.Error.WriteLine($"NOTICE.md has no cached legal text for {FormatIdentity(dependency)}.");
                }

                Console.Error.WriteLine("Run scripts/generate-notice.cs to resolve new or changed dependencies.");
                return 1;
            }

            if (unresolvedGoDependencies.Count > 0)
            {
                Console.WriteLine($"Collecting legal text for {unresolvedGoDependencies.Count} Go module(s).");
                Dictionary<string, List<LegalNotice>> goNotices = await CollectGoNoticesAsync(repositoryRoot, inventory.Values);
                foreach (InventoryDependency dependency in unresolvedGoDependencies)
                {
                    string key = GetKey(dependency.Ecosystem, dependency.Name, dependency.Version);
                    if (!goNotices.TryGetValue(key, out List<LegalNotice>? notices) || notices.Count == 0)
                    {
                        throw new InvalidOperationException(
                            $"No legal text was found for {FormatIdentity(dependency)}. Add a reviewed manual entry to NOTICE.md.");
                    }

                    generatedDependencies.Add(CreateDependency(dependency, notices));
                }
            }

            foreach (InventoryDependency dependency in unresolvedNuGetDependencies)
            {
                Console.WriteLine($"Fetching legal text for {FormatIdentity(dependency)}.");
                LegalNotice notice = await FetchClearlyDefinedNoticeAsync(dependency);
                generatedDependencies.Add(CreateDependency(dependency, [notice]));
            }

            generatedDependencies.Sort(CompareDependencies);
            var document = new NoticeDocument
            {
                SchemaVersion = SchemaVersion,
                Dependencies = generatedDependencies,
            };
            ValidateDocument(document, inventory);

            string expectedContents = Serialize(document);
            NoticeDocument roundTrippedDocument = DeserializeNotice(expectedContents);
            ValidateDocument(roundTrippedDocument, inventory);
            ValidateRoundTrip(document, roundTrippedDocument);
            string? currentContents = File.Exists(noticePath) ? await File.ReadAllTextAsync(noticePath) : null;
            if (string.Equals(currentContents, expectedContents, StringComparison.Ordinal))
            {
                Console.WriteLine("NOTICE.md is up to date.");
                return 0;
            }

            if (options.Check)
            {
                Console.Error.WriteLine("NOTICE.md is out of date. Run scripts/generate-notice.cs.");
                return 1;
            }

            await WriteAtomicallyAsync(noticePath, expectedContents);
            Console.WriteLine($"Wrote NOTICE.md with {generatedDependencies.Count} dependencies.");
            return 0;
        }
        catch (UsageException exception)
        {
            Console.Error.WriteLine(exception.Message);
            Console.Error.WriteLine(Usage);
            return 1;
        }
        catch (Exception exception)
        {
            Console.Error.WriteLine($"Failed to generate NOTICE.md: {exception.Message}");
            return 1;
        }
    }

    private static Options ParseOptions(string[] args)
    {
        bool check = false;
        bool showHelp = false;
        var refreshTargets = new HashSet<string>(StringComparer.Ordinal);

        for (int index = 0; index < args.Length; index++)
        {
            switch (args[index])
            {
                case "--check":
                    check = true;
                    break;
                case "--help" or "-h":
                    showHelp = true;
                    break;
                case "--refresh":
                    if (++index >= args.Length)
                    {
                        throw new UsageException("--refresh requires an ecosystem:name@version value or 'all'.");
                    }

                    refreshTargets.Add(args[index]);
                    break;
                default:
                    throw new UsageException($"Unknown argument '{args[index]}'.");
            }
        }

        return new(check, showHelp, refreshTargets);
    }

    private static async Task<Dictionary<string, InventoryDependency>> BuildInventoryAsync(string repositoryRoot)
    {
        var inventory = new Dictionary<string, InventoryDependency>(StringComparer.Ordinal);
        string cliRoot = Path.Combine(repositoryRoot, "cli");

        foreach ((string component, string packagePath) in s_goComponents)
        {
            ProcessResult result = await RunProcessAsync(
                "go",
                ["list", "-deps", "-json", packagePath],
                cliRoot);

            foreach ((string name, string version) in ParseGoModules(result.StandardOutput))
            {
                AddInventoryDependency(inventory, GoEcosystem, name, version, component);
            }
        }

        foreach ((string component, string relativeLockPath) in s_nuGetComponents)
        {
            string lockPath = Path.Combine(repositoryRoot, relativeLockPath);
            foreach ((string name, string version) in ParseNuGetLockFile(lockPath))
            {
                AddInventoryDependency(inventory, NuGetEcosystem, name, version, component);
            }
        }

        return inventory;
    }

    private static IReadOnlyList<(string Name, string Version)> ParseGoModules(string output)
    {
        var result = new List<(string Name, string Version)>();
        var seen = new HashSet<string>(StringComparer.Ordinal);
        var reader = new Utf8JsonReader(
            Encoding.UTF8.GetBytes(output),
            new JsonReaderOptions { AllowMultipleValues = true });
        while (reader.Read())
        {
            if (reader.TokenType != JsonTokenType.StartObject)
            {
                continue;
            }

            using JsonDocument packageDocument = JsonDocument.ParseValue(ref reader);
            if (!packageDocument.RootElement.TryGetProperty("Module", out JsonElement module) ||
                (module.TryGetProperty("Main", out JsonElement main) && main.GetBoolean()))
            {
                continue;
            }

            if (module.TryGetProperty("Replace", out JsonElement replacement))
            {
                module = replacement;
            }

            string name = GetRequiredJsonString(module, "Path", "Go module");
            string version = GetRequiredJsonString(module, "Version", $"Go module '{name}'");
            string identity = GetKey(GoEcosystem, name, version);
            if (seen.Add(identity))
            {
                result.Add((name, version));
            }
        }

        return result;
    }

    private static IEnumerable<(string Name, string Version)> ParseNuGetLockFile(string lockPath)
    {
        using JsonDocument lockDocument = JsonDocument.Parse(File.ReadAllText(lockPath));
        JsonElement dependencies = lockDocument.RootElement.GetProperty("dependencies");
        var seen = new HashSet<string>(StringComparer.Ordinal);

        foreach (JsonProperty targetFramework in dependencies.EnumerateObject())
        {
            foreach (JsonProperty package in targetFramework.Value.EnumerateObject())
            {
                JsonElement details = package.Value;
                string type = GetRequiredJsonString(details, "type", $"NuGet dependency '{package.Name}'");
                if (type == "Project")
                {
                    continue;
                }

                if (type is not ("Direct" or "Transitive"))
                {
                    throw new InvalidOperationException($"Unknown dependency type '{type}' for '{package.Name}' in {lockPath}.");
                }

                string version = GetRequiredJsonString(details, "resolved", $"NuGet dependency '{package.Name}'");
                string identity = GetKey(NuGetEcosystem, package.Name, version);
                if (seen.Add(identity))
                {
                    yield return (package.Name, version);
                }
            }
        }
    }

    private static string GetRequiredJsonString(JsonElement element, string propertyName, string description)
    {
        if (!element.TryGetProperty(propertyName, out JsonElement property) || property.GetString() is not { Length: > 0 } value)
        {
            throw new InvalidOperationException($"{description} has no {propertyName} value.");
        }

        return value;
    }

    private static void AddInventoryDependency(
        Dictionary<string, InventoryDependency> inventory,
        string ecosystem,
        string name,
        string version,
        string component)
    {
        string key = GetKey(ecosystem, name, version);
        if (!inventory.TryGetValue(key, out InventoryDependency? dependency))
        {
            dependency = new(ecosystem, name, version);
            inventory.Add(key, dependency);
        }

        dependency.UsedBy.Add(component);
    }

    private static Dictionary<string, NoticeDependency> LoadCachedDependencies(string noticePath)
    {
        if (!File.Exists(noticePath))
        {
            return new(StringComparer.Ordinal);
        }

        NoticeDocument document = DeserializeNotice(File.ReadAllText(noticePath));
        if (document.SchemaVersion != SchemaVersion)
        {
            throw new InvalidOperationException(
            $"NOTICE.md uses schema version {document.SchemaVersion}; expected {SchemaVersion}.");
        }

        var result = new Dictionary<string, NoticeDependency>(StringComparer.Ordinal);
        foreach (NoticeDependency dependency in document.Dependencies)
        {
            ValidateNotices(dependency);
            string key = GetKey(dependency.Ecosystem, dependency.Name, dependency.Version);
            if (!result.TryAdd(key, dependency))
            {
                throw new InvalidOperationException($"NOTICE.md contains duplicate dependency {FormatIdentity(dependency)}.");
            }
        }

        return result;
    }

    private static NoticeDocument DeserializeNotice(string contents)
    {
        string normalizedContents = NormalizeLineEndings(contents);
        Match schemaMatch = Regex.Match(
            normalizedContents,
            "(?m)^<!-- tyger-notice-schema: (?<version>[0-9]+) -->$");
        if (!schemaMatch.Success)
        {
            throw new InvalidOperationException("NOTICE.md has no schema version marker.");
        }

        var document = new NoticeDocument
        {
            SchemaVersion = int.Parse(schemaMatch.Groups["version"].Value, System.Globalization.CultureInfo.InvariantCulture),
        };
        string[] lines = normalizedContents.Split('\n');
        NoticeDependency? currentDependency = null;
        int currentNoticeIndex = 0;

        for (int lineIndex = 0; lineIndex < lines.Length; lineIndex++)
        {
            string line = lines[lineIndex];
            if (line.StartsWith("<div ", StringComparison.Ordinal) &&
                line.Contains("data-tyger-dependency=\"\"", StringComparison.Ordinal))
            {
                EnsureAllNoticeTextsFound(currentDependency, currentNoticeIndex);

                int metadataEnd = lineIndex;
                while (metadataEnd < lines.Length && lines[metadataEnd] != "</div>")
                {
                    metadataEnd++;
                }

                if (metadataEnd == lines.Length)
                {
                    throw new InvalidOperationException("NOTICE.md contains an unterminated dependency metadata block.");
                }

                currentDependency = ParseDependencyMetadata(string.Join('\n', lines[lineIndex..(metadataEnd + 1)]));
                document.Dependencies.Add(currentDependency);
                currentNoticeIndex = 0;
                lineIndex = metadataEnd;
                continue;
            }

            Match fenceMatch = Regex.Match(line, "^(?<fence>`{3,})text$");
            if (!fenceMatch.Success || currentDependency is null)
            {
                continue;
            }

            if (currentNoticeIndex >= currentDependency.Notices.Count)
            {
                throw new InvalidOperationException(
                    $"NOTICE.md contains too many legal-text blocks for {FormatIdentity(currentDependency)}.");
            }

            string fence = fenceMatch.Groups["fence"].Value;
            int legalTextEnd = lineIndex + 1;
            while (legalTextEnd < lines.Length && lines[legalTextEnd] != fence)
            {
                legalTextEnd++;
            }

            if (legalTextEnd == lines.Length)
            {
                throw new InvalidOperationException(
                    $"NOTICE.md contains an unterminated legal-text block for {FormatIdentity(currentDependency)}.");
            }

            currentDependency.Notices[currentNoticeIndex].Text = string.Join('\n', lines[(lineIndex + 1)..legalTextEnd]);
            currentNoticeIndex++;
            lineIndex = legalTextEnd;
        }

        EnsureAllNoticeTextsFound(currentDependency, currentNoticeIndex);
        return document;
    }

    private static NoticeDependency ParseDependencyMetadata(string contents)
    {
        XElement metadata = XElement.Parse(contents, LoadOptions.PreserveWhitespace);
        var dependency = new NoticeDependency
        {
            Ecosystem = GetRequiredAttribute(metadata, "data-ecosystem"),
            Name = GetRequiredAttribute(metadata, "data-name"),
            Version = GetRequiredAttribute(metadata, "data-version"),
            UsedBy = [.. metadata.Elements("input")
                .Where(element => element.Attribute("data-tyger-used-by") is not null)
                .Select(element => GetRequiredAttribute(element, "value"))],
        };

        foreach (XElement noticeElement in metadata.Elements("input")
            .Where(element => element.Attribute("data-tyger-notice") is not null))
        {
            int noticeIndex = int.Parse(
                GetRequiredAttribute(noticeElement, "data-index"),
                System.Globalization.CultureInfo.InvariantCulture);
            if (noticeIndex != dependency.Notices.Count)
            {
                throw new InvalidOperationException(
                    $"NOTICE.md has non-sequential notice metadata for {FormatIdentity(dependency)}.");
            }

            dependency.Notices.Add(new()
            {
                Source = GetRequiredAttribute(noticeElement, "data-source"),
                Url = (string?)noticeElement.Attribute("data-url"),
                Review = (string?)noticeElement.Attribute("data-review"),
                SourcePaths = [.. metadata.Elements("input")
                    .Where(element => element.Attribute("data-tyger-source-path") is not null &&
                        (string?)element.Attribute("data-notice-index") == noticeIndex.ToString(System.Globalization.CultureInfo.InvariantCulture))
                    .Select(element => GetRequiredAttribute(element, "value"))],
            });
        }

        return dependency;
    }

    private static void EnsureAllNoticeTextsFound(NoticeDependency? dependency, int noticeCount)
    {
        if (dependency is not null && noticeCount != dependency.Notices.Count)
        {
            throw new InvalidOperationException(
                $"NOTICE.md contains {noticeCount} legal-text blocks for {FormatIdentity(dependency)}; expected {dependency.Notices.Count}.");
        }
    }

    private static string GetRequiredAttribute(XElement element, string name)
    {
        string? value = (string?)element.Attribute(name);
        if (string.IsNullOrEmpty(value))
        {
            throw new InvalidOperationException($"NOTICE.md metadata element '{element.Name.LocalName}' is missing attribute '{name}'.");
        }

        return value;
    }

    private static async Task<Dictionary<string, List<LegalNotice>>> CollectGoNoticesAsync(
        string repositoryRoot,
        IEnumerable<InventoryDependency> inventory)
    {
        string outputPath = Path.Combine(Path.GetTempPath(), $"tyger-go-licenses-{Guid.NewGuid():N}");
        string cliRoot = Path.Combine(repositoryRoot, "cli");
        var arguments = new List<string>
        {
            "run",
            $"github.com/google/go-licenses@{GoLicensesVersion}",
            "save",
        };
        arguments.AddRange(s_goComponents.Values);
        arguments.Add("--ignore");
        arguments.Add("github.com/microsoft/tyger/cli");
        arguments.Add($"--save_path={outputPath}");

        try
        {
            ProcessResult result = await RunProcessAsync("go", arguments, cliRoot);
            if (!string.IsNullOrWhiteSpace(result.StandardError))
            {
                Console.Error.Write(result.StandardError);
            }

            List<InventoryDependency> goDependencies = [.. inventory
                .Where(dependency => dependency.Ecosystem == GoEcosystem)
                .OrderByDescending(dependency => dependency.Name.Length)];
            var notices = new Dictionary<string, List<LegalNotice>>(StringComparer.Ordinal);

            foreach (string filePath in Directory.EnumerateFiles(outputPath, "*", SearchOption.AllDirectories).Order(StringComparer.Ordinal))
            {
                string relativePath = Path.GetRelativePath(outputPath, filePath).Replace(Path.DirectorySeparatorChar, '/');
                string sourceDirectory = Path.GetDirectoryName(relativePath)?.Replace(Path.DirectorySeparatorChar, '/') ?? string.Empty;
                InventoryDependency? dependency = goDependencies.FirstOrDefault(
                    candidate => sourceDirectory.Equals(candidate.Name, StringComparison.Ordinal) ||
                        sourceDirectory.StartsWith(candidate.Name + "/", StringComparison.Ordinal));
                if (dependency is null)
                {
                    continue;
                }

                string legalText = NormalizeLegalText(await File.ReadAllTextAsync(filePath));
                if (legalText.Length == 0)
                {
                    continue;
                }

                string key = GetKey(dependency.Ecosystem, dependency.Name, dependency.Version);
                if (!notices.TryGetValue(key, out List<LegalNotice>? dependencyNotices))
                {
                    dependencyNotices = [];
                    notices.Add(key, dependencyNotices);
                }

                AddLegalNotice(dependencyNotices, "go-licenses@" + GoLicensesVersion, relativePath, legalText);
            }

            return notices;
        }
        finally
        {
            if (Directory.Exists(outputPath))
            {
                Directory.Delete(outputPath, recursive: true);
            }
        }
    }

    private static void AddLegalNotice(List<LegalNotice> notices, string source, string sourcePath, string legalText)
    {
        LegalNotice? existingNotice = notices.FirstOrDefault(notice => notice.Text.Equals(legalText, StringComparison.Ordinal));
        if (existingNotice is null)
        {
            notices.Add(new()
            {
                Source = source,
                SourcePaths = [sourcePath],
                Text = legalText,
            });
            return;
        }

        if (!existingNotice.SourcePaths.Contains(sourcePath, StringComparer.Ordinal))
        {
            existingNotice.SourcePaths.Add(sourcePath);
        }
    }

    private static async Task<LegalNotice> FetchClearlyDefinedNoticeAsync(InventoryDependency dependency)
    {
        string coordinate = $"nuget/nuget/-/{dependency.Name}/{dependency.Version}";
        string requestBody = CreateClearlyDefinedRequestBody(coordinate);

        const int MaxAttempts = 6;
        for (int attempt = 1; attempt <= MaxAttempts; attempt++)
        {
            using var request = new HttpRequestMessage(HttpMethod.Post, "https://api.clearlydefined.io/notices")
            {
                Content = new StringContent(requestBody, Encoding.UTF8, "application/json"),
            };
            using HttpResponseMessage response = await s_httpClient.SendAsync(request);
            string responseBody = await response.Content.ReadAsStringAsync();

            if (response.IsSuccessStatusCode)
            {
                using JsonDocument responseDocument = JsonDocument.Parse(responseBody);
                string legalText = responseDocument.RootElement.TryGetProperty("content", out JsonElement content)
                    ? NormalizeClearlyDefinedContent(content.GetString() ?? string.Empty)
                    : string.Empty;
                if (legalText.Length == 0)
                {
                    throw new InvalidOperationException(
                        $"ClearlyDefined returned no legal text for {FormatIdentity(dependency)}. " +
                        "Add a reviewed manual entry to NOTICE.md.");
                }

                return new()
                {
                    Source = "clearlydefined",
                    Url = GetClearlyDefinedUrl(dependency.Name, dependency.Version),
                    Text = legalText,
                };
            }

            bool retryable = response.StatusCode == HttpStatusCode.RequestTimeout ||
                response.StatusCode == HttpStatusCode.TooManyRequests ||
                (int)response.StatusCode >= 500;
            if (!retryable || attempt == MaxAttempts)
            {
                throw new HttpRequestException(
                    $"ClearlyDefined returned {(int)response.StatusCode} ({response.ReasonPhrase}) for {coordinate}: {responseBody}");
            }

            TimeSpan delay = GetRetryDelay(response, attempt);
            Console.Error.WriteLine(
                $"ClearlyDefined returned {(int)response.StatusCode} for {coordinate}; retrying in {delay.TotalSeconds:0} seconds.");
            await Task.Delay(delay);
        }

        throw new UnreachableException();
    }

    private static string CreateClearlyDefinedRequestBody(string coordinate)
    {
        using var stream = new MemoryStream();
        using (var writer = new Utf8JsonWriter(stream))
        {
            writer.WriteStartObject();
            writer.WriteStartArray("coordinates");
            writer.WriteStringValue(coordinate);
            writer.WriteEndArray();
            writer.WriteStartObject("options");
            writer.WriteEndObject();
            writer.WriteEndObject();
        }

        return Encoding.UTF8.GetString(stream.ToArray());
    }

    private static TimeSpan GetRetryDelay(HttpResponseMessage response, int attempt)
    {
        TimeSpan? requestedDelay = response.Headers.RetryAfter?.Delta;
        if (requestedDelay is null && response.Headers.RetryAfter?.Date is DateTimeOffset retryAt)
        {
            requestedDelay = retryAt - DateTimeOffset.UtcNow;
        }

        TimeSpan delay = requestedDelay is { } value && value > TimeSpan.Zero
            ? value
            : TimeSpan.FromSeconds(Math.Pow(2, attempt));
        return delay > TimeSpan.FromSeconds(60) ? TimeSpan.FromSeconds(60) : delay;
    }

    private static string NormalizeClearlyDefinedContent(string content)
    {
        string normalized = NormalizeLineEndings(content);
        Match heading = Regex.Match(normalized, "(?m)^\\*\\* .+; version .+ --\\s*$");
        if (heading.Success)
        {
            normalized = normalized[(heading.Index + heading.Length)..];
        }

        return NormalizeLegalText(normalized);
    }

    private static HashSet<string> ResolveRefreshKeys(
        IReadOnlySet<string> refreshTargets,
        IReadOnlyDictionary<string, InventoryDependency> inventory)
    {
        if (refreshTargets.Contains("all"))
        {
            if (refreshTargets.Count != 1)
            {
                throw new UsageException("The 'all' refresh target cannot be combined with package targets.");
            }

            return inventory.Keys.ToHashSet(StringComparer.Ordinal);
        }

        var result = new HashSet<string>(StringComparer.Ordinal);
        foreach (string target in refreshTargets)
        {
            int colonIndex = target.IndexOf(':');
            int atIndex = target.LastIndexOf('@');
            if (colonIndex <= 0 || atIndex <= colonIndex + 1 || atIndex == target.Length - 1)
            {
                throw new UsageException($"Invalid refresh target '{target}'. Expected ecosystem:name@version.");
            }

            string ecosystem = target[..colonIndex];
            string name = target[(colonIndex + 1)..atIndex];
            string version = target[(atIndex + 1)..];
            string key = GetKey(ecosystem, name, version);
            if (!inventory.ContainsKey(key))
            {
                throw new UsageException($"Refresh target '{target}' is not a production dependency.");
            }

            result.Add(key);
        }

        return result;
    }

    private static void ValidateDocument(
        NoticeDocument document,
        IReadOnlyDictionary<string, InventoryDependency> inventory)
    {
        if (document.Dependencies.Count != inventory.Count)
        {
            throw new InvalidOperationException(
                $"Generated {document.Dependencies.Count} notice entries for {inventory.Count} dependencies.");
        }

        var keys = new HashSet<string>(StringComparer.Ordinal);
        foreach (NoticeDependency dependency in document.Dependencies)
        {
            ValidateNotices(dependency);
            string key = GetKey(dependency.Ecosystem, dependency.Name, dependency.Version);
            if (!inventory.ContainsKey(key))
            {
                throw new InvalidOperationException($"Generated notice contains out-of-scope dependency {FormatIdentity(dependency)}.");
            }

            if (!keys.Add(key))
            {
                throw new InvalidOperationException($"Generated notice contains duplicate dependency {FormatIdentity(dependency)}.");
            }
        }
    }

    private static void ValidateRoundTrip(NoticeDocument expected, NoticeDocument actual)
    {
        if (expected.SchemaVersion != actual.SchemaVersion || expected.Dependencies.Count != actual.Dependencies.Count)
        {
            throw new InvalidOperationException("NOTICE.md did not round-trip through its parser.");
        }

        for (int dependencyIndex = 0; dependencyIndex < expected.Dependencies.Count; dependencyIndex++)
        {
            NoticeDependency expectedDependency = expected.Dependencies[dependencyIndex];
            NoticeDependency actualDependency = actual.Dependencies[dependencyIndex];
            if (expectedDependency.Ecosystem != actualDependency.Ecosystem ||
                expectedDependency.Name != actualDependency.Name ||
                expectedDependency.Version != actualDependency.Version ||
                !expectedDependency.UsedBy.SequenceEqual(actualDependency.UsedBy, StringComparer.Ordinal) ||
                expectedDependency.Notices.Count != actualDependency.Notices.Count)
            {
                throw new InvalidOperationException(
                    $"NOTICE.md did not round-trip metadata for {FormatIdentity(expectedDependency)}.");
            }

            for (int noticeIndex = 0; noticeIndex < expectedDependency.Notices.Count; noticeIndex++)
            {
                LegalNotice expectedNotice = expectedDependency.Notices[noticeIndex];
                LegalNotice actualNotice = actualDependency.Notices[noticeIndex];
                if (expectedNotice.Source != actualNotice.Source ||
                    expectedNotice.Url != actualNotice.Url ||
                    expectedNotice.Review != actualNotice.Review ||
                    !expectedNotice.SourcePaths.SequenceEqual(actualNotice.SourcePaths, StringComparer.Ordinal) ||
                    expectedNotice.Text != actualNotice.Text)
                {
                    throw new InvalidOperationException(
                        $"NOTICE.md did not round-trip legal notice {noticeIndex + 1} for {FormatIdentity(expectedDependency)}.");
                }
            }
        }
    }

    private static void ValidateNotices(NoticeDependency dependency)
    {
        if (string.IsNullOrWhiteSpace(dependency.Ecosystem) ||
            string.IsNullOrWhiteSpace(dependency.Name) ||
            string.IsNullOrWhiteSpace(dependency.Version))
        {
            throw new InvalidOperationException("NOTICE.md contains a dependency with an incomplete identity.");
        }

        if (dependency.Notices.Count == 0)
        {
            throw new InvalidOperationException($"NOTICE.md contains no legal text for {FormatIdentity(dependency)}.");
        }

        foreach (LegalNotice notice in dependency.Notices)
        {
            if (string.IsNullOrWhiteSpace(notice.Source) || string.IsNullOrWhiteSpace(notice.Text))
            {
                throw new InvalidOperationException($"NOTICE.md contains incomplete legal text for {FormatIdentity(dependency)}.");
            }

            if (notice.Source == "manual" && string.IsNullOrWhiteSpace(notice.Review))
            {
                throw new InvalidOperationException(
                    $"Manual legal text for {FormatIdentity(dependency)} must include a review explanation.");
            }
        }
    }

    private static NoticeDependency CreateDependency(InventoryDependency dependency, IEnumerable<LegalNotice> notices)
    {
        List<LegalNotice> clonedNotices = [.. notices.Select(CloneNotice)];
        foreach (LegalNotice notice in clonedNotices)
        {
            notice.SourcePaths.Sort(StringComparer.Ordinal);
        }

        List<LegalNotice> sortedNotices = [.. clonedNotices
            .OrderBy(GetNoticePriority)
            .ThenBy(notice => GetNoticeTopLevelDirectory(dependency.Name, notice), StringComparer.OrdinalIgnoreCase)
            .ThenBy(GetNoticeSortPath, StringComparer.OrdinalIgnoreCase)
            .ThenBy(notice => notice.Source, StringComparer.Ordinal)
            .ThenBy(notice => notice.Text, StringComparer.Ordinal)];

        return new()
        {
            Ecosystem = dependency.Ecosystem,
            Name = dependency.Name,
            Version = dependency.Version,
            UsedBy = [.. dependency.UsedBy.Order(StringComparer.Ordinal)],
            Notices = sortedNotices,
        };
    }

    private static LegalNotice CloneNotice(LegalNotice notice) => new()
    {
        Source = notice.Source,
        Url = notice.Url,
        Review = notice.Review,
        SourcePaths = [.. notice.SourcePaths],
        Text = NormalizeLegalText(notice.Text),
    };

    private static int CompareDependencies(NoticeDependency left, NoticeDependency right)
    {
        int result = StringComparer.Ordinal.Compare(left.Ecosystem, right.Ecosystem);
        if (result == 0)
        {
            result = StringComparer.OrdinalIgnoreCase.Compare(left.Name, right.Name);
        }

        return result == 0 ? StringComparer.Ordinal.Compare(left.Version, right.Version) : result;
    }

    private static string Serialize(NoticeDocument document)
    {
        int goDependencyCount = document.Dependencies.Count(dependency => dependency.Ecosystem == GoEcosystem);
        int nuGetDependencyCount = document.Dependencies.Count(dependency => dependency.Ecosystem == NuGetEcosystem);

        var builder = new StringBuilder();
        builder.AppendLine("<!-- Copyright (c) Microsoft Corporation. Licensed under the MIT License. -->");
        builder.AppendLine($"<!-- tyger-notice-schema: {document.SchemaVersion} -->");
        builder.AppendLine();
        builder.AppendLine("# Third-party notices");
        builder.AppendLine();
        builder.AppendLine("This repository incorporates material from the third-party dependencies listed below.");
        builder.AppendLine();
        builder.AppendLine($"**{document.Dependencies.Count} dependencies:** {goDependencyCount} Go modules and {nuGetDependencyCount} NuGet packages.");

        foreach (IGrouping<string, NoticeDependency> group in document.Dependencies.GroupBy(dependency => dependency.Ecosystem))
        {
            string heading = group.Key == GoEcosystem ? "Go modules" : "NuGet packages";
            builder.AppendLine();
            builder.AppendLine($"## {heading}");
            builder.AppendLine();
            builder.AppendLine($"{group.Count()} dependencies");

            foreach (NoticeDependency dependency in group)
            {
                builder.AppendLine();
                builder.AppendLine(CreateDependencyMetadata(dependency).ToString());
                builder.AppendLine();
                builder.AppendLine("<details>");
                builder.AppendLine($"<summary>{CreateCodeElement(dependency.Name)} {CreateCodeElement(dependency.Version)} · Used by {dependency.UsedBy.Count}</summary>");
                builder.AppendLine();
                builder.Append("**Used by:** ");
                builder.AppendLine(string.Join(", ", dependency.UsedBy.Select(CreateCodeElement)));

                List<(string Source, string? Url)> sources = [.. dependency.Notices
                    .Select(notice => (notice.Source, notice.Url))
                    .Distinct()];
                bool showSourcePerNotice = sources.Count != 1;
                if (!showSourcePerNotice)
                {
                    AppendNoticeSource(builder, sources[0].Source, sources[0].Url);
                }

                List<LegalNotice> licenseNotices = [.. dependency.Notices.Where(IsLicenseNotice)];
                List<LegalNotice> otherLegalNotices = [.. dependency.Notices.Where(IsOtherLegalNotice)];
                List<LegalNotice> primaryNotices = licenseNotices.Count > 0 ? licenseNotices : otherLegalNotices;
                List<LegalNotice> collapsedLegalNotices = licenseNotices.Count > 0 ? otherLegalNotices : [];
                List<LegalNotice> additionalNotices = [.. dependency.Notices.Where(notice => GetNoticePriority(notice) == 2)];

                foreach (LegalNotice notice in primaryNotices)
                {
                    builder.AppendLine();
                    builder.AppendLine($"### {GetNoticeLabel(dependency, notice)}");
                    AppendNoticeContents(builder, notice, showSourcePerNotice);
                }

                if (collapsedLegalNotices.Count == 1)
                {
                    LegalNotice notice = collapsedLegalNotices[0];
                    AppendNoticeDisclosure(
                        builder,
                        $"Other legal file: {GetNoticeLabel(dependency, notice)}",
                        notice,
                        showSourcePerNotice);
                }
                else if (collapsedLegalNotices.Count > 1)
                {
                    builder.AppendLine();
                    builder.AppendLine("<details>");
                    builder.AppendLine($"<summary>Other legal files ({collapsedLegalNotices.Count})</summary>");

                    foreach (LegalNotice notice in collapsedLegalNotices)
                    {
                        AppendNoticeDisclosure(
                            builder,
                            GetNoticeLabel(dependency, notice),
                            notice,
                            showSourcePerNotice);
                    }

                    builder.AppendLine();
                    builder.AppendLine("</details>");
                }

                AppendAdditionalNotices(builder, dependency, additionalNotices, showSourcePerNotice);

                builder.AppendLine();
                builder.AppendLine("</details>");
            }
        }

        builder.AppendLine();
        builder.AppendLine("Generated from the production dependency graph by `scripts/generate-notice.cs`.");
        return builder.ToString();
    }

    private static int GetNoticePriority(LegalNotice notice) =>
        IsLicenseNotice(notice) ? 0 : IsOtherLegalNotice(notice) ? 1 : 2;

    private static bool IsLicenseNotice(LegalNotice notice) =>
        notice.SourcePaths.Count == 0 || notice.SourcePaths.Any(IsLicensePath);

    private static bool IsOtherLegalNotice(LegalNotice notice) =>
        !IsLicenseNotice(notice) && notice.SourcePaths.Any(sourcePath => HasNamedLegalFile(sourcePath, s_otherLegalFileNames));

    private static bool IsLicensePath(string sourcePath)
    {
        string normalizedPath = sourcePath.Replace('\\', '/');
        if (normalizedPath.StartsWith("LICENSES/", StringComparison.OrdinalIgnoreCase) ||
            normalizedPath.Contains("/LICENSES/", StringComparison.OrdinalIgnoreCase))
        {
            return true;
        }

        return HasNamedLegalFile(normalizedPath, s_licenseFileNames);
    }

    private static bool HasNamedLegalFile(string sourcePath, IEnumerable<string> legalFileNames)
    {
        string normalizedPath = sourcePath.Replace('\\', '/');
        string fileName = normalizedPath[(normalizedPath.LastIndexOf('/') + 1)..];
        return legalFileNames.Any(legalFileName =>
            fileName.Equals(legalFileName, StringComparison.OrdinalIgnoreCase) ||
            fileName.StartsWith(legalFileName + ".", StringComparison.OrdinalIgnoreCase) ||
            fileName.StartsWith(legalFileName + "-", StringComparison.OrdinalIgnoreCase) ||
            fileName.StartsWith(legalFileName + "_", StringComparison.OrdinalIgnoreCase));
    }

    private static string GetNoticeSortPath(LegalNotice notice) =>
        notice.SourcePaths.Count == 0 ? string.Empty : notice.SourcePaths[0];

    private static string GetNoticeTopLevelDirectory(string dependencyName, LegalNotice notice)
    {
        if (notice.SourcePaths.Count == 0)
        {
            return string.Empty;
        }

        string relativePath = GetRelativeSourcePath(dependencyName, notice.SourcePaths[0]);
        int separatorIndex = relativePath.IndexOf('/');
        return separatorIndex < 0 ? string.Empty : relativePath[..separatorIndex];
    }

    private static string GetNoticeLabel(NoticeDependency dependency, LegalNotice notice, string? directory = null)
    {
        if (notice.SourcePaths.Count == 0)
        {
            return "License and notices";
        }

        return string.Join(", ", notice.SourcePaths.Select(sourcePath =>
        {
            string displayPath = GetRelativeSourcePath(dependency.Name, sourcePath);
            if (!string.IsNullOrEmpty(directory) && displayPath.StartsWith(directory + "/", StringComparison.OrdinalIgnoreCase))
            {
                displayPath = displayPath[(directory.Length + 1)..];
            }

            return CreateCodeElement(displayPath);
        }));
    }

    private static string GetRelativeSourcePath(string dependencyName, string sourcePath)
    {
        string dependencyPrefix = dependencyName.TrimEnd('/') + "/";
        return sourcePath.StartsWith(dependencyPrefix, StringComparison.Ordinal)
            ? sourcePath[dependencyPrefix.Length..]
            : sourcePath;
    }

    private static void AppendAdditionalNotices(
        StringBuilder builder,
        NoticeDependency dependency,
        IReadOnlyCollection<LegalNotice> notices,
        bool showSourcePerNotice)
    {
        if (notices.Count == 0)
        {
            return;
        }

        builder.AppendLine();
        builder.AppendLine("<details>");
        builder.AppendLine($"<summary>Additional files ({notices.Count})</summary>");
        builder.AppendLine();
        builder.AppendLine("<blockquote>");

        foreach (IGrouping<string, LegalNotice> directoryGroup in notices.GroupBy(
            notice => GetNoticeTopLevelDirectory(dependency.Name, notice),
            StringComparer.OrdinalIgnoreCase))
        {
            List<LegalNotice> directoryNotices = [.. directoryGroup];
            if (directoryNotices.Count == 1)
            {
                LegalNotice notice = directoryNotices[0];
                AppendNoticeDisclosure(
                    builder,
                    GetNoticeLabel(dependency, notice),
                    notice,
                    showSourcePerNotice);
                continue;
            }

            string directoryLabel = directoryGroup.Key.Length == 0
                ? "Repository root"
                : CreateCodeElement(directoryGroup.Key + "/");
            builder.AppendLine();
            builder.AppendLine("<details>");
            builder.AppendLine($"<summary>{directoryLabel} ({directoryNotices.Count} files)</summary>");
            builder.AppendLine();
            builder.AppendLine("<blockquote>");

            foreach (LegalNotice notice in directoryNotices)
            {
                AppendNoticeDisclosure(
                    builder,
                    GetNoticeLabel(dependency, notice, directoryGroup.Key),
                    notice,
                    showSourcePerNotice);
            }

            builder.AppendLine();
            builder.AppendLine("</blockquote>");
            builder.AppendLine();
            builder.AppendLine("</details>");
        }

        builder.AppendLine();
        builder.AppendLine("</blockquote>");
        builder.AppendLine();
        builder.AppendLine("</details>");
    }

    private static void AppendNoticeDisclosure(
        StringBuilder builder,
        string label,
        LegalNotice notice,
        bool showSource)
    {
        builder.AppendLine();
        builder.AppendLine("<details>");
        builder.AppendLine($"<summary>{label}</summary>");
        AppendNoticeContents(builder, notice, showSource);
        builder.AppendLine();
        builder.AppendLine("</details>");
    }

    private static void AppendNoticeContents(StringBuilder builder, LegalNotice notice, bool showSource)
    {
        if (showSource)
        {
            AppendNoticeSource(builder, notice.Source, notice.Url);
        }

        if (notice.Review is not null)
        {
            builder.AppendLine();
            builder.AppendLine($"**Review:** {WebUtility.HtmlEncode(notice.Review)}");
        }

        string fence = GetMarkdownFence(notice.Text);
        builder.AppendLine();
        builder.AppendLine(fence + "text");
        builder.AppendLine(notice.Text);
        builder.AppendLine(fence);
    }

    private static void AppendNoticeSource(StringBuilder builder, string source, string? url)
    {
        builder.AppendLine();
        builder.Append("**Source:** ");
        builder.AppendLine(url is null ? CreateCodeElement(source) : CreateLinkElement(source, url));
    }

    private static XElement CreateDependencyMetadata(NoticeDependency dependency)
    {
        var metadata = new XElement("div",
            new XAttribute("data-tyger-dependency", string.Empty),
            new XAttribute("hidden", string.Empty),
            new XAttribute("data-ecosystem", dependency.Ecosystem),
            new XAttribute("data-name", dependency.Name),
            new XAttribute("data-version", dependency.Version));

        foreach (string component in dependency.UsedBy)
        {
            metadata.Add(new XElement("input",
                new XAttribute("type", "hidden"),
                new XAttribute("data-tyger-used-by", string.Empty),
                new XAttribute("value", component)));
        }

        for (int index = 0; index < dependency.Notices.Count; index++)
        {
            LegalNotice notice = dependency.Notices[index];
            var noticeMetadata = new XElement("input",
                new XAttribute("type", "hidden"),
                new XAttribute("data-tyger-notice", string.Empty),
                new XAttribute("data-index", index),
                new XAttribute("data-source", notice.Source));
            if (notice.Url is not null)
            {
                noticeMetadata.Add(new XAttribute("data-url", notice.Url));
            }

            if (notice.Review is not null)
            {
                noticeMetadata.Add(new XAttribute("data-review", notice.Review));
            }

            metadata.Add(noticeMetadata);
            foreach (string sourcePath in notice.SourcePaths)
            {
                metadata.Add(new XElement("input",
                    new XAttribute("type", "hidden"),
                    new XAttribute("data-tyger-source-path", string.Empty),
                    new XAttribute("data-notice-index", index),
                    new XAttribute("value", sourcePath)));
            }
        }

        return metadata;
    }

    private static string CreateCodeElement(string value) =>
        new XElement("code", value).ToString(SaveOptions.DisableFormatting);

    private static string CreateLinkElement(string text, string url) =>
        new XElement("a", new XAttribute("href", url), text).ToString(SaveOptions.DisableFormatting);

    private static string GetMarkdownFence(string legalText)
    {
        int longestRun = Regex.Matches(legalText, "`+").Select(match => match.Length).DefaultIfEmpty().Max();
        return new string('`', Math.Max(3, longestRun + 1));
    }

    private static async Task WriteAtomicallyAsync(string path, string contents)
    {
        string temporaryPath = Path.Combine(Path.GetDirectoryName(path)!, $".{Path.GetFileName(path)}.{Guid.NewGuid():N}.tmp");
        try
        {
            await File.WriteAllTextAsync(temporaryPath, contents, new UTF8Encoding(encoderShouldEmitUTF8Identifier: false));
            File.Move(temporaryPath, path, overwrite: true);
        }
        finally
        {
            if (File.Exists(temporaryPath))
            {
                File.Delete(temporaryPath);
            }
        }
    }

    private static async Task<ProcessResult> RunProcessAsync(
        string fileName,
        IEnumerable<string> arguments,
        string workingDirectory)
    {
        var startInfo = new ProcessStartInfo(fileName)
        {
            WorkingDirectory = workingDirectory,
            RedirectStandardOutput = true,
            RedirectStandardError = true,
            UseShellExecute = false,
        };
        foreach (string argument in arguments)
        {
            startInfo.ArgumentList.Add(argument);
        }

        using Process process = Process.Start(startInfo)
            ?? throw new InvalidOperationException($"Failed to start '{fileName}'.");
        Task<string> standardOutput = process.StandardOutput.ReadToEndAsync();
        Task<string> standardError = process.StandardError.ReadToEndAsync();
        await process.WaitForExitAsync();
        string output = await standardOutput;
        string error = await standardError;
        if (process.ExitCode != 0)
        {
            throw new InvalidOperationException(
                $"'{fileName}' exited with code {process.ExitCode}.{Environment.NewLine}{error}{output}");
        }

        return new(output, error);
    }

    private static string GetRepositoryRoot()
    {
        string scriptPath = GetSourceFilePath();
        string? scriptsDirectory = Path.GetDirectoryName(scriptPath);
        string? repositoryRoot = scriptsDirectory is null ? null : Directory.GetParent(scriptsDirectory)?.FullName;
        if (repositoryRoot is null || !File.Exists(Path.Combine(repositoryRoot, "cli", "go.mod")))
        {
            throw new InvalidOperationException("Could not locate the repository root from the generator source path.");
        }

        return repositoryRoot;
    }

    private static string GetSourceFilePath([CallerFilePath] string sourceFilePath = "") => sourceFilePath;

    private static string GetKey(string ecosystem, string name, string version)
    {
        string normalizedEcosystem = ecosystem.ToLowerInvariant();
        string normalizedName = normalizedEcosystem == NuGetEcosystem ? name.ToLowerInvariant() : name;
        return $"{normalizedEcosystem}\0{normalizedName}\0{version}";
    }

    private static string FormatIdentity(InventoryDependency dependency) =>
        $"{dependency.Ecosystem}:{dependency.Name}@{dependency.Version}";

    private static string FormatIdentity(NoticeDependency dependency) =>
        $"{dependency.Ecosystem}:{dependency.Name}@{dependency.Version}";

    private static string GetClearlyDefinedUrl(string name, string version) =>
        $"https://clearlydefined.io/definitions/nuget/nuget/-/{Uri.EscapeDataString(name)}/{Uri.EscapeDataString(version)}";

    private static string NormalizeLegalText(string value) =>
        NormalizeLineEndings(value).Trim('\n', '\r', '\uFEFF');

    private static string NormalizeLineEndings(string value) => value.Replace("\r\n", "\n", StringComparison.Ordinal).Replace('\r', '\n');

    private static HttpClient CreateHttpClient()
    {
        var client = new HttpClient
        {
            Timeout = TimeSpan.FromMinutes(2),
        };
        client.DefaultRequestHeaders.UserAgent.ParseAdd("tyger-notice-generator/1.0");
        return client;
    }

    private sealed record Options(bool Check, bool ShowHelp, IReadOnlySet<string> RefreshTargets);

    private sealed record ProcessResult(string StandardOutput, string StandardError);

    private sealed class InventoryDependency(string ecosystem, string name, string version)
    {
        public string Ecosystem { get; } = ecosystem;

        public string Name { get; } = name;

        public string Version { get; } = version;

        public HashSet<string> UsedBy { get; } = new(StringComparer.Ordinal);
    }

    private sealed class NoticeDocument
    {
        public int SchemaVersion { get; set; }

        public List<NoticeDependency> Dependencies { get; set; } = [];
    }

    private sealed class NoticeDependency
    {
        public string Ecosystem { get; set; } = string.Empty;

        public string Name { get; set; } = string.Empty;

        public string Version { get; set; } = string.Empty;

        public List<string> UsedBy { get; set; } = [];

        public List<LegalNotice> Notices { get; set; } = [];
    }

    private sealed class LegalNotice
    {
        public string Source { get; set; } = string.Empty;

        public string? Url { get; set; }

        public string? Review { get; set; }

        public List<string> SourcePaths { get; set; } = [];

        public string Text { get; set; } = string.Empty;
    }

    private sealed class UsageException(string message) : Exception(message);
}
