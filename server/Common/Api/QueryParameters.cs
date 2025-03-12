// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

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
}
