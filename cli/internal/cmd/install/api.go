// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"

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
	cmd.AddCommand(NewMigrationsCommand(cmd))

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

func NewMigrationsCommand(parentCommand *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "migration",
		Aliases:               []string{"migrations"},
		Short:                 "Manage the tyger API database",
		Long:                  "Manage the tyger API database",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(NewMigrationsListCommand())
	cmd.AddCommand(NewMigrationApplyCommand())
	cmd.AddCommand(NewMigrationLogsCommand())

	return cmd
}

func NewMigrationApplyCommand() *cobra.Command {
	flags := commonFlags{}
	targetVersion := 0
	latest := false
	wait := false
	cmd := &cobra.Command{
		Use:                   "apply",
		Short:                 "Apply tyger database migrations",
		Long:                  "Apply tyger database migrations",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if !latest && targetVersion == 0 {
				log.Fatal().Msg("Either --latest or --target-version must be specified")
			}

			if latest && targetVersion != 0 {
				log.Fatal().Msg("Only one of --latest or --target-version can be specified")
			}

			ctx := commonPrerun(cmd.Context(), &flags)

			ctx, err := loginAndValidateSubscription(ctx)
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			if err := install.ApplyMigrations(ctx, targetVersion, latest, wait); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	cmd.Flags().IntVar(&targetVersion, "target-version", targetVersion, "The target version to migrate to")
	cmd.Flags().BoolVar(&latest, "latest", latest, "Migrate to the latest version")
	cmd.Flags().BoolVar(&wait, "wait", wait, "Wait for the migration to complete")

	addCommonFlags(cmd, &flags)
	return cmd
}

func NewMigrationLogsCommand() *cobra.Command {
	flags := commonFlags{}
	targetVersion := 0
	latest := false
	wait := false
	cmd := &cobra.Command{
		Use:                   "logs ID",
		Short:                 "Get the logs of a database migration",
		Long:                  "Get the logs of a database migration",
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx := commonPrerun(cmd.Context(), &flags)

			ctx, err := loginAndValidateSubscription(ctx)
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			id, err := strconv.Atoi(args[0])
			if err != nil {
				log.Fatal().Msg("The ID argument must be an integer")
			}

			if err := install.GetMigrationLogs(ctx, id, os.Stdout); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	cmd.Flags().IntVar(&targetVersion, "target-version", targetVersion, "The target version to migrate to")
	cmd.Flags().BoolVar(&latest, "latest", latest, "Migrate to the latest version")
	cmd.Flags().BoolVar(&wait, "wait", wait, "Wait for the migration to complete")

	addCommonFlags(cmd, &flags)
	return cmd
}

func NewMigrationsListCommand() *cobra.Command {
	flags := commonFlags{}
	all := false
	cmd := &cobra.Command{
		Use:                   "list",
		Short:                 "List the tyger API database migrations",
		Long:                  "List the tyger API database migrations",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx := commonPrerun(cmd.Context(), &flags)

			ctx, err := loginAndValidateSubscription(ctx)
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			versions, err := install.ListDatabaseVersions(ctx, all)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to exec into pod")
			}
			encoder := json.NewEncoder(os.Stdout)
			encoder.SetIndent("", "  ")

			if err := encoder.Encode(versions); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	addCommonFlags(cmd, &flags)
	cmd.Flags().BoolVar(&all, "all", all, "Show all versions, including those that have been applied")
	return cmd
}
