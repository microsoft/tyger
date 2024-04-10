// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"
	"fmt"
	"os"
	"os/signal"
	"path"
	"syscall"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/go-viper/mapstructure/v2"
	"github.com/google/uuid"
	"github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
	"github.com/microsoft/tyger/cli/internal/install/dockerinstall"
	"github.com/rs/zerolog/log"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

type commonFlags struct {
	configPath                       string
	setOverrides                     map[string]string
	skipLoginAndValidateSubscription bool
}

func commonPrerun(ctx context.Context, flags *commonFlags) (context.Context, install.Installer) {
	utilruntime.ErrorHandlers = []func(error){
		func(err error) {
			log.Debug().Err(err).Msg("Kubernetes client runtime error")
		},
	}

	koanfConfig := koanf.New(".")
	if flags.configPath == "" {
		flags.configPath = getDefaultConfigPath()
	}

	if err := koanfConfig.Load(file.Provider(flags.configPath), yaml.Parser()); err != nil {
		if os.IsNotExist(err) {
			if flags.configPath != "" {
				log.Fatal().Err(err).Msgf("Config file not found at %s", flags.configPath)
			} else {
				log.Fatal().Err(err).Msgf("Config file not found at %s", getDefaultConfigPath())
			}
		} else {
			log.Fatal().Err(err).Msg("Error reading config file")
		}
	}

	for k, v := range flags.setOverrides {
		koanfConfig.Set(k, v)
	}

	var config any
	var installer install.Installer

	environmentKind := koanfConfig.Get("kind")

	switch environmentKind {
	case nil, cloudinstall.EnvironmentKindCloud:
		c := &cloudinstall.CloudEnvironmentConfig{}
		installer = &cloudinstall.Installer{
			Config: c,
		}
		config = c
	case dockerinstall.EnvironmentKindDocker:
		config = &dockerinstall.DockerEnvironmentConfig{}
	default:
		log.Fatal().Msgf("The `kind` field must be one of `%s` or `%s`. Given value: `%s`", cloudinstall.EnvironmentKindCloud, dockerinstall.EnvironmentKindDocker, environmentKind)
	}

	err := koanfConfig.UnmarshalWithConf("", &config, koanf.UnmarshalConf{
		Tag: "json",
		DecoderConfig: &mapstructure.DecoderConfig{
			WeaklyTypedInput: true,
			ErrorUnused:      true,
			Squash:           true,
			Result:           &config,
		},
	})

	if err != nil {
		log.Fatal().Err(err).Msg("Failed to parse config file")
	}

	ctx = install.SetEnvironmentConfigOnContext(ctx, config)

	var stopFunc context.CancelFunc
	ctx, stopFunc = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-ctx.Done()
		stopFunc()
		log.Warn().Msg("Canceling...")
	}()

	switch t := config.(type) {
	case *cloudinstall.CloudEnvironmentConfig:
		if !cloudinstall.QuickValidateCloudEnvironmentConfig(t) {
			os.Exit(1)
		}

		if !flags.skipLoginAndValidateSubscription {
			ctx, err = loginAndValidateSubscription(ctx)
			if err != nil {
				log.Fatal().Err(err).Send()
			}
		}
	case *dockerinstall.DockerEnvironmentConfig:
		if !dockerinstall.QuickValidateDockerEnvironmentConfig(t) {
			os.Exit(1)
		}
	}

	return ctx, installer
}

func getDefaultConfigPath() string {
	userConfigDir, err := os.UserConfigDir()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get user config dir")
	}
	defaultPath := path.Join(userConfigDir, "tyger", "config.yml")
	return defaultPath
}

func loginAndValidateSubscription(ctx context.Context) (context.Context, error) {
	config := cloudinstall.GetCloudEnvironmentConfigFromContext(ctx)
	cred, err := cloudinstall.NewMiAwareAzureCLICredential(
		&azidentity.AzureCLICredentialOptions{
			TenantID: config.Cloud.TenantID,
		})

	if err == nil {
		_, err = cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{cloud.AzurePublic.Services[cloud.ResourceManager].Audience}})
	}

	if err != nil {
		return ctx, fmt.Errorf("please log in with the Azure CLI with the command `az login --tenant %s`", config.Cloud.TenantID)
	}

	ctx = cloudinstall.SetAzureCredentialOnContext(ctx, cred)

	// Get the subscription ID if we are given the name.
	if _, err := uuid.Parse(config.Cloud.SubscriptionID); err != nil {
		config.Cloud.SubscriptionID, err = cloudinstall.GetSubscriptionId(ctx, config.Cloud.SubscriptionID, cred)
		if err != nil {
			return ctx, fmt.Errorf("failed to get subscription ID: %w", err)
		}
	}

	return ctx, nil
}
