package main

import (
	"os"
	"runtime"
	"time"

	"github.com/mitchellh/go-ps"
	"github.com/spf13/cobra"
	"github.com/thediveo/enumflag"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var (
	// set during build
	commit = ""
)

func main() {
	err := newRootCommand(commit).Execute()
	if err != nil {
		os.Exit(1)
	}
}

func newRootCommand(commit string) *cobra.Command {
	if commit == "" {
		commit = "unknown"
	}

	type LogFormat int8
	const (
		Unspecified LogFormat = iota
		Pretty
		Plain
		Json
	)

	logFormat := Unspecified

	logLevel := zerolog.InfoLevel
	cmd := &cobra.Command{
		Use:     "buffer-proxy",
		Version: commit,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {

			if logFormat == Unspecified {
				if isStdErrTerminal() {
					logFormat = Pretty
				} else {
					logFormat = Plain
				}
			}

			zerolog.SetGlobalLevel(logLevel)
			zerolog.TimeFieldFormat = time.RFC3339Nano
			switch logFormat {
			case Pretty, Plain:
				log.Logger = log.Output(zerolog.ConsoleWriter{
					Out:        os.Stderr,
					TimeFormat: "2006-01-02T15:04:05.000Z07:00", // like RFC3339Nano, but always showing three digits for the fractional seconds
					NoColor:    logFormat == Plain,
				})
			}

			warnIfRunningInPowerShell()

			log.Logger = log.Logger.With().Str("command", cmd.Name()).Logger()
		},
	}

	// hide --help as a flag in the usage output
	cmd.PersistentFlags().BoolP("help", "h", false, "Print usage")
	cmd.PersistentFlags().Lookup("help").Hidden = true

	var levelIds = map[zerolog.Level][]string{
		zerolog.TraceLevel: {"trace"},
		zerolog.DebugLevel: {"debug"},
		zerolog.InfoLevel:  {"info"},
		zerolog.WarnLevel:  {"warn"},
		zerolog.ErrorLevel: {"error"},
	}

	var logFormatIds = map[LogFormat][]string{
		Unspecified: {""},
		Pretty:      {"pretty"},
		Plain:       {"plain"},
		Json:        {"json"},
	}

	cmd.PersistentFlags().VarP(
		enumflag.New(&logLevel, "level", levelIds, enumflag.EnumCaseInsensitive),
		"log-level", "l",
		"specifies logging level. Can be one of: 'trace', 'debug', 'info', 'warn', or 'error'.")

	// cmd.PersistentFlags().
	cmd.PersistentFlags().VarP(
		enumflag.New(&logFormat, "format", logFormatIds, enumflag.EnumCaseInsensitive),
		"log-format", "f",
		"specifies logging format. Can be one of: 'pretty', 'plain', or 'json'. The default is 'pretty' unless stderr is redirected, in which case it will be 'plain'. 'json' is the most efficient.")

	cobra.EnableCommandSorting = false

	cmd.AddCommand(newWriteCommand())
	cmd.AddCommand(newReadCommand())
	cmd.AddCommand(newGenerateCommand())

	return cmd
}

func isStdErrTerminal() bool {
	o, _ := os.Stderr.Stat()
	return (o.Mode() & os.ModeCharDevice) == os.ModeCharDevice
}

func warnIfRunningInPowerShell() {
	parentPid := os.Getppid()
	parentProcess, err := ps.FindProcess(parentPid)
	if err == nil && parentProcess != nil {
		switch parentProcess.Executable() {
		case "pwsh", "pwsh.exe", "powershell.exe":
			var suggestion string
			if runtime.GOOS == "windows" {
				suggestion = "Consider using cmd or bash."
			} else {
				suggestion = "Consider using a different shell."
			}

			log.Warn().Msgf("PowerShell I/O redirection may corrupt binary data. See https://github.com/PowerShell/PowerShell/issues/1908. %s", suggestion)
		}
	}
}

type BufferBlob struct {
	BlobNumber int
	Contents   []byte
}
