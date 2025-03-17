// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Database;

namespace Tyger.ControlPlane.Buffers;

/// <summary>
/// Deletes all expired buffers.
/// </summary>
public class BufferDeleter : BackgroundService
{
    private readonly Repository _repository;
    private readonly BufferManager _bufferManager;
    private readonly IBufferProvider _bufferProvider;
    private readonly ILogger<BufferDeleter> _logger;

    public BufferDeleter(Repository repository, BufferManager manager, IBufferProvider bufferProvider, ILogger<BufferDeleter> logger)
    {
        _repository = repository;
        _bufferManager = manager;
        _bufferProvider = bufferProvider;
        _logger = logger;
    }

    protected override async Task ExecuteAsync(CancellationToken stoppingToken)
    {
        while (!stoppingToken.IsCancellationRequested)
        {
            try
            {
                var timestamp = DateTimeOffset.UtcNow;
                await Task.Delay(TimeSpan.FromSeconds(30), stoppingToken);
                await Task.WhenAll(
                    HardDeleteExpiredBuffers(stoppingToken),
                    SoftDeletedExpiredBuffers(stoppingToken));
            }
            catch (OperationCanceledException) when (stoppingToken.IsCancellationRequested)
            {
                return;
            }
            catch (Exception e)
            {
                _logger.ErrorDuringBackgroundBufferDelete(e);
            }
        }
    }

    /// <summary>
    /// Purges expired buffers that are already soft deleted.
    /// </summary>
    private async Task HardDeleteExpiredBuffers(CancellationToken stoppingToken)
    {
        var readyToPurge = await _repository.GetExpiredBufferIds(whereSoftDeleted: true, stoppingToken);
        if (readyToPurge.Count > 0)
        {
            var deletedIds = await _bufferProvider.DeleteBuffers(readyToPurge, stoppingToken);
            await _repository.HardDeleteBuffers(deletedIds, stoppingToken);
            _logger.HardDeletedBuffers(deletedIds.Count);
        }
    }

    /// <summary>
    /// Soft deletes expired buffers that are not already soft deleted.
    /// </summary>
    private async Task SoftDeletedExpiredBuffers(CancellationToken stoppingToken)
    {
        var readyToSoftDelete = await _repository.GetExpiredBufferIds(whereSoftDeleted: false, stoppingToken);
        if (readyToSoftDelete.Count > 0)
        {
            foreach (var id in readyToSoftDelete)
            {
                await _bufferManager.SoftDeleteBufferById(id, null, false, stoppingToken);
            }

            _logger.SoftDeletedBuffers(readyToSoftDelete.Count);
        }
    }
}
