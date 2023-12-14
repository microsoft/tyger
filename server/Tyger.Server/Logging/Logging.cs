using Microsoft.Extensions.Logging.Console;

namespace Tyger.Server.Logging;

public static class Logging
{
    public static void ConfigureLogging(this ILoggingBuilder builder)
    {
        builder.AddConsoleFormatter<JsonFormatter, ConsoleFormatterOptions>();
        builder.Configure(l => l.ActivityTrackingOptions = ActivityTrackingOptions.None);
    }
}
