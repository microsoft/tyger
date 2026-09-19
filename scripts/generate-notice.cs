#!/usr/bin/env -S dotnet --

// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Diagnostics;
using System.Net;
using System.Runtime.CompilerServices;
using System.Security.Cryptography;
using System.Text;
using System.Text.Json;
using System.Text.RegularExpressions;
using System.Xml;
using System.Xml.Linq;

return await NoticeGenerator.RunAsync(args);

internal static class NoticeGenerator
{
    private const int SchemaVersion = 1;
    private const string GoEcosystem = "go";
    private const string NuGetEcosystem = "nuget";
    private const string GoLicensesVersion = "v1.6.0";
    private const string Usage = "Usage: scripts/generate-notice.cs [--check] [--refresh <ecosystem:name@version>|all]";
    private static readonly XNamespace s_xhtml = "http://www.w3.org/1999/xhtml";

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
            string noticePath = Path.Combine(repositoryRoot, "NOTICE.xhtml");
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
                    Console.Error.WriteLine($"NOTICE.xhtml has no cached legal text for {FormatIdentity(dependency)}.");
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
                            $"No legal text was found for {FormatIdentity(dependency)}. Add a reviewed manual entry to NOTICE.xhtml.");
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
            _ = XDocument.Parse(expectedContents, LoadOptions.PreserveWhitespace);
            string? currentContents = File.Exists(noticePath) ? await File.ReadAllTextAsync(noticePath) : null;
            if (string.Equals(currentContents, expectedContents, StringComparison.Ordinal))
            {
                Console.WriteLine("NOTICE.xhtml is up to date.");
                return 0;
            }

            if (options.Check)
            {
                Console.Error.WriteLine("NOTICE.xhtml is out of date. Run scripts/generate-notice.cs.");
                return 1;
            }

            await WriteAtomicallyAsync(noticePath, expectedContents);
            Console.WriteLine($"Wrote NOTICE.xhtml with {generatedDependencies.Count} dependencies.");
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
            Console.Error.WriteLine($"Failed to generate NOTICE.xhtml: {exception.Message}");
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
            $"NOTICE.xhtml uses schema version {document.SchemaVersion}; expected {SchemaVersion}.");
        }

        var result = new Dictionary<string, NoticeDependency>(StringComparer.Ordinal);
        foreach (NoticeDependency dependency in document.Dependencies)
        {
            ValidateNotices(dependency);
            string key = GetKey(dependency.Ecosystem, dependency.Name, dependency.Version);
            if (!result.TryAdd(key, dependency))
            {
                throw new InvalidOperationException($"NOTICE.xhtml contains duplicate dependency {FormatIdentity(dependency)}.");
            }
        }

        return result;
    }

    private static NoticeDocument DeserializeNotice(string contents)
    {
        using var stringReader = new StringReader(contents);
        using XmlReader xmlReader = XmlReader.Create(stringReader, new XmlReaderSettings
        {
            DtdProcessing = DtdProcessing.Ignore,
            XmlResolver = null,
        });
        XDocument xhtmlDocument = XDocument.Load(xmlReader, LoadOptions.PreserveWhitespace | LoadOptions.SetLineInfo);
        XElement root = xhtmlDocument.Root
            ?? throw new InvalidOperationException("NOTICE.xhtml has no document element.");
        if (root.Name != s_xhtml + "html")
        {
            throw new InvalidOperationException("NOTICE.xhtml must use the XHTML namespace.");
        }

        var document = new NoticeDocument
        {
            SchemaVersion = int.Parse(GetRequiredAttribute(root, "data-schema-version"), System.Globalization.CultureInfo.InvariantCulture),
        };

        XElement body = GetRequiredElement(root, "body");
        XElement main = GetRequiredElement(body, "main");
        foreach (XElement dependencyElement in main.Elements(s_xhtml + "section")
            .Where(element => HasClass(element, "ecosystem"))
            .SelectMany(element => element.Elements(s_xhtml + "details").Where(child => HasClass(child, "dependency"))))
        {
            var dependency = new NoticeDependency
            {
                Ecosystem = GetRequiredAttribute(dependencyElement, "data-ecosystem"),
                Name = GetRequiredAttribute(dependencyElement, "data-name"),
                Version = GetRequiredAttribute(dependencyElement, "data-version"),
            };

            XElement dependencyContent = GetRequiredElement(dependencyElement, "div", "dependency-content");
            XElement usage = GetRequiredElement(dependencyContent, "section", "usage");
            XElement usedBy = GetRequiredElement(usage, "ul", "used-by");
            dependency.UsedBy.AddRange(usedBy.Elements(s_xhtml + "li").Select(element => element.Value));

            foreach (XElement noticeElement in dependencyContent.Elements(s_xhtml + "section")
                .Where(element => HasClass(element, "notice")))
            {
                XElement? provenance = GetOptionalElement(noticeElement, "p", "provenance");
                XElement? sourceLink = provenance is null ? null : GetRequiredElement(provenance, "a", "source-url");
                XElement? review = GetOptionalElement(noticeElement, "p", "review");
                XElement? sourcePathsBlock = GetOptionalElement(noticeElement, "div", "source-paths-block");
                List<string> sourcePaths = sourcePathsBlock is null
                    ? []
                    : GetRequiredElement(sourcePathsBlock, "ul", "source-paths")
                        .Elements(s_xhtml + "li")
                        .Select(element => element.Value)
                        .ToList();

                string? reviewText = null;
                if (review is not null)
                {
                    XElement reviewLabel = GetRequiredElement(review, "strong");
                    reviewText = string.Concat(reviewLabel.NodesAfterSelf().OfType<XText>().Select(text => text.Value)).Trim();
                }

                dependency.Notices.Add(new()
                {
                    Source = GetRequiredAttribute(noticeElement, "data-source"),
                    Url = sourceLink is null ? null : GetRequiredAttribute(sourceLink, "href"),
                    Review = reviewText,
                    SourcePaths = sourcePaths,
                    Text = GetRequiredElement(noticeElement, "pre", "legal-text").Value,
                });
            }

            document.Dependencies.Add(dependency);
        }

        return document;
    }

    private static string GetRequiredAttribute(XElement element, string name)
    {
        string? value = (string?)element.Attribute(name);
        if (string.IsNullOrEmpty(value))
        {
            throw new InvalidOperationException($"NOTICE.xhtml element '{element.Name.LocalName}' is missing attribute '{name}'.");
        }

        return value;
    }

    private static XElement GetRequiredElement(XElement parent, string name, string? className = null)
    {
        List<XElement> matches = parent.Elements(s_xhtml + name)
            .Where(element => className is null || HasClass(element, className))
            .ToList();
        if (matches.Count != 1)
        {
            string description = className is null ? name : $"{name}.{className}";
            throw new InvalidOperationException(
                $"NOTICE.xhtml element '{parent.Name.LocalName}' must contain exactly one '{description}' element.");
        }

        return matches[0];
    }

    private static XElement? GetOptionalElement(XElement parent, string name, string className)
    {
        List<XElement> matches = parent.Elements(s_xhtml + name)
            .Where(element => HasClass(element, className))
            .ToList();
        if (matches.Count > 1)
        {
            throw new InvalidOperationException(
                $"NOTICE.xhtml element '{parent.Name.LocalName}' must contain at most one '{name}.{className}' element.");
        }

        return matches.SingleOrDefault();
    }

    private static bool HasClass(XElement element, string className) =>
        ((string?)element.Attribute("class"))?.Split(' ', StringSplitOptions.RemoveEmptyEntries).Contains(className, StringComparer.Ordinal) == true;

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

            List<InventoryDependency> goDependencies = inventory
                .Where(dependency => dependency.Ecosystem == GoEcosystem)
                .OrderByDescending(dependency => dependency.Name.Length)
                .ToList();
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
                        "Add a reviewed manual entry to NOTICE.xhtml.");
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

    private static void ValidateNotices(NoticeDependency dependency)
    {
        if (string.IsNullOrWhiteSpace(dependency.Ecosystem) ||
            string.IsNullOrWhiteSpace(dependency.Name) ||
            string.IsNullOrWhiteSpace(dependency.Version))
        {
            throw new InvalidOperationException("NOTICE.xhtml contains a dependency with an incomplete identity.");
        }

        if (dependency.Notices.Count == 0)
        {
            throw new InvalidOperationException($"NOTICE.xhtml contains no legal text for {FormatIdentity(dependency)}.");
        }

        foreach (LegalNotice notice in dependency.Notices)
        {
            if (string.IsNullOrWhiteSpace(notice.Source) || string.IsNullOrWhiteSpace(notice.Text))
            {
                throw new InvalidOperationException($"NOTICE.xhtml contains incomplete legal text for {FormatIdentity(dependency)}.");
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
        List<LegalNotice> sortedNotices = notices
            .Select(CloneNotice)
            .OrderBy(notice => notice.Source, StringComparer.Ordinal)
            .ThenBy(notice => notice.Text, StringComparer.Ordinal)
            .ToList();
        foreach (LegalNotice notice in sortedNotices)
        {
            notice.SourcePaths.Sort(StringComparer.Ordinal);
        }

        return new()
        {
            Ecosystem = dependency.Ecosystem,
            Name = dependency.Name,
            Version = dependency.Version,
            UsedBy = dependency.UsedBy.Order(StringComparer.Ordinal).ToList(),
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

                var main = new XElement(s_xhtml + "main",
                        new XElement(s_xhtml + "p",
                                new XAttribute("class", "introduction"),
                                "This repository incorporates material from the third-party dependencies listed below."),
                        CreateStats(document.Dependencies.Count, goDependencyCount, nuGetDependencyCount));

                foreach (IGrouping<string, NoticeDependency> group in document.Dependencies.GroupBy(dependency => dependency.Ecosystem))
                {
                        string heading = group.Key == GoEcosystem ? "Go modules" : "NuGet packages";
                        var section = new XElement(s_xhtml + "section",
                                new XAttribute("class", "ecosystem"),
                                new XAttribute("data-ecosystem", group.Key),
                                new XElement(s_xhtml + "div",
                                        new XAttribute("class", "ecosystem-heading"),
                                        new XElement(s_xhtml + "h2", heading),
                                        new XElement(s_xhtml + "span", new XAttribute("class", "count"), $"{group.Count()} dependencies")));

                        foreach (NoticeDependency dependency in group)
                        {
                                section.Add(CreateDependencyElement(dependency));
                        }

                        main.Add(section);
                }

                var html = new XElement(s_xhtml + "html",
                        new XAttribute(XNamespace.Xml + "lang", "en"),
                        new XAttribute("lang", "en"),
                        new XAttribute("data-schema-version", document.SchemaVersion),
                        new XElement(s_xhtml + "head",
                                new XElement(s_xhtml + "meta", new XAttribute("charset", "utf-8")),
                                new XElement(s_xhtml + "meta",
                                        new XAttribute("name", "viewport"),
                                        new XAttribute("content", "width=device-width, initial-scale=1")),
                                new XElement(s_xhtml + "title", "Tyger third-party notices"),
                                new XElement(s_xhtml + "style", new XAttribute("type", "text/css"), Styles)),
                        new XElement(s_xhtml + "body",
                                new XElement(s_xhtml + "header",
                                        new XElement(s_xhtml + "div",
                                                new XAttribute("class", "header-content"),
                                                new XElement(s_xhtml + "p", new XAttribute("class", "eyebrow"), "TYGER"),
                                                new XElement(s_xhtml + "h1", "Third-party notices"),
                                                new XElement(s_xhtml + "p", new XAttribute("class", "subtitle"), "Licenses and attribution for production dependencies"))),
                                main,
                                new XElement(s_xhtml + "footer",
                                        "Generated from the production dependency graph by ",
                                        new XElement(s_xhtml + "code", "scripts/generate-notice.cs"),
                                        ".")));

                var xhtmlDocument = new XDocument(
                        new XDeclaration("1.0", "utf-8", null),
                        new XDocumentType("html", null, null, null),
                        new XComment(" Copyright (c) Microsoft Corporation. Licensed under the MIT License. "),
                        html);

                using var stream = new MemoryStream();
                using (XmlWriter writer = XmlWriter.Create(stream, new XmlWriterSettings
                {
                        Encoding = new UTF8Encoding(encoderShouldEmitUTF8Identifier: false),
                        Indent = true,
                        IndentChars = "  ",
                        NewLineChars = "\n",
                        NewLineHandling = NewLineHandling.None,
                }))
                {
                        xhtmlDocument.Save(writer);
                }

                string contents = Encoding.UTF8.GetString(stream.ToArray());
                return contents.EndsWith('\n') ? contents : contents + '\n';
    }

        private static XElement CreateStats(int dependencyCount, int goDependencyCount, int nuGetDependencyCount) =>
                new(s_xhtml + "dl",
                        new XAttribute("class", "stats"),
                        CreateStat(dependencyCount, "dependencies"),
                        CreateStat(goDependencyCount, "Go modules"),
                        CreateStat(nuGetDependencyCount, "NuGet packages"));

        private static XElement CreateStat(int count, string label) =>
                new(s_xhtml + "div",
                        new XElement(s_xhtml + "dt", count),
                        new XElement(s_xhtml + "dd", label));

        private static XElement CreateDependencyElement(NoticeDependency dependency)
        {
                var content = new XElement(s_xhtml + "div",
                        new XAttribute("class", "dependency-content"),
                        new XElement(s_xhtml + "section",
                                new XAttribute("class", "usage"),
                                new XElement(s_xhtml + "h3", "Used by"),
                                new XElement(s_xhtml + "ul",
                                        new XAttribute("class", "used-by"),
                                        dependency.UsedBy.Select(component => new XElement(s_xhtml + "li", component)))));

                for (int index = 0; index < dependency.Notices.Count; index++)
                {
                        content.Add(CreateNoticeElement(dependency.Notices[index], index + 1, dependency.Notices.Count));
                }

                return new XElement(s_xhtml + "details",
                        new XAttribute("class", "dependency"),
                        new XAttribute("id", GetDependencyId(dependency)),
                        new XAttribute("data-ecosystem", dependency.Ecosystem),
                        new XAttribute("data-name", dependency.Name),
                        new XAttribute("data-version", dependency.Version),
                        new XElement(s_xhtml + "summary",
                                new XElement(s_xhtml + "code", new XAttribute("class", "package-name"), dependency.Name),
                                new XElement(s_xhtml + "span", new XAttribute("class", "version"), dependency.Version),
                                new XElement(s_xhtml + "span", new XAttribute("class", "usage-count"), $"Used by {dependency.UsedBy.Count}")),
                        content);
        }

        private static XElement CreateNoticeElement(LegalNotice notice, int number, int total)
        {
                var section = new XElement(s_xhtml + "section",
                        new XAttribute("class", "notice"),
                        new XAttribute("data-source", notice.Source),
                        new XElement(s_xhtml + "div",
                                new XAttribute("class", "notice-heading"),
                                new XElement(s_xhtml + "h3", total == 1 ? "Legal notice" : $"Legal notice {number}"),
                                new XElement(s_xhtml + "span", new XAttribute("class", "source-kind"), notice.Source)));

                if (notice.Url is not null)
                {
                        section.Add(new XElement(s_xhtml + "p",
                                new XAttribute("class", "provenance"),
                                "Source: ",
                                new XElement(s_xhtml + "a",
                                        new XAttribute("class", "source-url"),
                                        new XAttribute("href", notice.Url),
                                        notice.Url)));
                }

                if (notice.Review is not null)
                {
                        section.Add(new XElement(s_xhtml + "p",
                                new XAttribute("class", "review"),
                                new XElement(s_xhtml + "strong", "Review: "),
                                notice.Review));
                }

                if (notice.SourcePaths.Count > 0)
                {
                        section.Add(new XElement(s_xhtml + "div",
                                new XAttribute("class", "source-paths-block"),
                                new XElement(s_xhtml + "h4", "Source paths"),
                                new XElement(s_xhtml + "ul",
                                        new XAttribute("class", "source-paths"),
                                        notice.SourcePaths.Select(sourcePath => new XElement(s_xhtml + "li", sourcePath)))));
                }

                section.Add(new XElement(s_xhtml + "pre",
                        new XAttribute("class", "legal-text"),
                        new XAttribute(XNamespace.Xml + "space", "preserve"),
                        notice.Text));
                return section;
        }

        private static string GetDependencyId(NoticeDependency dependency)
        {
                byte[] identity = Encoding.UTF8.GetBytes($"{dependency.Ecosystem}\0{dependency.Name}\0{dependency.Version}");
                return $"dependency-{dependency.Ecosystem}-{Convert.ToHexStringLower(SHA256.HashData(identity))[..12]}";
        }

        private const string Styles = """
                :root {
                    color-scheme: light;
                    font-family: Georgia, "Times New Roman", serif;
                    color: #172126;
                    background: #e9eff0;
                    letter-spacing: 0;
                }

                * { box-sizing: border-box; }

                body {
                    min-width: 320px;
                    margin: 0;
                    background: #e9eff0;
                }

                header {
                    color: #f8fbfa;
                    background: #16383c;
                    border-bottom: 5px solid #e76f51;
                }

                .header-content, main, footer {
                    width: min(100% - 2rem, 1120px);
                    margin-inline: auto;
                }

                .header-content { padding: 3rem 0 2.5rem; }

                .eyebrow {
                    margin: 0 0 0.75rem;
                    color: #8ed5cb;
                    font-family: ui-monospace, "Cascadia Mono", Consolas, monospace;
                    font-size: 0.78rem;
                    font-weight: 700;
                }

                h1 {
                    margin: 0;
                    font-size: 2.75rem;
                    line-height: 1.05;
                    letter-spacing: 0;
                }

                .subtitle {
                    max-width: 44rem;
                    margin: 0.8rem 0 0;
                    color: #cbdcda;
                    font-size: 1.05rem;
                }

                main { padding: 2.25rem 0 4rem; }

                .introduction {
                    max-width: 48rem;
                    margin: 0 0 1.5rem;
                    font-size: 1.05rem;
                    line-height: 1.65;
                }

                .stats {
                    display: flex;
                    flex-wrap: wrap;
                    gap: 1rem 2.5rem;
                    margin: 0 0 3rem;
                    padding: 1.25rem 0;
                    border-block: 1px solid #aebfc1;
                }

                .stats div { display: flex; align-items: baseline; gap: 0.5rem; }
                .stats dt { color: #006d77; font-size: 1.65rem; font-weight: 700; }
                .stats dd { margin: 0; color: #465b5f; }
                .ecosystem + .ecosystem { margin-top: 3.5rem; }

                .ecosystem-heading {
                    display: flex;
                    align-items: baseline;
                    justify-content: space-between;
                    gap: 1rem;
                    margin-bottom: 1rem;
                    border-bottom: 2px solid #16383c;
                }

                h2 { margin: 0 0 0.6rem; font-size: 1.65rem; letter-spacing: 0; }
                .count { color: #52666a; font-size: 0.88rem; }

                .dependency {
                    margin-bottom: 0.65rem;
                    overflow: hidden;
                    background: #ffffff;
                    border: 1px solid #bdcbcd;
                    border-left: 4px solid #008c95;
                    border-radius: 6px;
                }

                summary {
                    display: grid;
                    grid-template-columns: 0.75rem minmax(0, 1fr) auto auto;
                    gap: 1rem;
                    align-items: center;
                    min-height: 3.25rem;
                    padding: 0.75rem 1rem;
                    cursor: pointer;
                }

                summary:hover { background: #f1f7f6; }

                summary::before {
                    width: 0.45rem;
                    height: 0.45rem;
                    content: "";
                    border-right: 2px solid #006d77;
                    border-bottom: 2px solid #006d77;
                    transform: rotate(-45deg);
                    transition: transform 120ms ease-out;
                }

                details[open] summary::before { transform: rotate(45deg); }

                .package-name {
                    min-width: 0;
                    color: #143c41;
                    font-family: ui-monospace, "Cascadia Mono", Consolas, monospace;
                    font-size: 0.88rem;
                    font-weight: 700;
                    overflow-wrap: anywhere;
                }

                .version, .usage-count, .source-kind {
                    padding: 0.25rem 0.45rem;
                    border-radius: 4px;
                    font-family: ui-monospace, "Cascadia Mono", Consolas, monospace;
                    font-size: 0.72rem;
                }

                .version { color: #8a3f2d; background: #fff0eb; }
                .usage-count { color: #40575a; background: #edf2f2; }

                .dependency-content {
                    padding: 0 1rem 1.25rem;
                    border-top: 1px solid #d9e1e2;
                }

                h3 { margin: 1.25rem 0 0.65rem; font-size: 1rem; letter-spacing: 0; }
                h4 { margin: 1rem 0 0.45rem; font-size: 0.88rem; letter-spacing: 0; }

                .used-by, .source-paths {
                    display: flex;
                    flex-wrap: wrap;
                    gap: 0.4rem;
                    margin: 0;
                    padding: 0;
                    list-style: none;
                }

                .used-by li, .source-paths li {
                    padding: 0.25rem 0.45rem;
                    color: #31484b;
                    background: #edf4f3;
                    border: 1px solid #c7d8d6;
                    border-radius: 4px;
                    font-family: ui-monospace, "Cascadia Mono", Consolas, monospace;
                    font-size: 0.75rem;
                    overflow-wrap: anywhere;
                }

                .notice + .notice { margin-top: 2rem; border-top: 1px solid #bdcbcd; }

                .notice-heading {
                    display: flex;
                    align-items: center;
                    justify-content: space-between;
                    gap: 1rem;
                }

                .source-kind { color: #ffffff; background: #006d77; }
                .provenance, .review { margin: 0.6rem 0; color: #3e5256; line-height: 1.5; }
                a { color: #006d77; overflow-wrap: anywhere; }

                .legal-text {
                    max-height: 34rem;
                    margin: 1rem 0 0;
                    padding: 1rem;
                    overflow: auto;
                    color: #1c272a;
                    background: #f7f8f5;
                    border: 1px solid #d3d9d5;
                    border-radius: 4px;
                    font-family: ui-monospace, "Cascadia Mono", Consolas, monospace;
                    font-size: 0.78rem;
                    line-height: 1.5;
                    white-space: pre-wrap;
                    overflow-wrap: anywhere;
                }

                footer {
                    padding: 1.5rem 0 2.5rem;
                    color: #52666a;
                    border-top: 1px solid #aebfc1;
                    font-size: 0.85rem;
                }

                @media (max-width: 700px) {
                    .header-content { padding: 2.25rem 0 2rem; }
                    h1 { font-size: 2.1rem; }
                    summary { grid-template-columns: 0.75rem minmax(0, 1fr) auto; gap: 0.55rem; }
                    .usage-count { grid-column: 2 / -1; justify-self: start; }
                    .ecosystem-heading { align-items: flex-start; flex-direction: column; gap: 0; }
                }

                @media print {
                    :root, body { background: #ffffff; }
                    header { color: #172126; background: #ffffff; border-bottom-color: #172126; }
                    .eyebrow, .subtitle { color: #172126; }
                    .dependency { break-inside: avoid; }
                      .dependency:not([open]) .dependency-content { display: block !important; }
                    .legal-text { max-height: none; overflow: visible; }
                }
                """;

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