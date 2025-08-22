// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.AccessControl;

public static partial class LogingExtensions
{
    [LoggerMessage(LogLevel.Critical, "Missing authorization specification on {endpointDisplayName}")]
    public static partial void AuthorizationNotSpecifiedOnEndpoint(this ILogger logger, string endpointDisplayName);

    [LoggerMessage(LogLevel.Information, "MISE authentication denied: {reason}")]
    public static partial void MiseAuthentationDenied(this ILogger logger, string reason);
}
