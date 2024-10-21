// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using Docker.DotNet;
using Tyger.ControlPlane.Buffers;
using Tyger.ControlPlane.Database;
using Tyger.ControlPlane.Logging;
using Tyger.ControlPlane.Runs;
using Tyger.ControlPlane.ServiceMetadata;

namespace Tyger.ControlPlane.Compute.Docker;

public static class Docker
{
    public static void AddDocker(this IHostApplicationBuilder builder)
    {
        if (builder is WebApplicationBuilder)
        {
            builder.Services.AddOptions<DockerOptions>().BindConfiguration("compute:docker").ValidateDataAnnotations().ValidateOnStart();
        }

        builder.Services.AddSingleton(sp => new DockerClientConfiguration().CreateClient());

        builder.Services.AddSingleton<IReplicaDatabaseVersionProvider, DockerReplicaDatabaseVersionProvider>();
        builder.Services.AddSingleton<DockerRunCreator>();
        builder.Services.AddSingleton<IRunCreator>(sp => sp.GetRequiredService<DockerRunCreator>());
        builder.Services.AddSingleton(sp => (IHostedService)sp.GetRequiredService<IRunCreator>());
        builder.Services.AddSingleton<ICapabilitiesContributor>(sp => sp.GetRequiredService<DockerRunCreator>());
        builder.Services.AddSingleton<IRunReader, DockerRunReader>();
        builder.Services.AddSingleton<IRunUpdater, DockerRunUpdater>();
        builder.Services.AddSingleton<ILogSource, DockerLogSource>();
        builder.Services.AddSingleton<DockerRunSweeper>();
        builder.Services.AddSingleton<IRunSweeper>(sp => sp.GetRequiredService<DockerRunSweeper>());
        builder.Services.AddSingleton<IHostedService>(sp => sp.GetRequiredService<DockerRunSweeper>());
        builder.Services.AddSingleton<DockerEphemeralBufferProvider>();
        builder.Services.AddSingleton<IEphemeralBufferProvider>(sp => sp.GetRequiredService<DockerEphemeralBufferProvider>());
    }
}

public class DockerOptions
{
    [Required]
    public required string RunSecretsPath { get; set; }

    [Required]
    public required string EphemeralBuffersPath { get; set; }

    public bool GpuSupport { get; set; }

    [Required]
    public required string NetworkName { get; set; }
}
