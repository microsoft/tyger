// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.ComponentModel.DataAnnotations;
using System.Text.RegularExpressions;

namespace Tyger.ControlPlane.Model;

public static partial class Tags
{
    private const int MaxTags = 100;

    public static void Validate(IReadOnlyDictionary<string, string>? tags)
    {
        if (tags == null)
        {
            return;
        }

        if (tags.Count > MaxTags)
        {
            throw new ValidationException($"No more than {MaxTags} tags can be set on a buffer");
        }

        foreach (var tag in tags)
        {
            if (!TagKeyRegex().IsMatch(tag.Key))
            {
                throw new ValidationException("Tag keys must contain up to 128 letters (a-z, A-Z), numbers (0-9) and underscores (_)");
            }

            if (!TagValueRegex().IsMatch(tag.Value))
            {
                throw new ValidationException("Tag values can contain up to 256 letters (a-z, A-Z), numbers (0-9) and underscores (_)");
            }
        }
    }

    [GeneratedRegex(@"^[a-zA-Z0-9-_.]{1,128}$")]
    private static partial Regex TagKeyRegex();

    [GeneratedRegex(@"^[a-zA-Z0-9-_.]{0,256}$")]
    private static partial Regex TagValueRegex();
}
