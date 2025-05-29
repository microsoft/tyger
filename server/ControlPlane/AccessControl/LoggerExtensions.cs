// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

namespace Tyger.ControlPlane.AccessControl;

public static partial class LogingExtensions
{
    [LoggerMessage(LogLevel.Critical, "Missing authorization specification on {endpointDisplayName}")]
    public static partial void AuthorizationNotSpecifiedOnEndpoint(this ILogger logger, string endpointDisplayName);
}
