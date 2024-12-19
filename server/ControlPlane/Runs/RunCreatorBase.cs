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

    protected async Task<Codespec> GetCodespec(ICodespecRef codespecRef, CancellationToken cancellationToken)
    {
        if (codespecRef is Codespec inlineCodespec)
        {
            return inlineCodespec;
        }

        if (codespecRef is not CommittedCodespecRef committedCodespecRef)
        {
            throw new InvalidOperationException("Invalid codespec reference");
        }

        if (committedCodespecRef.Version == null)
        {
            return await Repository.GetLatestCodespec(committedCodespecRef.Name, cancellationToken)
                ?? throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The codespec '{0}' was not found", committedCodespecRef.Name));
        }

        var codespec = await Repository.GetCodespecAtVersion(committedCodespecRef.Name, committedCodespecRef.Version.Value, cancellationToken);
        if (codespec == null)
        {
            // See if it's just the version number that was not found
            var latestCodespec = await Repository.GetLatestCodespec(committedCodespecRef.Name, cancellationToken)
                ?? throw new ValidationException(string.Format(CultureInfo.InvariantCulture, "The codespec '{0}' was not found", committedCodespecRef.Name));

            throw new ValidationException(
                string.Format(
                    CultureInfo.InvariantCulture,
                    "The version '{0}' of codespec '{1}' was not found. The latest version is '{2}'.",
                    committedCodespecRef.Version, committedCodespecRef.Name, latestCodespec.Version));
        }

        return codespec;
    }

    protected async Task ProcessBufferArguments(BufferParameters? parameters, Dictionary<string, string> arguments, Dictionary<string, string> tags, CancellationToken cancellationToken)
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
                var newTags = new Dictionary<string, string>(tags) { ["bufferName"] = param };
                var newBuffer = new Model.Buffer() { Tags = newTags };

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
    protected async Task<Dictionary<string, (bool write, Uri sasUri)>> GetBufferMap(BufferParameters? parameters, Dictionary<string, string> arguments, CancellationToken cancellationToken)
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

        var responses = await BufferManager.CreateBufferAccessUrls(requests, preferTcp: false, fromDocker: false, checkExists: false, cancellationToken);
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
}
