package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/google/uuid"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/mitchellh/mapstructure"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"github.com/spf13/viper"
)

func NewInstallCommand() *cobra.Command {
	var installCmd *cobra.Command
	installCmd = &cobra.Command{
		Use:                   "install",
		Aliases:               []string{"install"},
		Short:                 "Install cloud infrastructure and the Tyger API",
		Long:                  "Install cloud infrastructure and the Tyger API",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if parent := installCmd.Parent(); parent != nil && parent.PersistentPreRun != nil {
				parent.PersistentPreRun(parent, args)
			}

			v := viper.New()
			v.SetConfigName("config")
			v.AddConfigPath("/workspaces/tyger")

			err := v.ReadInConfig() // Find and read the config file
			if err != nil {         // Handle errors reading the config file
				log.Fatal().Err(err).Msg("Fatal error reading config file")
			}

			var config install.EnvironmentConfig
			err = v.Unmarshal(&config, func(dc *mapstructure.DecoderConfig) {
				dc.WeaklyTypedInput = true
				dc.ErrorUnused = true
				dc.TagName = "json"
			})

			if err != nil {
				panic(fmt.Errorf("fatal error config file: %w", err))
			}

			ctx := cmd.Context()
			ctx = install.SetConfigOnContext(ctx, &config)

			ctx, _ = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)
			cmd.SetContext(ctx)

			go func() {
				<-ctx.Done()
				log.Warn().Msg("Cancelling...")
			}()

			if !install.QuickValidateEnvironmentConfig(&config) {
				os.Exit(1)
			}

		},
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	installCmd.AddCommand(newInstallCloudCommand())
	installCmd.AddCommand(newInstallApiCommand())

	return installCmd
}

func newInstallCloudCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "cloud",
		Short:                 "Install cloud infrastructure",
		Long:                  "Install cloud infrastructure",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			log.Info().Msg("Starting cloud install")
			ctx, err := loginAndValidateSubscription(cmd.Context())
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			if err := install.InstallCloud(ctx); err != nil {
				if err != install.ErrAlreadyLoggedError {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}
			log.Info().Msg("Install complete")
		},
	}

	return cmd
}

func newInstallApiCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "api",
		Short:                 "Install the Tyger API",
		Long:                  "Install the Tyger API",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			log.Info().Msg("Starting Tyger API install")

			ctx, err := loginAndValidateSubscription(cmd.Context())
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

	return cmd
}

func loginAndValidateSubscription(ctx context.Context) (context.Context, error) {
	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		log.Debug().Err(err).Msg("Failed to get credentials")
		return ctx, errors.New("failed to get credentials; make sure the Azure CLI is installed and and you have run `az login`")
	}

	config := install.GetConfigFromContext(ctx)

	ctx = install.SetAzureCredentialOnContext(ctx, cred)

	// Get the subscription ID if we are given the name.
	if _, err := uuid.Parse(config.Cloud.SubscriptionID); err != nil {
		config.Cloud.SubscriptionID, err = install.GetSubscriptionId(ctx, config.Cloud.SubscriptionID, cred)
		if err != nil {
			return ctx, fmt.Errorf("failed to get subscription ID: %w", err)
		}
	}

	return ctx, nil
}
