using System.CommandLine;
using System.CommandLine.Parsing;
using System.Runtime.CompilerServices;
using System.Xml;
using System.Xml.Xsl;

[assembly: InternalsVisibleTo("Transform-Xml.Tests")]

var inputOption = new Option<FileInfo?>(
    new[] { "--input", "-i" },
    "The input XML file path (default: standard in)")
{
    IsRequired = false
};
inputOption.AddValidator(ValidateFileExistsIfSpecified);

var transformOption = new Option<FileInfo>(
    new[] { "--transform", "-t" },
    "The XSL transform file")
{
    IsRequired = true
};
transformOption.AddValidator(ValidateFileExistsIfSpecified);

var outputOption = new Option<FileInfo?>(
    new[] { "--output", "-o" },
    "The output XML file (default: standard out)")
{
    IsRequired = false
};

outputOption.AddValidator(res =>
{
    if (res.GetValueOrDefault<FileInfo>() is FileInfo fi)
    {
        // Create the containing directory if it doesn't exist.
        Directory.CreateDirectory(fi.Directory!.FullName);
    }
});

var transformCommand = new Command(
    name: "transform-xml",
    description: "Applies an XSL transform to an XML file.")
{
    inputOption,
    transformOption,
    outputOption
};

transformCommand.SetHandler((input, transform, output) =>
{
    using var inputReader = input is null ? Console.In : File.OpenText(input.FullName);
    using var inputXmlReader = XmlReader.Create(inputReader);

    var xslt = new XslCompiledTransform();
    xslt.Load(transform.FullName);
    var outputSettings = new XmlWriterSettings { Indent = true };

    using var outputWriter = output is null ? Console.Out : new StreamWriter(File.OpenWrite(output.FullName));
    using (var outputXmlWriter = XmlWriter.Create(outputWriter, outputSettings))
    {
        xslt.Transform(inputXmlReader, outputXmlWriter);
    }

    outputWriter.Write(outputSettings.NewLineChars);

}, inputOption, transformOption, outputOption);

return transformCommand.Invoke(args);

static void ValidateFileExistsIfSpecified(OptionResult result)
{
    var fi = result.GetValueOrDefault<FileInfo>();
    if (fi?.Exists == false)
    {
        result.ErrorMessage = $"The file {fi} does not exist.";
    }
}
