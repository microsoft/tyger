package cmd

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"path"
	"syscall"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/google/uuid"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/mitchellh/mapstructure"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

func NewInstallCommand(parentCommand *cobra.Command) *cobra.Command {
	var configPath string
	var setOverrides map[string]string

	installCmd := &cobra.Command{
		Use:                   "install",
		Short:                 "Install cloud infrastructure, the Tyger API, and Entra ID app identities",
		Long:                  "Install cloud infrastructure and the Tyger API, and Entra ID app identities.",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if parentCommand.PersistentPreRun != nil {
				parentCommand.PersistentPreRun(cmd, args)
			}

			commonPrerun(configPath, setOverrides, cmd)

		},
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	installCmd.AddCommand(newInstallCloudCommand())
	installCmd.AddCommand(newInstallApiCommand())
	installCmd.AddCommand(newInstallIdentitiesCommand())

	installCmd.PersistentFlags().StringVarP(&configPath, "file", "f", "", "path to config file")
	installCmd.PersistentFlags().StringToStringVar(&setOverrides, "set", nil, "override config values (e.g. --set cloud.subscriptionID=1234 --set cloud.resourceGroup=foo)")

	return installCmd
}

func NewUninstallCommand(parentCommand *cobra.Command) *cobra.Command {
	var configPath string
	var setOverrides map[string]string

	installCmd := &cobra.Command{
		Use:                   "uninstall",
		Short:                 "Uninstall cloud infrastructure and the Tyger API",
		Long:                  "Uninstall cloud infrastructure and the Tyger API",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		PersistentPreRun: func(cmd *cobra.Command, args []string) {
			if parentCommand.PersistentPreRun != nil {
				parentCommand.PersistentPreRun(cmd, args)
			}

			commonPrerun(configPath, setOverrides, cmd)

		},
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	installCmd.AddCommand(newUninstallCloudCommand())
	installCmd.AddCommand(newUninstallApiCommand())

	installCmd.PersistentFlags().StringVarP(&configPath, "file", "f", "", "path to config file")
	installCmd.PersistentFlags().StringToStringVar(&setOverrides, "set", nil, "override config values (e.g. --set cloud.subscriptionID=1234 --set cloud.resourceGroup=foo)")

	return installCmd
}

func commonPrerun(configPath string, setOverrides map[string]string, cmd *cobra.Command) string {
	utilruntime.ErrorHandlers = []func(error){
		func(err error) {
			log.Debug().Err(err).Msg("Kubernetes client runtime error")
		},
	}

	koanfConfig := koanf.New(".")
	if configPath == "" {
		configPath = getDefaultConfigPath()
	}

	if err := koanfConfig.Load(file.Provider(configPath), yaml.Parser()); err != nil {
		if os.IsNotExist(err) {
			if configPath != "" {
				log.Fatal().Err(err).Msgf("Config file not found at %s", configPath)
			} else {
				log.Fatal().Err(err).Msgf("Config file not found at %s", getDefaultConfigPath())
			}
		} else {
			log.Fatal().Err(err).Msg("Error reading config file")
		}
	}

	for k, v := range setOverrides {
		koanfConfig.Set(k, v)
	}

	config := install.EnvironmentConfig{}
	err := koanfConfig.UnmarshalWithConf("", &config, koanf.UnmarshalConf{
		Tag: "json",
		DecoderConfig: &mapstructure.DecoderConfig{
			WeaklyTypedInput: true,
			ErrorUnused:      true,
			Result:           &config,
		},
	})

	if err != nil {
		log.Fatal().Err(err).Msg("Failed to parse config file")
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
	return configPath
}

func getDefaultConfigPath() string {
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get user config dir")
	}
	defaultPath := path.Join(userConfigDir, "tyger", "config.yml")
	return defaultPath
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

func newUninstallCloudCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "cloud",
		Short:                 "Uninstall cloud infrastructure",
		Long:                  "uninstall cloud infrastructure",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			log.Info().Msg("Starting cloud uninstall")
			ctx, err := loginAndValidateSubscription(cmd.Context())
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			if err := install.UninstallCloud(ctx); err != nil {
				if err != install.ErrAlreadyLoggedError {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}
			log.Info().Msg("Uninstall complete")
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

func newUninstallApiCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "api",
		Short:                 "Install the Tyger API",
		Long:                  "Install the Tyger API",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			log.Info().Msg("Starting Tyger API uninstall")

			ctx, err := loginAndValidateSubscription(cmd.Context())
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

	return cmd
}

func newInstallIdentitiesCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "identities",
		Short:                 "Install Entra ID identities",
		Long:                  "Install Entra ID identities",
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			config := install.GetConfigFromContext(cmd.Context())
			cred, err := azidentity.NewAzureCLICredential(&azidentity.AzureCLICredentialOptions{TenantID: config.Api.Auth.TenantID})
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			for {
				ctx = install.SetAzureCredentialOnContext(ctx, cred)
				if _, err := install.GetGraphToken(ctx); err != nil {
					fmt.Printf("Run 'az login --tenant %s --allow-no-subscriptions' from another terminal window.\nPress any key when ready...\n\n", config.Api.Auth.TenantID)
					getSingleKey()
					continue
				}
				break
			}

			log.Info().Msg("Starting identities install")

			if err := install.InstallIdentities(ctx); err != nil {
				if err != install.ErrAlreadyLoggedError {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}

			log.Info().Msg("Install complete")

			return nil
		},
	}

	return cmd
}

func loginAndValidateSubscription(ctx context.Context) (context.Context, error) {
	cred, err := azidentity.NewDefaultAzureCredential(
		&azidentity.DefaultAzureCredentialOptions{
			AdditionallyAllowedTenants: []string{"*"},
		})
	if err != nil {
		log.Debug().Err(err).Msg("Failed to get credentials")
		return ctx, errors.New("failed to get credentials; make sure the Azure CLI is installed and and you have run `az login`")
	}

	ctx = install.SetAzureCredentialOnContext(ctx, cred)
	config := install.GetConfigFromContext(ctx)

	// Get the subscription ID if we are given the name.
	if _, err := uuid.Parse(config.Cloud.SubscriptionID); err != nil {
		config.Cloud.SubscriptionID, err = install.GetSubscriptionId(ctx, config.Cloud.SubscriptionID, cred)
		if err != nil {
			return ctx, fmt.Errorf("failed to get subscription ID: %w", err)
		}
	}

	return ctx, nil
}
