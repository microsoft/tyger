// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Text.RegularExpressions;
using Microsoft.Extensions.Options;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Model;
using Buffer = Tyger.ControlPlane.Model.Buffer;

namespace Tyger.ControlPlane.Buffers;

public sealed partial class BufferManager
{
    private readonly Repository _repository;
    private readonly IBufferProvider _bufferProvider;
    private readonly IEphemeralBufferProvider _ephemeralBufferProvider;
    private readonly ILogger<BufferManager> _logger;
    private readonly IOptions<BufferOptions> _bufferOptions;

    public BufferManager(Repository repository, IBufferProvider bufferProvider, IEphemeralBufferProvider ephemeralBufferProvider, ILogger<BufferManager> logger, IOptions<BufferOptions> bufferOptions)
    {
        _repository = repository;
        _bufferProvider = bufferProvider;
        _ephemeralBufferProvider = ephemeralBufferProvider;
        _logger = logger;
        _bufferOptions = bufferOptions;
    }

    public async Task<Buffer> CreateBuffer(Buffer newBuffer, CancellationToken cancellationToken)
    {
        Tags.Validate(newBuffer.Tags);

        string id = UniqueId.Create();
        _logger.CreatingBuffer(id);

        var buffer = newBuffer with { Id = id };

        return await _bufferProvider.CreateBuffer(buffer, cancellationToken);
    }

    public async Task<Buffer?> GetBufferById(string id, bool softDeleted, CancellationToken cancellationToken)
    {
        return await _repository.GetBuffer(id, softDeleted, cancellationToken);
    }

    public async Task<UpdateWithPreconditionResult<Buffer>> SoftDeleteBufferById(string id, TimeSpan? ttl, bool purge, CancellationToken cancellationToken)
    {
        var expiresAt = GetDefaultDeletedBufferExpiresAt();
        if (ttl.HasValue)
        {
            expiresAt = ComputeExpiration(ttl.Value);
        }

        return await _repository.SoftDeleteBuffer(id, expiresAt, purge, cancellationToken);
    }

    public async Task<UpdateWithPreconditionResult<Buffer>> RestoreBufferById(string id, CancellationToken cancellationToken)
    {
        var expiresAt = GetDefaultActiveBufferExpiresAt();
        return await _repository.RestoreBuffer(id, expiresAt, cancellationToken);
    }

    public async Task<bool> CheckBuffersExist(ICollection<string> ids, CancellationToken cancellationToken)
    {
        return await _repository.CheckBuffersExist(ids, cancellationToken);
    }

    public async Task<UpdateWithPreconditionResult<Buffer>> UpdateBuffer(BufferUpdate bufferUpdate, string? eTagPrecondition, CancellationToken cancellationToken)
    {
        return await _repository.UpdateBuffer(bufferUpdate, eTagPrecondition, cancellationToken);
    }

    public async Task<(IList<Buffer>, string? nextContinuationToken)> GetBuffers(IDictionary<string, string>? tags, IDictionary<string, string>? excludeTags,
            bool softDeleted, int limit, string? continuationToken, CancellationToken cancellationToken)
    {
        return await _repository.GetBuffers(tags, excludeTags, softDeleted, limit, continuationToken, cancellationToken);
    }

    public async Task<int> SoftDeleteBuffers(IDictionary<string, string>? tags, IDictionary<string, string>? excludeTags, TimeSpan? ttl, bool purge, CancellationToken cancellationToken)
    {
        var expiresAt = GetDefaultDeletedBufferExpiresAt();
        if (ttl.HasValue)
        {
            expiresAt = ComputeExpiration(ttl.Value);
        }

        return await _repository.SoftDeleteBuffers(tags, excludeTags, expiresAt, purge, cancellationToken);
    }

    public async Task<int> SoftDeleteExpiredBuffers(CancellationToken cancellationToken)
    {
        var expiresAt = GetDefaultDeletedBufferExpiresAt();
        return await _repository.SoftDeleteExpiredBuffers(expiresAt, cancellationToken);
    }

