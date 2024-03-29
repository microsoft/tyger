// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cmd

import (
	"fmt"
	"io"
	"os"
	"strings"
	"time"

	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/httpclient"
	"github.com/microsoft/tyger/cli/internal/logging"
	"github.com/microsoft/tyger/cli/internal/settings"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/thediveo/enumflag"
)

func NewCommonRootCommand(commit string) *cobra.Command {
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
		Version:      commit,
		SilenceUsage: true,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if logFormat == Unspecified {
				if isStdErrTerminal() {
					logFormat = Pretty
				} else {
					logFormat = Plain
				}
			}

			zerolog.TimeFieldFormat = time.RFC3339Nano
			log.Logger = log.Logger.Level(logLevel)
			var logSink io.Writer
			switch logFormat {
			case Pretty, Plain:
				logSink = zerolog.ConsoleWriter{
					Out:        os.Stderr,
					TimeFormat: "2006-01-02T15:04:05.000Z07:00", // like RFC3339Nano, but always showing three digits for the fractional seconds
					NoColor:    logFormat == Plain,
				}
				log.Logger = log.Output(logSink)
			default:
				logSink = os.Stderr
			}

			zerolog.DefaultContextLogger = &log.Logger
			ctx := logging.SetLogSinkOnContext(cmd.Context(), logSink)
			ctx = log.Logger.WithContext(ctx)

			ctx = settings.SetServiceInfoFuncOnContext(ctx, controlplane.GetPersistedServiceInfo)

			cmd.SetContext(ctx)

			cmdPath := cmd.CommandPath()
			switch {
			case strings.HasPrefix(cmdPath, "tyger api"),
				strings.HasPrefix(cmdPath, "tyger config"),
				strings.HasPrefix(cmdPath, "tyger cloud"):
				break
			default:
				// Disable the default transport so that we don't forget
				// to apply proxy settings, unless performing installation operations.
				httpclient.DisableDefaultTransport()
			}

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

	cmd.PersistentFlags().Var(
		enumflag.New(&logLevel, "level", levelIds, enumflag.EnumCaseInsensitive),
		"log-level",
		"specifies logging level. Can be one of: 'trace', 'debug', 'info', 'warn', or 'error'.")

	cmd.PersistentFlags().Var(
		enumflag.New(&logFormat, "format", logFormatIds, enumflag.EnumCaseInsensitive),
		"log-format",
		"specifies logging format. Can be one of: 'pretty', 'plain', or 'json'. The default is 'pretty' unless stderr is redirected, in which case it will be 'plain'. 'json' is the most efficient.")

	cobra.EnableCommandSorting = false

	return cmd
}

func isStdErrTerminal() bool {
	o, _ := os.Stderr.Stat()
	return (o.Mode() & os.ModeCharDevice) == os.ModeCharDevice
}

func hasFlagChanged(cmd *cobra.Command, flagName string) bool {
	flag := cmd.Flags().Lookup(flagName)
	if flag == nil {
		panic(fmt.Sprintf("flag %s not found", flagName))
	}

	return flag.Changed
}
