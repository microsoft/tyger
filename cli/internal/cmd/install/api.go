// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"encoding/json"
	"errors"
	"os"
	"strconv"

	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/microsoft/tyger/cli/internal/install/dockerinstall"
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

	cmd.AddCommand(newApiInstallCommand())
	cmd.AddCommand(newApiUninstallCommand())
	cmd.AddCommand(newApiLogsCommand())
	cmd.AddCommand(NewMigrationsCommand())
	cmd.AddCommand(NewGenerateSingingKeyCommand())

	return cmd
}

func newApiLogsCommand() *cobra.Command {
	commonFlags := newSingleOrgCommonFlags()
	options := install.ServerLogOptions{Destination: os.Stdout}

	cmd := cobra.Command{
		Use:                   "logs [--follow] [--tail LINES] [--data-plane] -f CONFIG.yml",
		Short:                 "Get Tyger API logs",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx, installer := commonPrerun(cmd.Context(), &commonFlags)
			if err := installer.GetServerLogs(ctx, options); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	addCommonFlags(&cmd, &commonFlags)

	cmd.Flags().BoolVar(&options.Follow, "follow", options.Follow, "Follow the logs")
	cmd.Flags().IntVar(&options.TailLines, "tail", options.TailLines, "Start from the last N lines")
	cmd.Flags().BoolVar(&options.DataPlane, "data-plane", options.DataPlane, "Get the logs of the data plane instead of the control plane. Only applies to Docker installations")

	return &cmd
}

func newApiInstallCommand() *cobra.Command {
	flags := newMultiOrgFlags()
	cmd := cobra.Command{
		Use:                   "install -f CONFIG.yml",
		Short:                 "Install the Typer API",
		Long:                  "Install the Typer API",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx, installer := commonPrerun(cmd.Context(), &flags)

			log.Ctx(ctx).Info().Msg("Starting Tyger API install")

			err := installer.InstallTyger(ctx)
			if err != nil {
				if !errors.Is(err, install.ErrAlreadyLoggedError) {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}

			log.Ctx(ctx).Info().Msg("Install complete")
		},
	}

	addCommonFlags(&cmd, &flags)
	return &cmd
}

func newApiUninstallCommand() *cobra.Command {
	flags := newMultiOrgFlags()
	deleteData := false
	preserveRunContainers := false
	cmd := cobra.Command{
		Use:                   "uninstall -f CONFIG.yml",
		Short:                 "Uninstall the Typer API",
		Long:                  "Uninstall the Typer API",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx, installer := commonPrerun(cmd.Context(), &flags)
			log.Ctx(ctx).Info().Msg("Starting Tyger API uninstall")

			if err := installer.UninstallTyger(ctx, deleteData, preserveRunContainers); err != nil {
				if !errors.Is(err, install.ErrAlreadyLoggedError) {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}

			log.Ctx(ctx).Info().Msg("Uninstall complete")
		},
	}

	addCommonFlags(&cmd, &flags)
	cmd.Flags().BoolVar(&deleteData, "delete-data", deleteData, "Permanently delete data (Docker only)")
	cmd.Flags().BoolVar(&preserveRunContainers, "preserve-run-containers", preserveRunContainers, "Preserve run containers (Docker only)") // for testing purposes only
	cmd.Flags().MarkHidden("preserve-run-containers")
	return &cmd
}

func NewMigrationsCommand() *cobra.Command {
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
	flags := newMultiOrgFlags()
	targetVersion := 0
	latest := false
	wait := false
	offline := false
	cmd := &cobra.Command{
		Use:                   "apply -f CONFIG.yml",
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

			ctx, installer := commonPrerun(cmd.Context(), &flags)
			if err := installer.ApplyMigrations(ctx, targetVersion, latest, offline, wait); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	cmd.Flags().IntVar(&targetVersion, "target-version", targetVersion, "The target version to migrate to")
	cmd.Flags().BoolVar(&latest, "latest", latest, "Migrate to the latest version")
	cmd.Flags().BoolVar(&wait, "wait", wait, "Wait for the migration to complete")
	cmd.Flags().BoolVar(&offline, "offline", offline, "Do not coordinate with replicas to ensure uninterrupted service")

	addCommonFlags(cmd, &flags)
	return cmd
}

func NewMigrationLogsCommand() *cobra.Command {
	flags := newSingleOrgCommonFlags()
	cmd := &cobra.Command{
		Use:                   "logs ID -f CONFIG.yml",
		Short:                 "Get the logs of a database migration",
		Long:                  "Get the logs of a database migration",
		DisableFlagsInUseLine: true,
		Args:                  cobra.ExactArgs(1),
		Run: func(cmd *cobra.Command, args []string) {
			ctx, installer := commonPrerun(cmd.Context(), &flags)

			id, err := strconv.Atoi(args[0])
			if err != nil {
				log.Fatal().Msg("The ID argument must be an integer")
			}

			if err := installer.GetMigrationLogs(ctx, id, os.Stdout); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	addCommonFlags(cmd, &flags)
	return cmd
}

func NewMigrationsListCommand() *cobra.Command {
	flags := newSingleOrgCommonFlags()
	all := false
	cmd := &cobra.Command{
		Use:                   "list -f CONFIG.yml",
		Short:                 "List the tyger API database migrations",
		Long:                  "List the tyger API database migrations",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx, installer := commonPrerun(cmd.Context(), &flags)

			versions, err := installer.ListDatabaseVersions(ctx, all)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed list database versions")
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

func NewGenerateSingingKeyCommand() *cobra.Command {
	publicFile := ""
	privateFile := ""
	cmd := &cobra.Command{
		Use:                   "generate-signing-key --public FILE.pem --private FILE.pem",
		Short:                 "Generate a new signing key pair",
		Long:                  "Generate a new signing key pair",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := dockerinstall.GenerateSigningKeyPair(publicFile, privateFile); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	cmd.Flags().StringVar(&publicFile, "public", publicFile, "The file to write the public key to")
	cmd.MarkFlagRequired("public")
	cmd.Flags().StringVar(&privateFile, "private", privateFile, "The file to write the private key to")
	cmd.MarkFlagRequired("private")
	return cmd
}
