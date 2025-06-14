// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;

namespace Tyger.Common.Api;

public static class QueryParameters
{
    public static Dictionary<string, string>? GetTagQueryParameters(this HttpContext context, string prefix = "tag")
    {
        Dictionary<string, string>? tagQuery = null;
        foreach (var tag in context.Request.Query)
        {
            if (tag.Key.StartsWith(prefix + "[", StringComparison.Ordinal) && tag.Key.EndsWith(']') && tag.Value.Count > 0)
            {
                var start = prefix.Length + 1;
                (tagQuery ??= []).Add(tag.Key[start..^1], tag.Value.FirstOrDefault() ?? "");
            }
        }

        return tagQuery;
    }

    /// <summary>
    /// Parses the "ttl" query parameter from the request and validates it.
    /// </summary>
    /// <param name="context">The HTTP context.</param>
    /// <param name="ttl">The parsed TTL value, or null if not provided.</param>
    /// <param name="key">The key to look for in the query string. Default is "ttl".</param>
    /// <returns>true if TTL was parsed or not provided, false if the TTL is invalid</returns>
    public static bool ParseAndValidateTtlQueryParameter(this HttpContext context, out TimeSpan? ttl, string key = "ttl")
    {
        ttl = null;
        if (context.Request.Query.TryGetValue(key, out var ttlValues))
        {
            if (!TimeSpan.TryParse(ttlValues, out var ttlParsed))
            {
                return false;
            }

            // TTL is invalid if it is negative
            if (ttlParsed < TimeSpan.Zero)
            {
                return false;
            }

            ttl = ttlParsed;
        }

        return true;
    }

    public static uint GetValidatedPageLimit(int? limit, uint defaultLimit = 20, uint maxLimit = 2000)
    {
        return limit switch
        {
            null => defaultLimit,
            < 0 => throw new ValidationException("Limit must be a non-negative integer."),
            _ => (uint)Math.Min(limit.Value, maxLimit)
        };
    }
}
