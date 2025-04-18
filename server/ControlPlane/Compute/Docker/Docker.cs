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
            builder.Services.AddOptions<DockerOptions>().BindConfiguration("compute:docker").ValidateDataAnnotations().ValidateOnStart().PostConfigure(options =>
            {
                options.HostPathTranslations = options.HostPathTranslations.ToDictionary(kvp => kvp.Key.EndsWith('/') ? kvp.Key : kvp.Key + "/", kvp => kvp.Value.EndsWith('/') ? kvp.Value : kvp.Value + "/");
            });
        }

        builder.Services.AddSingleton(sp => new DockerClientConfiguration().CreateClient());

        builder.Services.AddSingleton<IReplicaDatabaseVersionProvider, DockerReplicaDatabaseVersionProvider>();
        builder.Services.AddSingleton<DockerRunCreator>();
        builder.Services.AddSingleton<IRunCreator>(sp => sp.GetRequiredService<DockerRunCreator>());
        builder.Services.AddHostedService(sp => sp.GetRequiredService<DockerRunCreator>());
        builder.Services.AddSingleton<ICapabilitiesContributor>(sp => sp.GetRequiredService<DockerRunCreator>());
        builder.Services.AddSingleton<DockerRunReader>();
        builder.Services.AddSingleton<IRunReader>(sp => sp.GetRequiredService<DockerRunReader>());
        builder.Services.AddSingleton(sp => new Lazy<IRunAugmenter>(() => sp.GetRequiredService<DockerRunReader>()));
        builder.Services.AddSingleton<DockerRunUpdater>();
        builder.Services.AddSingleton<IRunUpdater>(sp => sp.GetRequiredService<DockerRunUpdater>());
        builder.Services.AddSingleton<ILogSource, DockerLogSource>();
        builder.Services.AddSingleton<DockerRunSweeper>();
        builder.Services.AddSingleton<IRunSweeper>(sp => sp.GetRequiredService<DockerRunSweeper>());
        builder.Services.AddHostedService(sp => sp.GetRequiredService<DockerRunSweeper>());
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

    public Dictionary<string, string> HostPathTranslations { get; set; } = [];
}
