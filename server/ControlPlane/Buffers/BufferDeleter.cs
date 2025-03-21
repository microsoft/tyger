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
        const int MaxLimit = 1000;
        bool hasMoreExpired = true;

        while (hasMoreExpired && !stoppingToken.IsCancellationRequested)
        {
            var idsToPurge = await _repository.GetExpiredBufferIds(whereSoftDeleted: true, limit: MaxLimit, stoppingToken);
            if (idsToPurge.Count <= 0)
            {
                break;
            }

            var deletedFromProvider = await _bufferProvider.DeleteBuffers(idsToPurge, stoppingToken);
            var numDeletedFromDatabase = await _repository.HardDeleteBuffers(idsToPurge, stoppingToken);
            _logger.HardDeletedBuffers(deletedFromProvider.Count, numDeletedFromDatabase);

            // If we got MaxLimit ids, it is very likely there are more to purge
            hasMoreExpired = idsToPurge.Count >= MaxLimit;
            if (hasMoreExpired)
            {
                await Task.Delay(TimeSpan.FromSeconds(1), stoppingToken);
            }
        }
    }

    /// <summary>
    /// Soft deletes expired buffers that are not already soft deleted.
    /// </summary>
    private async Task SoftDeletedExpiredBuffers(CancellationToken stoppingToken)
    {
        var count = await _bufferManager.SoftDeleteExpiredBuffers(stoppingToken);
        if (count > 0)
        {
            _logger.SoftDeletedBuffers(count);
        }
    }
}
