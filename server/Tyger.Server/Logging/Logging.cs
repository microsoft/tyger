using Microsoft.Extensions.Logging.Console;

namespace Tyger.Server.Logging;

public static class Logging
{
    public static void ConfigureLogging(this ILoggingBuilder builder)
    {
        builder.AddConsole(o => o.LogToStandardErrorThreshold = 0); // always log to stderr
        builder.AddConsoleFormatter<JsonFormatter, ConsoleFormatterOptions>();
        builder.Configure(l => l.ActivityTrackingOptions = ActivityTrackingOptions.None);
    }
}
