// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Text.Json.Serialization;
using Generator.Equals;

namespace Tyger.ControlPlane.Model;

/// <summary>
/// The state details of a run that can be determined by inspecting its Kubernetes objects.
/// </summary>
[Equatable]
public partial record struct ObservedRunState(
    long Id,
    [property: JsonConverter(typeof(JsonStringEnumConverter))]
    [property:JsonIgnore(Condition = JsonIgnoreCondition.Never)]
    RunStatus Status,
    int SpecifiedJobReplicaCount,
    int SpecifiedWorkerReplicaCount
    )
{

    public ObservedRunState(Run run, DateTimeOffset? databaseUpdatedAt)
        : this(run.Id!.Value, run.Status!.Value, run.Job.Replicas, run.Worker?.Replicas ?? 0)
    {
        StatusReason = run.StatusReason;
        RunningCount = run.RunningCount;
        StartedAt = run.StartedAt;
        FinishedAt = run.FinishedAt;
        JobNodePool = run.Job.NodePool;
        WorkerNodePool = run.Worker?.NodePool;
        DatabaseUpdatedAt = databaseUpdatedAt;
    }

    public string? StatusReason { get; init; }

    public int? RunningCount { get; init; }

    public DateTimeOffset? StartedAt { get; init; }

    public DateTimeOffset? FinishedAt { get; init; }

    public string? JobNodePool { get; init; }

    public string? WorkerNodePool { get; init; }

    [IgnoreEquality]
    public DateTimeOffset? DatabaseUpdatedAt { get; init; }

    [IgnoreEquality]
    public int? TagsVersion { get; init; }

    public readonly Run ApplyToRun(Run run)
    {
        return run with
        {
            Status = Status,
            StatusReason = StatusReason,
            RunningCount = RunningCount,
            StartedAt = StartedAt,
            FinishedAt = FinishedAt,
            Job = run.Job with { NodePool = JobNodePool },
            Worker = run.Worker == null ? null : run.Worker with { NodePool = WorkerNodePool }
        };
    }
}
