// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"syscall"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
	"github.com/goccy/go-yaml/parser"
	"github.com/google/uuid"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
	"github.com/microsoft/tyger/cli/internal/install/dockerinstall"
	"github.com/rs/zerolog/log"
	"golang.org/x/term"
	utilruntime "k8s.io/apimachinery/pkg/util/runtime"
)

type commonFlags struct {
	configPath                       string
	singleOrg                        *string
	multiOrg                         *[]string
	skipLoginAndValidateSubscription bool
	configPathOptional               bool
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

	yamlBytes, err := os.ReadFile(flags.configPath)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to read config file")
	}

	config, err := ParseConfig(yamlBytes)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to parse config file")
	}

	installer, err := newInstallerFromConfig(config)
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

	if err := installer.GetConfig().QuickValidateConfig(ctx); err != nil {
		if !errors.Is(err, install.ErrAlreadyLoggedError) {
			log.Fatal().Err(err).Send()
		}
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

func newInstallerFromConfig(config install.ValidatableConfig) (install.Installer, error) {
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

func ParseConfigFileCommon(yamlBytes []byte) (*install.ConfigFileCommon, error) {
	installCommon := &install.ConfigFileCommon{}
	if err := yaml.UnmarshalWithOptions(yamlBytes, installCommon); err != nil {
		return nil, fmt.Errorf("failed to decode config file: %w", err)
	}

	if installCommon.Kind == "" {
		return nil, fmt.Errorf("the `kind` field is required in the config file")
	}

	return installCommon, nil
}

func ParseConfig(yamlBytes []byte) (install.ValidatableConfig, error) {
	installCommon, err := ParseConfigFileCommon(yamlBytes)
	if err != nil {
		return nil, err
	}

	if installCommon.Kind == "" {
		return nil, fmt.Errorf("the `kind` field is required in the config file")
	}

	var config install.ValidatableConfig
	var decodeErr error
	switch installCommon.Kind {
	case cloudinstall.ConfigKindCloud:
		cfg := cloudinstall.CloudEnvironmentConfig{}
		config = &cfg
		if decodeErr = yaml.UnmarshalWithOptions(yamlBytes, &cfg, yaml.Strict()); decodeErr == nil {
			return config, nil
		}

	case dockerinstall.ConfigKindDocker:
		cfg := dockerinstall.DockerEnvironmentConfig{}
		config = &cfg
		if decodeErr = yaml.UnmarshalWithOptions(yamlBytes, &cfg, yaml.Strict()); decodeErr == nil {
			return config, nil
		}
	case "":
		return nil, fmt.Errorf("the `kind` field is required in the config file")
	default:
		return nil, fmt.Errorf("the `kind` field must be either `%s` or `%s`. Given value: `%s`", cloudinstall.ConfigKindCloud, dockerinstall.ConfigKindDocker, installCommon.Kind)
	}

	// There was an error decoding the config. See if this is because of an old config format
	if conversionErr := checkConfigFileNeedsConversion(*installCommon, decodeErr, yamlBytes); conversionErr != decodeErr {
		return nil, conversionErr
	}

	log.Error().Msg("failed to decode config file")

	fmt.Fprintln(os.Stderr, yaml.FormatError(decodeErr, term.IsTerminal(int(os.Stderr.Fd())), true))

	return nil, install.ErrAlreadyLoggedError
}

func checkConfigFileNeedsConversion(installCommon install.ConfigFileCommon, decodeErr error, yamlBytes []byte) error {
	if installCommon.Kind != cloudinstall.ConfigKindCloud {
		return decodeErr
	}

	configAst, err := ParseConfigToAst(yamlBytes)
	if err != nil {
		return decodeErr
	}

	isPreOrganizationsConfig := func() bool {
		if p, err := yaml.PathString("$.api"); err != nil {
			panic(fmt.Errorf("failed to create YAML path: %w", err))
		} else if n, _ := p.FilterNode(configAst); n != nil && n.Type() == ast.MappingType {
			return true
		}

		if p, err := yaml.PathString("$.cloud.storage"); err != nil {
			panic(fmt.Errorf("failed to create YAML path: %w", err))
		} else if n, _ := p.FilterNode(configAst); n != nil && n.Type() == ast.MappingType {
			return true
		}

		if p, err := yaml.PathString("$.organizations"); err != nil {
			panic(fmt.Errorf("failed to create YAML path: %w", err))
		} else if n, _ := p.FilterNode(configAst); n == nil {
			return true
		}

		return false
	}

	isPreAccessControlConfig := func() bool {
		var unknownFieldErr *yaml.UnknownFieldError
		if !errors.As(decodeErr, &unknownFieldErr) {
			return false
		}

		if unknownFieldErr.Token.Value != "auth" {
			return false
		}

		visitor := &tokenFinderVisitor{target: unknownFieldErr.Token}
		ast.Walk(visitor, configAst)
		if visitor.foundNode != nil {
			pathRegex := regexp.MustCompile(`\$\.organizations\[\d+\]\.api\.auth`)
			return pathRegex.MatchString(visitor.foundNode.GetPath())
		}

		return true
	}

	if isPreOrganizationsConfig() {
		return fmt.Errorf("the config file appears to be in an old format. Please convert it to the new format using the command `tyger config convert`")
	}

	if isPreAccessControlConfig() {
		return fmt.Errorf("the access control config file appears to be in an old for old format. The `auth` field has been renamed to `accessControl`. You can use `tyger config convert` to convert it to the new format.`")
	}

	return decodeErr
}

func ParseConfigToMap(yamlBytes []byte) (map[string]any, error) {
	config := make(map[string]any)

	if err := yaml.Unmarshal(yamlBytes, &config); err != nil {
		return nil, fmt.Errorf("failed to decode config file: %w", err)
	}

	return config, nil
}

func ParseConfigToAst(yamlBytes []byte) (ast.Node, error) {
	file, err := parser.ParseBytes(yamlBytes, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("failed to parse config file: %w", err)
	}

	if len(file.Docs) > 1 {
		return nil, fmt.Errorf("the config file contains multiple documents, which is not supported")
	}

	return file.Docs[0].Body, nil
}

func parseStandaloneAccessControlConfig(yamlBytes []byte) (*cloudinstall.StandaloneAccessControlConfig, error) {
	ac := cloudinstall.StandaloneAccessControlConfig{}
	if err := yaml.UnmarshalWithOptions(yamlBytes, &ac, yaml.Strict()); err != nil {
		return nil, fmt.Errorf("failed to decode access control config file: %w", err)
	}

	return &ac, nil
}

func parseStandaloneAccessControlConfigToAst(yamlBytes []byte) (ast.Node, error) {
	file, err := parser.ParseBytes(yamlBytes, parser.ParseComments)
	if err != nil {
		return nil, fmt.Errorf("failed to parse access control config file: %w", err)
	}

	if len(file.Docs) > 1 {
		return nil, fmt.Errorf("the access control config file contains multiple documents, which is not supported")
	}

	return file.Docs[0].Body, nil
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
