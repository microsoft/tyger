using System.Text.Json.Serialization;

namespace Tyger.ControlPlane.Model;

public record struct ObservedRunState(
    long Id,
    [property: JsonConverter(typeof(JsonStringEnumConverter))]
    [property:JsonIgnore(Condition = JsonIgnoreCondition.Never)]
    RunStatus Status
    )
{

    public ObservedRunState(Run run)
        : this(run.Id!.Value, run.Status!.Value)
    {
        StatusReason = run.StatusReason;
        RunningCount = run.RunningCount;
        FinishedAt = run.FinishedAt;
        JobNodePool = run.Job.NodePool;
        WorkerNodePool = run.Worker?.NodePool;
    }

    public string? StatusReason { get; init; }

    public int? RunningCount { get; init; }

    public DateTimeOffset? FinishedAt { get; init; }

    public string? JobNodePool { get; init; }

    public string? WorkerNodePool { get; init; }

    public readonly Run UpdateRun(Run run)
    {
        return run with
        {
            Status = Status,
            StatusReason = StatusReason,
            RunningCount = RunningCount,
            FinishedAt = FinishedAt,
            Job = run.Job with { NodePool = JobNodePool },
            Worker = run.Worker == null ? null : run.Worker with { NodePool = WorkerNodePool }
        };
    }
}
