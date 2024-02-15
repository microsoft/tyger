// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using Microsoft.Extensions.Logging.Console;

namespace Tyger.Server.Logging;

public static class Logging
{
    public static void ConfigureLogging(this IHostApplicationBuilder builder)
    {
        builder.Logging.AddConsole(o => o.LogToStandardErrorThreshold = 0); // always log to stderr
        builder.Logging.AddConsoleFormatter<JsonFormatter, ConsoleFormatterOptions>();
        builder.Logging.Configure(l => l.ActivityTrackingOptions = ActivityTrackingOptions.None);
    }
}
