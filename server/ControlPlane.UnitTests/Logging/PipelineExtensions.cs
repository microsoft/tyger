// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.IO.Pipelines;
using Tyger.ControlPlane.Logging;

namespace Tyger.ControlPlane.UnitTests.Logging;

public static class PipelineExtensions
{
    /// <summary>
    /// A helper for unit tests. Runs the given pipeline and reads the output as a string.
    /// </summary>
    public static async Task<string> ReadAllAsString(this IPipelineSource p)
    {
        if (p is not Pipeline)
        {
            // the pipeline takes care of closing readers and writers.
            p = new Pipeline(p);
        }

        var pipe = new Pipe();
        using var reader = new StreamReader(pipe.Reader.AsStream());
        _ = p.Process(pipe.Writer, CancellationToken.None);
        return await reader.ReadToEndAsync();
    }
}
