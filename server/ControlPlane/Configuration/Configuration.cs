// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Configuration;

public static class Configuration
{
    public static void AddConfigurationSources(this IHostApplicationBuilder builder)
    {
        builder.Configuration.AddJsonFile("appsettings.local.json", optional: true);
        if (builder.Configuration.GetValue<string>("KeyPerFileDirectory") is string keyPerFileDir)
        {
            builder.Configuration.AddKeyPerFile(keyPerFileDir, optional: true);
        }

        if (builder.Configuration.GetValue<string>("AppSettingsDirectory") is string settingsDir)
        {
            builder.Configuration.AddJsonFile(Path.Combine(settingsDir, "appsettings.json"), optional: false);
        }
    }
}
