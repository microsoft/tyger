// using Microsoft.Extensions.Options;
// using Tyger.ControlPlane.Model;

// namespace Tyger.ControlPlane.Compute.Kubernetes;

// public class JobResources
// {
//     private readonly k8s.Kubernetes _client;
//     private readonly KubernetesCoreOptions options;
//     private readonly Run _run;

//     public JobResources(k8s.Kubernetes client, IOptions<KubernetesCoreOptions> options, Run run)
//     {
//         _client = client;
//         _run = run;
//         this.options = options.Value;
//     }

//     public async Task<Run> GetUpdatedRun()
//     {
//         if (_run.Final)
//         {
//             return _run;
//         }
//     }
// }
