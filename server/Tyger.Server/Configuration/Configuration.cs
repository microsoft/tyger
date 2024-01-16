// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.Server.Configuration;

public static class Configuration
{
    public static void AddConfigurationSources(this IConfigurationManager configurationManager)
    {
        configurationManager.AddJsonFile("appsettings.local.json", optional: true);
        if (configurationManager.GetValue<string>("KeyPerFileDirectory") is string keyPerFileDir)
        {
            configurationManager.AddKeyPerFile(keyPerFileDir, optional: true);
        }

        if (configurationManager.GetValue<string>("AppSettingsDirectory") is string settingsDir)
        {
            configurationManager.AddJsonFile(Path.Combine(settingsDir, "appsettings.json"), optional: false);
        }
    }
}
