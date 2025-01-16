using Tyger.ControlPlane.Model;

namespace Tyger.ControlPlane.Database;

public interface IRunAugmenter
{
    Task<Run> AugmentRun(Run run, CancellationToken cancellationToken);
}
