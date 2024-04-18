// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"
	"fmt"
	"os"
	"os/signal"
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

	if err := koanfConfig.Load(file.Provider(flags.configPath), yaml.Parser()); err != nil {
		if os.IsNotExist(err) {
			log.Fatal().Err(err).Msgf("Config file not found at %s", flags.configPath)
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
		c := &cloudinstall.CloudEnvironmentConfig{
			Kind: cloudinstall.EnvironmentKindCloud,
		}
		installer = &cloudinstall.Installer{
			Config: c,
		}
		config = c
	case dockerinstall.EnvironmentKindDocker:
		i, err := dockerinstall.NewInstaller(&dockerinstall.DockerEnvironmentConfig{})
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to create docker installer")
		}
		installer = i
		config = i.Config
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

	var stopFunc context.CancelFunc
	ctx, stopFunc = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-ctx.Done()
		stopFunc()
		log.Warn().Msg("Canceling...")
	}()

	if !installer.QuickValidateConfig() {
		os.Exit(1)
	}

	if cloudInstaller, ok := installer.(*cloudinstall.Installer); ok {
		if !flags.skipLoginAndValidateSubscription {
			ctx, err = loginAndValidateSubscription(ctx, cloudInstaller)
			if err != nil {
				log.Fatal().Err(err).Send()
			}
		}
	}

	return ctx, installer
}

func loginAndValidateSubscription(ctx context.Context, cloudInstaller *cloudinstall.Installer) (context.Context, error) {
	cred, err := cloudinstall.NewMiAwareAzureCLICredential(
		&azidentity.AzureCLICredentialOptions{
			TenantID: cloudInstaller.Config.Cloud.TenantID,
		})

	if err == nil {
		_, err = cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{cloud.AzurePublic.Services[cloud.ResourceManager].Audience}})
	}

	if err != nil {
		return ctx, fmt.Errorf("please log in with the Azure CLI with the command `az login --tenant %s`", cloudInstaller.Config.Cloud.TenantID)
	}

	cloudInstaller.Credential = cred

	// Get the subscription ID if we are given the name.
	if _, err := uuid.Parse(cloudInstaller.Config.Cloud.SubscriptionID); err != nil {
		cloudInstaller.Config.Cloud.SubscriptionID, err = cloudinstall.GetSubscriptionId(ctx, cloudInstaller.Config.Cloud.SubscriptionID, cred)
		if err != nil {
			return ctx, fmt.Errorf("failed to get subscription ID: %w", err)
		}
	}

	return ctx, nil
}
