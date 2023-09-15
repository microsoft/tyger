package main

import (
	"os"

	"github.com/microsoft/tyger/cli/internal/cmd"
	"github.com/microsoft/tyger/cli/internal/cmd/env"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"go.opentelemetry.io/otel/baggage"
)

var (
	// set during build
	commit = ""
)

func newRootCommand() *cobra.Command {
	rootCommand := cmd.NewCommonRootCommand(commit)
	rootCommand.Use = "tyger"
	rootCommand.Short = "A command-line interface to the Tyger control plane."
	rootCommand.Long = `A command-line interface to the Tyger control plane.`

	baggageEntries := make(map[string]string)
	basePreRun := rootCommand.PersistentPreRun

	rootCommand.PersistentPreRun = func(cmd *cobra.Command, args []string) {
		basePreRun(cmd, args)

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
	}

	rootCommand.PersistentFlags().StringToStringVar(&baggageEntries, "baggage", nil, "adds key=value as an HTTP `baggage` header on all requests. Can be specified multiple times.")

	rootCommand.AddCommand(cmd.NewLoginCommand())
	rootCommand.AddCommand(cmd.NewLogoutCommand())
	rootCommand.AddCommand(cmd.NewBufferCommand())
	rootCommand.AddCommand(cmd.NewCodespecCommand())
	rootCommand.AddCommand(cmd.NewRunCommand())
	rootCommand.AddCommand(cmd.NewClusterCommand())
	rootCommand.AddCommand(env.NewEnvCommand())

	return rootCommand
}

func main() {
	err := newRootCommand().Execute()
	if err != nil {
		os.Exit(1)
	}
}
