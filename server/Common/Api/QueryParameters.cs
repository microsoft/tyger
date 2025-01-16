namespace Tyger.Common.Api;

public static class QueryParameters
{
    public static Dictionary<string, string>? GetTagQueryParameters(this HttpContext context)
    {
        Dictionary<string, string>? tagQuery = null;
        foreach (var tag in context.Request.Query)
        {
            if (tag.Key.StartsWith("tag[", StringComparison.Ordinal) && tag.Key.EndsWith(']') && tag.Value.Count > 0)
            {
                (tagQuery ??= []).Add(tag.Key[4..^1], tag.Value.FirstOrDefault() ?? "");
            }
        }

        return tagQuery;
    }
}
