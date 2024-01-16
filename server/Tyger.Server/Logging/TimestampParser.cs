// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Buffers;
using System.Buffers.Text;

namespace Tyger.Server.Logging;

public static class TimestampParser
{
    public static bool TryParseTimestampFromSequence(in ReadOnlySequence<byte> sequence, out DateTimeOffset timestamp)
    {
        if (sequence.IsSingleSegment)
        {
            return TryParseTimestampFromSpan(sequence.FirstSpan, out timestamp);
        }

        Span<byte> dateSpan = stackalloc byte[(int)sequence.Length];
        sequence.CopyTo(dateSpan);
        return TryParseTimestampFromSpan(dateSpan, out timestamp);
    }

    private static bool TryParseTimestampFromSpan(in ReadOnlySpan<byte> byteSpan, out DateTimeOffset timestamp)
    {
        return Utf8Parser.TryParse(byteSpan, out timestamp, out _, 'O');
    }
}
