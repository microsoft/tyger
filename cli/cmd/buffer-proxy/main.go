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

	logLevel := zerolog.InfoLevel
	cmd := &cobra.Command{
		Use:     "buffer-proxy",
		Version: commit,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			zerolog.SetGlobalLevel(logLevel)
			zerolog.TimeFieldFormat = time.RFC3339Nano
			log.Logger = log.Output(zerolog.ConsoleWriter{
				Out:        os.Stderr,
				TimeFormat: "2006-01-02T15:04:05.000Z07:00", // like RFC3339Nano, but always showing three digits for the fractional seconds
			})

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

	cmd.PersistentFlags().VarP(
		enumflag.New(&logLevel, "mode", levelIds, enumflag.EnumCaseInsensitive),
		"log-level", "l",
		"specifies logging level. Can be one of: trace, debug, info, warn, error.")

	cobra.EnableCommandSorting = false

	cmd.AddCommand(newWriteCommand())
	cmd.AddCommand(newReadCommand())
	cmd.AddCommand(newGenerateCommand())

	return cmd
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
