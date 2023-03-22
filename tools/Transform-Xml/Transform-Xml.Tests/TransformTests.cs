using System.Diagnostics;
using System.Runtime.CompilerServices;
using Shouldly;
using Xunit;

namespace Transform_Xml.Tests;
public class TransformTests
{
    [Fact]
    public async Task OutputIsStdout()
    {
        var (exitCode, stdOut, stdErr) = await Run(stdIn: null, "-i", TestFilePath("1_input.xml"), "-t", TestFilePath("1_transform.xsl"));
        stdErr.ShouldBeEmpty();
        exitCode.ShouldBe(0);
        stdOut.ShouldBe(File.ReadAllText(TestFilePath("1_output.xml")));
    }

    [Fact]
    public async Task InputIsStdIn()
    {
        var (exitCode, stdOut, stdErr) = await Run(stdIn: File.OpenText(TestFilePath("1_input.xml")), "-t", TestFilePath("1_transform.xsl"));
        stdErr.ShouldBeEmpty();
        exitCode.ShouldBe(0);
        stdOut.ShouldBe(File.ReadAllText(TestFilePath("1_output.xml")));
    }

    [Fact]
    public async Task OutputIsFile()
    {
        var tempFilePath = Path.GetTempFileName();
        try
        {
            var (exitCode, stdOut, stdErr) = await Run(stdIn: null, "-i", TestFilePath("1_input.xml"), "-t", TestFilePath("1_transform.xsl"), "-o", tempFilePath);
            stdErr.ShouldBeEmpty();
            stdOut.ShouldBeEmpty();
            exitCode.ShouldBe(0);
            File.ReadAllText(tempFilePath).ShouldBe(File.ReadAllText(TestFilePath("1_output.xml")));
        }
        finally
        {
            if (File.Exists(tempFilePath))
            {
                File.Delete(tempFilePath);
            }
        }
    }

    private static async Task<(int exitCode, string stdOut, string stdErr)> Run(TextReader? stdIn = null, params string[] args)
    {
        ProcessStartInfo startInfo = new()
        {
            FileName = Path.ChangeExtension(typeof(Program).Assembly.Location, null),
            RedirectStandardOutput = true,

            RedirectStandardError = true,
            RedirectStandardInput = true,
        };

        foreach (var arg in args)
        {
            startInfo.ArgumentList.Add(arg);
        }

        var process = Process.Start(startInfo)!;

        if (stdIn != null)
        {
            using (process.StandardInput)
            {
                await process.StandardInput.WriteLineAsync(await stdIn.ReadToEndAsync());
            }
        }

        var outTask = process.StandardOutput.ReadToEndAsync();
        var errTask = process.StandardError.ReadToEndAsync();
        process.WaitForExit();

        return (process.ExitCode, await outTask, await errTask);
    }

    private static string TestFilePath(string fileName, [CallerFilePath] string currentFilePath = "")
    {
        return Path.Combine(Path.GetDirectoryName(currentFilePath)!, fileName);
    }
}
