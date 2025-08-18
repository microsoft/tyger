// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Buffers;
using System.IO.Pipelines;

namespace Tyger.ControlPlane.Logging;

/// <summary>
/// A pipeline of elements that process streaming data using PipeReader and PipeWriter.
/// </summary>
public class Pipeline : IPipelineSource
{
    private readonly IPipelineSource _source;
    private readonly List<IPipelineElement> _elements;

    public Pipeline(IPipelineSource source, params IPipelineElement[] elements)
    {
        _source = source;
        _elements = [.. elements];
    }

    public Pipeline(PipeReader reader, params IPipelineElement[] elements)
        : this(new SimplePipelineSource(reader), elements)
    {
    }

    public Pipeline(Stream stream, params IPipelineElement[] elements)
        : this(new SimplePipelineSource(stream), elements)
    {
    }

    public Pipeline(byte[] array, params IPipelineElement[] elements)
        : this(new SimplePipelineSource(array), elements)
    {
    }

    public Pipeline AddElement(IPipelineElement element)
    {
        _elements.Add(element);
        return this;
    }

    public async Task Process(PipeWriter writer, CancellationToken cancellationToken)
    {
        if (_elements.Count == 0)
        {
            try
            {
                await _source.Process(writer, cancellationToken);
                await writer.CompleteAsync();
                return;
            }
            catch (Exception e)
            {
                await writer.CompleteAsync(e);
                throw;
            }
        }

        PipeReader currentReader = _source.GetReader(cancellationToken);

        for (int i = 0; i < _elements.Count - 1; i++)
        {
            var pipe = new Pipe();
            _ = ProcessElement(_elements[i], currentReader, pipe.Writer, cancellationToken);
            currentReader = pipe.Reader;
        }

        await ProcessElement(_elements[^1], currentReader, writer, cancellationToken);
    }

    private static async Task ProcessElement(IPipelineElement element, PipeReader reader, PipeWriter writer, CancellationToken cancellationToken)
    {
        try
        {
            await element.Process(reader, writer, cancellationToken);
            await reader.CompleteAsync();
            await writer.CompleteAsync();
        }
        catch (Exception e)
        {
            await reader.CompleteAsync(e);
            await writer.CompleteAsync(e);
            throw;
        }
    }
}

/// <summary>
/// Processes data from a PipeReader and writes data to a PipeWriter.
/// Pipeline.Process takes care or closing the reader and writer.
/// </summary>
public interface IPipelineElement
{
    Task Process(PipeReader reader, PipeWriter writer, CancellationToken cancellationToken);
}

/// <summary>
/// The source or head of a pipeline.
/// </summary>
public interface IPipelineSource
{
    /// <summary>
    /// Writes data to the given PipeWriter. The implementation is responsible for cleaning up its own resources.
    /// The PipeWriter is cleaned up by the pipeline.
    /// </summary>
    Task Process(PipeWriter writer, CancellationToken cancellationToken);

    /// <summary>
    /// When this is a simple source, an implementation can provide an implementation that does not perform any copying.
    /// We provide this default implementation for other cases.
    /// </summary>
    PipeReader GetReader(CancellationToken cancellationToken)
    {
        var pipe = new Pipe();
        _ = ProcessAndComplete(this, pipe.Writer, cancellationToken);
        return pipe.Reader;

        static async Task ProcessAndComplete(IPipelineSource source, PipeWriter writer, CancellationToken cancellationToken)
        {
            try
            {
                await source.Process(writer, cancellationToken);
                await writer.CompleteAsync();
            }
            catch (Exception e)
            {
                await writer.CompleteAsync(e);
                throw;
            }
        }
    }
}

/// <summary>
/// A PipelineSource that yields the contents of a PipeReader.
/// </summary>
public class SimplePipelineSource : IPipelineSource
{
    public PipeReader Reader { get; init; }

    public SimplePipelineSource(PipeReader reader)
    {
        Reader = reader;
    }

    public SimplePipelineSource(Stream stream)
        : this(PipeReader.Create(stream))
    {
    }

    public SimplePipelineSource(byte[] array)
        : this(PipeReader.Create(new ReadOnlySequence<byte>(array)))
    {
    }

    public async Task Process(PipeWriter writer, CancellationToken cancellationToken)
    {
        try
        {
            await Reader.CopyToAsync(writer, cancellationToken);
            await Reader.CompleteAsync();
        }
        catch (Exception e)
        {
            await Reader.CompleteAsync(e);
            throw;
        }
    }

    public PipeReader GetReader(CancellationToken cancellationToken) => Reader;
}
