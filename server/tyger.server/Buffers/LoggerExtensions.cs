namespace Tyger.Server.Buffers;

public static partial class LogingExtensions
{
    [LoggerMessage(0, LogLevel.Information, "Creating buffer {bufferId}")]
    public static partial void CreatingBuffer(this ILogger logger, string bufferId);
}
