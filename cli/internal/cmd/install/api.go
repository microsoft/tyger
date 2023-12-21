package install

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/microsoft/tyger/cli/internal/httpclient"
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
		Use:                   "migrations",
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

	return cmd
}

func NewMigrationApplyCommand() *cobra.Command {
	flags := commonFlags{}
	targetVersion := 0
	cmd := &cobra.Command{
		Use:                   "apply",
		Short:                 "Apply tyger database migrations",
		Long:                  "Apply tyger database migrations",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx := commonPrerun(cmd.Context(), &flags)

			ctx, err := loginAndValidateSubscription(ctx)
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			err = install.InstallMigrationRunner(ctx, targetVersion)
			if err != nil {
				log.Fatal().Err(err).Msg("Database migration failed")
			}
		},
	}

	cmd.Flags().IntVar(&targetVersion, "target-version", targetVersion, "The target version to migrate to")
	cmd.MarkFlagRequired("target-version")

	addCommonFlags(cmd, &flags)
	return cmd
}

func NewMigrationsListCommand() *cobra.Command {
	flags := commonFlags{}
	cmd := &cobra.Command{
		Use:                   "list",
		Short:                 "List the tyger API database migrations",
		Long:                  "List the tyger API database migrations",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx := commonPrerun(cmd.Context(), &flags)
			config := install.GetConfigFromContext(ctx)
			url := fmt.Sprintf("https://%s/v1/database-versions", config.Api.DomainName)

			req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodGet, url, nil)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to create request")
			}

			resp, err := httpclient.DefaultRetryableClient.Do(req)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to query database versions")
			}
			if resp.StatusCode != http.StatusOK {
				log.Fatal().Str("Status", resp.Status).Msg("Failed to query database versions")
			}

			page := model.Page[model.DatabaseVersion]{}
			if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
				log.Fatal().Err(err).Msg("Failed to decode response")
			}

			formattedItems, err := json.MarshalIndent(page.Items, "  ", "  ")
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to format response")
			}

			fmt.Println(string(formattedItems))
		},
	}

	addCommonFlags(cmd, &flags)
	return cmd
}
