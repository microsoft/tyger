package install

import (
	"errors"
	"os"

	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func NewApiCommand(parentCommand *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "api",
		Short:                 "Manage the tyger API",
		Long:                  "Manage the tyger API",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newApiInstallCommand(cmd))
	cmd.AddCommand(newApiUninstallCommand(cmd))

	return cmd
}

func newApiInstallCommand(parentCommand *cobra.Command) *cobra.Command {
	flags := commonFlags{}
	cmd := cobra.Command{
		Use:                   "install",
		Short:                 "Install the Typer API",
		Long:                  "Install the Typer API",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx := commonPrerun(cmd.Context(), &flags)

			log.Info().Msg("Starting Tyger API install")

			ctx, err := loginAndValidateSubscription(ctx)
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			if err := install.InstallTyger(ctx); err != nil {
				if err != install.ErrAlreadyLoggedError {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}
			log.Info().Msg("Install complete")
		},
	}

	addCommonFlags(&cmd, &flags)
	return &cmd
}

func newApiUninstallCommand(parentCommand *cobra.Command) *cobra.Command {
	flags := commonFlags{}
	cmd := cobra.Command{
		Use:                   "uninstall",
		Short:                 "Uninstall the Typer API",
		Long:                  "Uninstall the Typer API",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx := commonPrerun(cmd.Context(), &flags)

			log.Info().Msg("Starting Tyger API uninstall")

			ctx, err := loginAndValidateSubscription(ctx)
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			if err := install.UninstallTyger(ctx); err != nil {
				if err != install.ErrAlreadyLoggedError {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}
			log.Info().Msg("Uninstall complete")
		},
	}

	addCommonFlags(&cmd, &flags)
	return &cmd
}
