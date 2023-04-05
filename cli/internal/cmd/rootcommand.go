package cmd

import (
	"os"
	"time"

	"github.com/spf13/cobra"
	"github.com/thediveo/enumflag"
	"go.opentelemetry.io/otel/baggage"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
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
	baggageEntries := make(map[string]string)

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

			log.Logger = log.Logger.With().Str("command", cmd.CommandPath()).Logger()

			if len(baggageEntries) > 0 {
				b := baggage.Baggage{}
				for k, v := range baggageEntries {
					mem, err := baggage.NewMember(k, v)
					if err != nil {
						log.Fatal().Err(err).Msg("invalid baggage entry")
					}
					b, err = b.SetMember(mem)
					if err != nil {
						log.Fatal().Err(err).Msg("invalid baggage entry")
					}

				}

				ctx := baggage.ContextWithBaggage(cmd.Context(), b)
				cmd.SetContext(ctx)
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

	cmd.PersistentFlags().StringToStringVar(&baggageEntries, "baggage", nil, "adds key=value as an HTTP `baggage` header on all requests. Can be specified multiple times.")

	cobra.EnableCommandSorting = false

	return cmd
}

func isStdErrTerminal() bool {
	o, _ := os.Stderr.Stat()
	return (o.Mode() & os.ModeCharDevice) == os.ModeCharDevice
}