    public async Task<int> RestoreBuffers(IDictionary<string, string>? tags, IDictionary<string, string>? excludeTags, CancellationToken cancellationToken)
    {
        var expiresAt = GetDefaultActiveBufferExpiresAt();
        return await _repository.RestoreBuffers(tags, excludeTags, expiresAt, cancellationToken);
    }

    public async Task<int> GetBufferCount(IDictionary<string, string>? tags, IDictionary<string, string>? excludeTags, bool? softDeleted, CancellationToken cancellationToken)
    {
        return await _repository.GetBufferCount(tags, excludeTags, softDeleted, cancellationToken);
    }

    internal async Task<IList<(string id, bool writeable, BufferAccess? bufferAccess)>> CreateBufferAccessUrls(IList<(string id, bool writeable)> requests, bool preferTcp, bool fromDocker, bool checkExists, CancellationToken cancellationToken)
    {
        IList<(string id, bool writeable)> nonEphemeralRequests = requests;
        List<(string id, bool writeable, BufferAccess? bufferAccess)>? responses = null;

        for (int i = 0; i < requests.Count; i++)
        {
            var (fullId, writeable) = requests[i];
            var match = BufferIdRegex().Match(fullId);
            if (!match.Success)
            {
                (responses ??= []).Add((fullId, writeable, null));
                continue;
            }

            var id = match.Groups["BUFFERID"].Value;

            if (match.Groups["TEMP"].Success)
            {
                if (nonEphemeralRequests == requests)
                {
                    nonEphemeralRequests = new List<(string id, bool writeable)>(requests.Count);
                    for (int j = 0; j < i; j++)
                    {
                        nonEphemeralRequests.Add(requests[j]);
                    }
                }

                var runIdGroup = match.Groups["RUNID"];
                responses ??= [];
                if (runIdGroup.Success)
                {
                    var url = await _ephemeralBufferProvider.CreateBufferAccessUrl(id, writeable, preferTcp, fromDocker, cancellationToken);
                    responses.Add((fullId, writeable, url == null ? null : new BufferAccess(url)));
                }
                else
                {
                    responses.Add((fullId, writeable, new BufferAccess(new Uri("temporary", UriKind.Relative))));
                }
            }
            else
            {
                if (nonEphemeralRequests != requests)
                {
                    nonEphemeralRequests.Add(requests[i]);
                }
            }
        }

        if (nonEphemeralRequests.Count > 0)
        {
            var nonEphemeralResponses = await _bufferProvider.CreateBufferAccessUrls(nonEphemeralRequests, preferTcp, checkExists, cancellationToken);
            if (responses == null)
            {
                return nonEphemeralResponses;
            }

            responses.AddRange(nonEphemeralResponses);
            return responses;
        }

        return responses ?? [];
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

    public async Task<Run> ImportBuffers(ImportBuffersRequest importBuffersRequest, CancellationToken cancellationToken)
    {
        return await _bufferProvider.ImportBuffers(importBuffersRequest, cancellationToken);
    }

    internal IList<StorageAccount> GetStorageAccounts()
    {
        return _bufferProvider.GetStorageAccounts();
    }

    internal static DateTimeOffset ComputeExpiration(TimeSpan ttl)
    {
        if (ttl < TimeSpan.Zero)
        {
            throw new ArgumentOutOfRangeException(nameof(ttl), "TTL must be greater than zero.");
        }

        return DateTime.UtcNow.Add(ttl);
    }

    internal DateTimeOffset? GetDefaultActiveBufferExpiresAt()
    {
        if (_bufferOptions.Value.ActiveLifetime != TimeSpan.Zero)
        {
            return ComputeExpiration(_bufferOptions.Value.ActiveLifetime);
        }

        return null;
    }

    internal DateTimeOffset GetDefaultDeletedBufferExpiresAt()
    {
        return ComputeExpiration(_bufferOptions.Value.SoftDeletedLifetime);
    }

    [GeneratedRegex(@"^(?<TEMP>(run-(?<RUNID>\d+)-)?temp-)?(?<BUFFERID>\w+)$")]
    private static partial Regex BufferIdRegex();
}
