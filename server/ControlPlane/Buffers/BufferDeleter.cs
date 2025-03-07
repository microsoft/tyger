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
    private readonly IBufferProvider _bufferProvider;
    private readonly ILogger<BufferDeleter> _logger;

    public BufferDeleter(Repository repository, IBufferProvider bufferProvider, ILogger<BufferDeleter> logger)
    {
        _repository = repository;
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

                var ids = await _repository.GetExpiredBufferIds(stoppingToken);
                if (ids.Count == 0)
                {
                    continue;
                }

                var count = await _bufferProvider.DeleteBuffers(ids, stoppingToken);
                _logger.DeletedBuffers(count, ids.Count);
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
}
