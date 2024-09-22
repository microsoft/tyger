// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Text.RegularExpressions;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Buffer = Tyger.ControlPlane.Model.Buffer;

namespace Tyger.ControlPlane.Buffers;

public sealed partial class BufferManager
{
    private readonly IRepository _repository;
    private readonly IBufferProvider _bufferProvider;
    private readonly IEphemeralBufferProvider _ephemeralBufferProvider;
    private readonly ILogger<BufferManager> _logger;

    public BufferManager(IRepository repository, IBufferProvider bufferProvider, IEphemeralBufferProvider ephemeralBufferProvider, ILogger<BufferManager> logger)
    {
        _repository = repository;
        _bufferProvider = bufferProvider;
        _ephemeralBufferProvider = ephemeralBufferProvider;
        _logger = logger;
    }

    public async Task<Buffer> CreateBuffer(Buffer newBuffer, CancellationToken cancellationToken)
    {
        if (newBuffer.Tags != null)
        {
            string keyPattern = @"^[a-zA-Z0-9-_.]{1,128}$";
            string valuePattern = @"^[a-zA-Z0-9-_.]{0,256}$";

            foreach (var tag in newBuffer.Tags)
            {
                if (!Regex.IsMatch(tag.Key, keyPattern))
                {
                    throw new ValidationException("Tag keys must contain up to 128 letters (a-z, A-Z), numbers (0-9) and underscores (_)");
                }

                if (!Regex.IsMatch(tag.Value, valuePattern))
                {
                    throw new ValidationException("Tag values can contain up to 256 letters (a-z, A-Z), numbers (0-9) and underscores (_)");
                }
            }

            if (newBuffer.Tags.Count > 10)
            {
                throw new ValidationException("Only 10 tags can be set on a buffer");
            }
        }

        string id = UniqueId.Create();
        _logger.CreatingBuffer(id);
        await _bufferProvider.CreateBuffer(id, cancellationToken);
        return await _repository.CreateBuffer(newBuffer with { Id = id }, cancellationToken);
    }

    public async Task<Buffer?> GetBufferById(string id, CancellationToken cancellationToken)
    {
        return await GetBufferById(id, "", cancellationToken);
    }

    public async Task<Buffer?> GetBufferById(string id, string eTag, CancellationToken cancellationToken)
    {
        var buffer = await _repository.GetBuffer(id, eTag, cancellationToken);

        if (buffer == null)
        {
            return null;
        }

        if (await _bufferProvider.BufferExists(id, cancellationToken))
        {
            return buffer;
        }

        return null;
    }

    public async Task<Buffer?> UpdateBufferById(string id, string eTag, IDictionary<string, string>? tags, CancellationToken cancellationToken)
    {
        return await _repository.UpdateBufferById(id, eTag, tags, cancellationToken);
    }

    public async Task<(IList<Buffer>, string? nextContinuationToken)> GetBuffers(IDictionary<string, string>? tags, int limit, string? continuationToken, CancellationToken cancellationToken)
    {
        return await _repository.GetBuffers(tags, limit, continuationToken, cancellationToken);
    }

    internal async Task<BufferAccess?> CreateBufferAccessUrl(string id, bool writeable, bool preferTcp, bool fromDocker, CancellationToken cancellationToken)
    {
        var match = BufferIdRegex().Match(id);
        if (!match.Success)
        {
            return null;
        }

        id = match.Groups["BUFFERID"].Value;

        if (match.Groups["TEMP"].Success)
        {
            var runIdGroup = match.Groups["RUNID"];
            if (runIdGroup.Success)
            {
                var url = await _ephemeralBufferProvider.CreateBufferAccessUrl(id, writeable, preferTcp, fromDocker, cancellationToken);
                return url == null ? null : new BufferAccess(url);
            }

            return new BufferAccess(new Uri("temporary", UriKind.Relative));
        }

        if (await GetBufferById(id, cancellationToken) is null)
        {
            return null;
        }

        return new BufferAccess(_bufferProvider.CreateBufferAccessUrl(id, writeable, preferTcp));
    }

    public string GetUnqualifiedBufferId(string id)
    {
        var match = BufferIdRegex().Match(id);
        if (!match.Success)
        {
            return id;
        }

        return match.Groups["BUFFERID"].Value;
    }

    public async Task<Run> ExportBuffers(ExportBuffersRequest exportBufferRequest, CancellationToken cancellationToken)
    {
        return await _bufferProvider.ExportBuffers(exportBufferRequest, cancellationToken);
    }

    public async Task<Run> ImportBuffers(CancellationToken cancellationToken)
    {
        return await _bufferProvider.ImportBuffers(cancellationToken);
    }

    [GeneratedRegex(@"^(?<TEMP>(run-(?<RUNID>\d+)-)?temp-)?(?<BUFFERID>\w+)$")]
    private static partial Regex BufferIdRegex();
}
