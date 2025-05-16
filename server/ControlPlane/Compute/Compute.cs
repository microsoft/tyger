// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Tyger.ControlPlane.Compute.Docker;
using Tyger.ControlPlane.Compute.Kubernetes;

namespace Tyger.ControlPlane.Compute;

public static class Compute
{
    public static void AddCompute(this IHostApplicationBuilder builder)
    {
        builder.Services.AddSingleton<RunBufferAccessRefresher>();
        builder.Services.AddHostedService(sp => sp.GetRequiredService<RunBufferAccessRefresher>());

        var kubernetesSection = builder.Configuration.GetSection("compute:kubernetes");
        var dockerSection = builder.Configuration.GetSection("compute:docker");
        switch (kubernetesSection.Exists(), dockerSection.Exists())
        {
            case (true, false):
                builder.AddKubernetes();
                break;
            case (false, true):
                builder.AddDocker();
                break;
            case (false, false):
                throw new InvalidOperationException("Either 'compute:kubernetes' or 'compute:docker' must be specified in the configuration.");
            case (true, true):
                throw new InvalidOperationException("Only one of 'compute:kubernetes' or 'compute:docker' can be specified in the configuration.");
        }
    }
}
