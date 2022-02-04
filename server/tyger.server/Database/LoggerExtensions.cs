namespace Tyger.Server.Database;

public static partial class LoggerExtensions
{
    [LoggerMessage(0, LogLevel.Information, "Upserting codespec {name}")]
    public static partial void UpsertingCodespec(this ILogger logger, string name);

    [LoggerMessage(1, LogLevel.Information, "Conflict when upserting codespec {name}")]
    public static partial void UpsertingCodespecConflict(this ILogger logger, string name);
}
