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
	singleOrg                        *string
	multiOrg                         *[]string
	setOverrides                     map[string]string
	skipLoginAndValidateSubscription bool
}

func newSingleOrgCommonFlags() commonFlags {
	org := ""
	return commonFlags{
		singleOrg: &org,
	}
}

func newMultiOrgFlags() commonFlags {
	orgs := []string{}
	return commonFlags{
		multiOrg: &orgs,
	}
}

func commonPrerun(ctx context.Context, flags *commonFlags) (context.Context, install.Installer) {
	utilruntime.ErrorHandlers = []utilruntime.ErrorHandler{
		func(ctx context.Context, err error, msg string, keysAndValues ...interface{}) {
			log.Debug().Err(err).Msg("Kubernetes client runtime error")
		},
	}

	config, err := parseConfig(flags.configPath, flags.setOverrides, false)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to parse config file")
	}

	installer, err := getInstallerFromConfig(config)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get installer from config")
	}

	var stopFunc context.CancelFunc
	ctx, stopFunc = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

	go func() {
		<-ctx.Done()
		stopFunc()
		log.Ctx(ctx).Warn().Msg("Canceling...")
	}()

	if !installer.QuickValidateConfig() {
		os.Exit(1)
	}

	if flags.singleOrg != nil {
		if err := installer.ApplySingleOrgFilter(*flags.singleOrg); err != nil {
			log.Fatal().Err(err).Send()
		}
	} else if flags.multiOrg != nil {
		if err := installer.ApplyMultiOrgFilter(*flags.multiOrg); err != nil {
			log.Fatal().Err(err).Send()
		}
	} else {
		panic("either singleOrg or multiOrg must be set")
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

func getInstallerFromConfig(config any) (install.Installer, error) {
	switch c := config.(type) {
	case *cloudinstall.CloudEnvironmentConfig:
		return &cloudinstall.Installer{
			Config: c,
		}, nil
	case *dockerinstall.DockerEnvironmentConfig:
		return dockerinstall.NewInstaller(c)
	default:
		return nil, fmt.Errorf("unexpected config type: %T", config)
	}
}

func parseConfig(filePath string, overrides map[string]string, toMap bool) (any, error) {
	koanfConfig := koanf.New(".")

	if err := koanfConfig.Load(file.Provider(filePath), yaml.Parser()); err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("config file not found at %s: %w", filePath, err)
		}
		return nil, fmt.Errorf("error reading config file: %w", err)
	}

	for k, v := range overrides {
		koanfConfig.Set(k, v)
	}

	var config any
	if toMap {
		config = make(map[string]any)
	} else {
		environmentKind := koanfConfig.Get("kind")

		switch environmentKind {
		case nil, cloudinstall.EnvironmentKindCloud:
			config = &cloudinstall.CloudEnvironmentConfig{
				Kind: cloudinstall.EnvironmentKindCloud,
			}
		case dockerinstall.EnvironmentKindDocker:
			config = &dockerinstall.DockerEnvironmentConfig{
				Kind: dockerinstall.EnvironmentKindDocker}
		default:
			log.Fatal().Msgf("The `kind` field must be one of `%s` or `%s`. Given value: `%s`", cloudinstall.EnvironmentKindCloud, dockerinstall.EnvironmentKindDocker, environmentKind)
		}
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
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	return config, nil
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
