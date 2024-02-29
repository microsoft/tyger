

using System.Diagnostics.Contracts;
using System.Globalization;
using System.Text;
using Microsoft.AspNetCore.Http;
using Microsoft.AspNetCore.Http.Extensions;

namespace Tyger.Buffers;

public static class LocalSasHandler
{
    private const string CurrentSasVersion = "0.1.0";
    public const string SasTimeFormat = "yyyy-MM-ddTHH:mm:ssZ";

    public static QueryString GetSasQueryString(string containerId, SasResourceType resource, SasAction action, SignDataFunc signData)
    {
        var startTime = DateTimeOffset.UtcNow;
        var endTime = startTime.AddHours(1);

        string permissions = (resource, action) switch
        {
            (SasResourceType.Container, SasAction.Create) => "C",
            (SasResourceType.Container, SasAction.Read) => "R",
            (SasResourceType.Container, SasAction.Read | SasAction.Create) => "CR",
            (SasResourceType.Blob, SasAction.Create) => "c",
            (SasResourceType.Blob, SasAction.Read) => "r",
            (SasResourceType.Blob, SasAction.Read | SasAction.Create) => "cr",
            _ => throw new ArgumentException("Invalid resource and action combination")
        };

        var stringToSign = string.Join("\n",
            CurrentSasVersion,
            containerId,
            permissions,
            FormatTimeForSasSigning(startTime),
            FormatTimeForSasSigning(endTime));

        var signature = Convert.ToBase64String(signData(Encoding.UTF8.GetBytes(stringToSign)));

        var queryBuilder = new QueryBuilder {
            { "sv", CurrentSasVersion },
            { "sp", permissions },
            { "st", FormatTimeForSasSigning(startTime) },
            { "se", FormatTimeForSasSigning(endTime) },
            { "sig", signature }
        };

        return queryBuilder.ToQueryString();
    }

    internal static string FormatTimeForSasSigning(DateTimeOffset time) =>
        (time == default) ? "" : time.ToString(SasTimeFormat, CultureInfo.InvariantCulture);

    public static SasValidationResult ValidateRequest(string containerId, SasResourceType resourceType, SasAction action, IQueryCollection query, ValidateSignatureFunc validateSignature)
    {
        if (!query.TryGetValue("sv", out var sv) || sv != CurrentSasVersion)
        {
            return SasValidationResult.InvalidSas;
        }

        if (!query.TryGetValue("sp", out var sp))
        {
            return SasValidationResult.InvalidSas;
        }

        if (!query.TryGetValue("st", out var st) || !DateTimeOffset.TryParseExact(st, SasTimeFormat, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var startTime))
        {
            return SasValidationResult.InvalidSas;
        }

        if (!query.TryGetValue("se", out var se) || !DateTimeOffset.TryParseExact(se, SasTimeFormat, CultureInfo.InvariantCulture, DateTimeStyles.AssumeUniversal, out var endTime))
        {
            return SasValidationResult.InvalidSas;
        }

        if (!query.TryGetValue("sig", out var sig))
        {
            return SasValidationResult.InvalidSas;
        }

        var now = DateTimeOffset.UtcNow;

        if (now < startTime || now > endTime)
        {
            return SasValidationResult.InvalidSas;
        }

        var stringToSign = string.Join("\n",
            CurrentSasVersion,
            containerId,
            sp,
            st,
            se);

        if (!validateSignature(Encoding.UTF8.GetBytes(stringToSign), Convert.FromBase64String(sig.ToString())))
        {
            return SasValidationResult.InvalidSas;
        }

        switch (resourceType)
        {
            case SasResourceType.Container:
                if (action.HasFlag(SasAction.Create) && !sp.ToString().Contains('C'))
                {
                    return SasValidationResult.ActionNotAllowed;
                }

                if (action.HasFlag(SasAction.Read) && !sp.ToString().Contains('R'))
                {
                    return SasValidationResult.ActionNotAllowed;
                }

                break;
            case SasResourceType.Blob:
                if (action.HasFlag(SasAction.Create) && !sp.ToString().Contains('c'))
                {
                    return SasValidationResult.ActionNotAllowed;
                }

                if (action.HasFlag(SasAction.Read) && !sp.ToString().Contains('r'))
                {
                    return SasValidationResult.ActionNotAllowed;
                }

                break;
            default:
                throw new ArgumentException("Invalid resource type");
        }

        return SasValidationResult.ActionAllowed;
    }
}

[Flags]
public enum SasAction
{
    None = 0,
    Read = 1,
    Create = 1 << 1,
}

public enum SasResourceType
{
    Container,
    Blob
}

public enum SasValidationResult
{
    InvalidSas,
    ActionAllowed,
    ActionNotAllowed
}
