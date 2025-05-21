// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Globalization;
using Tyger.ControlPlane.Buffers;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Runs;

public abstract class RunCreatorBase : BackgroundService
{
    protected RunCreatorBase(Repository repository, BufferManager bufferManager)
    {
        Repository = repository;
        BufferManager = bufferManager;
    }

    protected Repository Repository { get; init; }

    protected BufferManager BufferManager { get; init; }

    protected async Task ProcessBufferArguments(BufferParameters? parameters, Dictionary<string, string> arguments, Dictionary<string, string>? tags, TimeSpan? bufferTtl, CancellationToken cancellationToken)
    {
        if (arguments != null)
        {
            var nonEphemeralArguments = arguments.Values.Where(a => a != "_").ToList();
            if (!await BufferManager.CheckBuffersExist(nonEphemeralArguments, cancellationToken))
            {
                var singleIdArray = new string[1];
                foreach (var bufferId in nonEphemeralArguments)
                {
                    singleIdArray[0] = bufferId;
                    if (!await BufferManager.CheckBuffersExist(singleIdArray, cancellationToken))
                    {
                        throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The buffer '{0}' was not found", bufferId));
                    }
                }
            }
        }

        Dictionary<string, string> argumentsClone = arguments == null ? new(StringComparer.OrdinalIgnoreCase) : new(arguments, StringComparer.OrdinalIgnoreCase);
        var combinedParameters = (parameters?.Inputs is null
                ? parameters?.Outputs
                : (parameters?.Outputs is null ? parameters?.Inputs : parameters.Inputs.Concat(parameters.Outputs))
            ) ?? [];

        foreach (var param in combinedParameters)
        {
            if (!argumentsClone.TryGetValue(param, out var bufferId))
            {
                var newTags = new Dictionary<string, string>(tags ??= []) { ["bufferName"] = param };
                DateTimeOffset? expiresAt = bufferTtl.HasValue ? DateTime.UtcNow.Add(bufferTtl.Value) : null;
                var newBuffer = new Model.Buffer() { Tags = newTags, ExpiresAt = expiresAt };
                var buffer = await BufferManager.CreateBuffer(newBuffer, cancellationToken);
                bufferId = buffer.Id!;
                arguments![param] = bufferId;
            }
            else if (bufferId == "_")
            {
                bufferId = $"temp-{UniqueId.Create()}";
                arguments![param] = bufferId;
            }

            argumentsClone.Remove(param);
        }

        foreach (var arg in argumentsClone)
        {
            throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "Buffer argument '{0}' does not correspond to a buffer parameter on the codespec", arg));
        }
    }

    // Assuming arguments are already validated
    protected async Task<Dictionary<string, (bool write, Uri sasUri)>> GetBufferMap(BufferParameters? parameters, Dictionary<string, string> arguments, TimeSpan? accessTtl, CancellationToken cancellationToken)
    {
        if (arguments is null or { Count: 0 })
        {
            return [];
        }

        var requests = new List<(string id, bool writeable)>();

        if (parameters?.Inputs is not null)
        {
            foreach (var param in parameters.Inputs)
            {
                requests.Add((arguments[param], false));
            }
        }

        if (parameters?.Outputs is not null)
        {
            foreach (var param in parameters.Outputs)
            {
                requests.Add((arguments[param], true));
            }
        }

        var responses = await BufferManager.CreateBufferAccessUrls(requests, preferTcp: false, fromDocker: false, checkExists: false, accessTtl, cancellationToken);
        var outputMap = new Dictionary<string, (bool write, Uri sasUri)>();
        foreach (var (id, writeable, bufferAccess) in responses)
        {
            if (bufferAccess is not null)
            {
                var paramName = writeable ? parameters!.Outputs!.First(p => arguments[p] == id) : parameters!.Inputs!.First(p => arguments[p] == id);
                outputMap[paramName] = (writeable, bufferAccess.Uri);
            }
            else
            {
                throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The buffer '{0}' was not found", id));
            }
        }

        return outputMap;
    }

    protected DateTimeOffset CalculateProactiveRefreshTimeFromNow(TimeSpan ttl)
    {
        return DateTimeOffset.UtcNow + TimeSpan.FromTicks((long)(ttl.Ticks * 0.70));
    }
}
