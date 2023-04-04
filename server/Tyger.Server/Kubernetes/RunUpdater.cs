using Tyger.Server.Database;
using Tyger.Server.Model;

namespace Tyger.Server.Kubernetes;

public class RunUpdater
{
    private readonly IRepository _repository;
    private readonly ILogger<RunUpdater> _logger;

    public RunUpdater(
        IRepository repository,
        ILogger<RunUpdater> logger)
    {
        _repository = repository;
        _logger = logger;
    }

    public async Task<Run?> CancelRun(long id, CancellationToken cancellationToken)
    {
        if (await _repository.GetRun(id, cancellationToken) is not (Run run, var final, _))
        {
            return null;
        }

        if (final || run.Status is "Succeeded" or "Failed")
        {
            return run;
        }

        Run newRun = run with
        {
            Status = "Failed",
            StatusReason = "Canceled"
        };

        await _repository.UpdateRun(newRun, cancellationToken: cancellationToken);
        _logger.LogInformation("Canceling job {0}", id);

        return newRun;
    }
}
