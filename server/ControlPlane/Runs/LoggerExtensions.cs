// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.Runs;

public static partial class LoggerExtensions
{
    [LoggerMessage(LogLevel.Error, "Error during run index prune.")]
    public static partial void ErrorDuringBackgroundIndexPrune(this ILogger logger, Exception exception);
}
