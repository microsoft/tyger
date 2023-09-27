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

func NewInstallCommand() *cobra.Command {
	var configPath string
	var setOverrides map[string]string

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

			// The k8s client library can noisily log to stderr using klog with entries like https://github.com/helm/helm/issues/11772
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

		},
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	installCmd.AddCommand(newInstallCloudCommand())
	installCmd.AddCommand(newInstallApiCommand())

	installCmd.PersistentFlags().StringVarP(&configPath, "file", "f", "", "path to config file")
	installCmd.PersistentFlags().StringToStringVar(&setOverrides, "set", nil, "override config values (e.g. --set cloud.subscriptionID=1234 --set cloud.resourceGroup=foo)")

	return installCmd
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
