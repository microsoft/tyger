// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
	"path"
	"slices"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/goccy/go-yaml"
	"github.com/google/uuid"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func NewAccessControlCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "access-control",
		Short:                 "Manage access control",
		Long:                  "Manage access control",
		DisableFlagsInUseLine: true,
	}

	cmd.AddCommand(newAccessControlApplyCommand())
	cmd.AddCommand(newAccessControlPrettyPrintCommand())
	cmd.AddCommand(newAccessControlInitCommand())
	cmd.AddCommand(newAccessControlShowCommand())
	return cmd
}

func newAccessControlInitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "init",
		Short:                 "Initialize a standalone access control configuration file",
		Long:                  "Initialize a standalone access control configuration file",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			accessControlConfig := &cloudinstall.StandaloneAccessControlConfig{
				ConfigFileCommon: install.ConfigFileCommon{
					Kind: cloudinstall.ConfigKindAccessControl,
				},
				AccessControlConfig: &cloudinstall.AccessControlConfig{
					ApiAppUri: "api://tyger-server",
					CliAppUri: "api://tyger-cli",
				},
			}

			if err := cloudinstall.PrettyPrintStandaloneAccessControlConfig(accessControlConfig, os.Stdout); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	return cmd
}

func newAccessControlShowCommand() *cobra.Command {
	serverUrl := ""

	cmd := &cobra.Command{
		Use:                   "show",
		Short:                 "Show the access control specification",
		Long:                  "Show the access control specification",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			accessControlConfig := getAccessControlConfigFromServerUrl(cmd.Context(), serverUrl)
			cred := getCredForTenant(cmd.Context(), accessControlConfig.TenantID)
			if err := cloudinstall.CompleteAcessControlConfig(cmd.Context(), accessControlConfig, cred); err != nil {
				log.Fatal().Err(err).Send()
			}

			standaloneConfig := &cloudinstall.StandaloneAccessControlConfig{
				ConfigFileCommon: install.ConfigFileCommon{
					Kind: cloudinstall.ConfigKindAccessControl,
				},
				AccessControlConfig: accessControlConfig,
			}

			if err := cloudinstall.PrettyPrintStandaloneAccessControlConfig(standaloneConfig, os.Stdout); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	cmd.Flags().StringVar(&serverUrl, "server-url", "", "The Tyger server URL")
	cmd.MarkFlagRequired("server-url")

	return cmd
}

func newAccessControlApplyCommand() *cobra.Command {
	filePath := ""
	organization := ""
	cmd := &cobra.Command{
		Use:                   "apply -f access-control.yml",
		Short:                 "Apply the access control configuration",
		Long:                  "Apply the access control configuration",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			yamlBytes, err := os.ReadFile(filePath)
			if err != nil {
				log.Fatal().Err(err).Msgf("Unable to read configuration file: %s", filePath)
			}

			installCommon, err := parseConfigFileCommon(yamlBytes)
			if err != nil {
				log.Fatal().Err(err).Msgf("Unable to parse configuration file: %s", filePath)
			}

			var desiredAccessControlConfig *cloudinstall.AccessControlConfig
			orgIndex := 0
			switch installCommon.Kind {
			case cloudinstall.ConfigKindCloud:
				c, err := parseConfig(yamlBytes)
				if err != nil {
					log.Fatal().Err(err).Msgf("Unable to parse cloud configuration file: %s", filePath)
				}

				cloudConfig := c.(*cloudinstall.CloudEnvironmentConfig)
				var orgConfig *cloudinstall.OrganizationConfig
				if organization != "" {
					if orgIndex = slices.IndexFunc(cloudConfig.Organizations, func(o *cloudinstall.OrganizationConfig) bool {
						return o.Name == organization
					}); orgIndex != -1 {
						orgConfig = cloudConfig.Organizations[orgIndex]
					} else {
						log.Fatal().Msgf("Organization '%s' not found in the cloud configuration", organization)
					}
				} else if len(cloudConfig.Organizations) == 1 {
					orgConfig = cloudConfig.Organizations[0]
					organization = orgConfig.Name
				} else if len(cloudConfig.Organizations) > 1 {
					log.Fatal().Msg("Multiple organizations found in the cloud configuration. Please specify the organization using --org flag.")
				} else {
					log.Fatal().Msg("No organizations found in the cloud configuration. Please add an organization to the cloud configuration.")
				}

				if orgConfig.Api == nil {
					log.Fatal().Msg("The `api` field is required in the organization configuration")
				}
				desiredAccessControlConfig = orgConfig.Api.AccessControl
				if desiredAccessControlConfig == nil {
					log.Fatal().Msg("The `api.accessControl` field is required in the organization configuration")
				}

			case cloudinstall.ConfigKindAccessControl:
				standalongConfig, err := parseStandaloneAccessControlConfig(yamlBytes)
				if err != nil {
					log.Fatal().Err(err).Msgf("Unable to parse access control specification file: %s", filePath)
				}

				desiredAccessControlConfig = standalongConfig.AccessControlConfig
			}

			if desiredAccessControlConfig.TenantID == "" {
				log.Fatal().Msg("The `tenantId` field is required in the access control specification file")
			}

			cred := getCredForTenant(cmd.Context(), desiredAccessControlConfig.TenantID)
			completedAccessControlConfig, err := cloudinstall.ApplyAccessControlConfig(cmd.Context(), desiredAccessControlConfig, cred)
			if err != nil {
				log.Fatal().Err(err).Msg("Unable to apply access control specification")
			}

			completedAccessControlAst, err := yaml.ValueToNode(completedAccessControlConfig, yaml.IndentSequence(true))
			if err != nil {
				log.Fatal().Err(err).Msg("Unable to convert access control specification to AST")
			}

			switch installCommon.Kind {
			case cloudinstall.ConfigKindCloud:
				cloudConfigAst, err := parseConfigToAst(yamlBytes)
				if err != nil {
					log.Fatal().Err(err).Msgf("Unable to parse cloud configuration file: %s", filePath)
				}

				path, err := yaml.PathString(fmt.Sprintf("$.organizations[%d].api.accessControl", orgIndex))
				if err != nil {
					panic(fmt.Errorf("unable to create YAML path: %w", err))
				}

				accessControlAst, err := path.FilterNode(cloudConfigAst)
				if err != nil {
					panic(fmt.Errorf("unable to filter YAML path: %w", err))
				}

				mergeAst(accessControlAst, completedAccessControlAst)
				if err := os.WriteFile(filePath, []byte(cloudConfigAst.String()+"\n"), 0644); err != nil {
					log.Fatal().Err(err).Msgf("Unable to write updated cloud configuration file: %s", filePath)
				}
			case cloudinstall.ConfigKindAccessControl:
				originalAst, err := parseStandaloneAccessControlConfigToAst(yamlBytes)
				if err != nil {
					log.Fatal().Err(err).Msgf("Unable to parse access control specification file: %s", filePath)
				}

				mergeAst(originalAst, completedAccessControlAst)
				if err := os.WriteFile(filePath, []byte(originalAst.String()+"\n"), 0644); err != nil {
					log.Fatal().Err(err).Msgf("Unable to write updated access control specification file: %s", filePath)
				}
			}
		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "The path to cloud environment file or standalone access control configuration file")
	cmd.MarkFlagRequired("file")

	cmd.Flags().StringVarP(&organization, "org", "o", "", "Then organization name for which to apply the access control configuration. Does not apply to standalone access control configuration files.")
	return cmd
}

func newAccessControlPrettyPrintCommand() *cobra.Command {
	inputPath := ""
	outputPath := ""

	cmd := &cobra.Command{
		Use:                   "pretty-print -f access-control.yml",
		Short:                 "Pretty print a standalone access control configuration file",
		Long:                  "Pretty print a standalone access control configuration file",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			yamlBytes, err := os.ReadFile(inputPath)
			if err != nil {
				log.Fatal().Err(err).Msgf("Unable to read configuration file: %s", inputPath)
			}

			var accessControlConfig *cloudinstall.StandaloneAccessControlConfig
			if installCommon, err := parseConfigFileCommon(yamlBytes); err == nil && installCommon.Kind == cloudinstall.ConfigKindAccessControl {
				accessControlConfig, err = parseStandaloneAccessControlConfig(yamlBytes)
				if err != nil {
					log.Fatal().Err(err).Msgf("Unable to parse access control configuration file: %s", inputPath)
				}
			} else {
				log.Fatal().Msgf("The file '%s' is not a valid standalone access control configuration file", inputPath)
			}

			outputBuffer := bytes.Buffer{}
			if err := cloudinstall.PrettyPrintStandaloneAccessControlConfig(accessControlConfig, &outputBuffer); err != nil {
				log.Fatal().Err(err).Send()
			}

			if err := os.MkdirAll(path.Dir(outputPath), 0775); err != nil {
				log.Fatal().AnErr("error", err).Msg("Failed to create config directory")
			}

			if err := os.WriteFile(outputPath, outputBuffer.Bytes(), 0644); err != nil {
				log.Fatal().AnErr("error", err).Msg("Failed to write config file")
			}
		},
	}

	cmd.Flags().StringVarP(&inputPath, "input", "i", "", "The path to the config file to read")
	cmd.MarkFlagRequired("input")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "The path to the config file to create")
	cmd.MarkFlagRequired("output")

	return cmd
}

func getAccessControlConfigFromServerUrl(ctx context.Context, serverUrl string) *cloudinstall.AccessControlConfig {
	serviceMetadata, err := controlplane.GetServiceMetadata(ctx, serverUrl)
	if err != nil {
		log.Fatal().Err(err).Msg("Unable to get service metadata")
	}

	parsedAuthority, err := url.Parse(serviceMetadata.Authority)
	if err != nil {
		log.Fatal().Err(err).Msg("Unable to parse authority URL")
	}

	segments := strings.Split(strings.Trim(parsedAuthority.Path, "/"), "/")
	if len(segments) == 0 {
		log.Fatal().Msgf("Authority path is invalid: %s", parsedAuthority.Path)
	}

	tenantID := segments[0]
	if _, err := uuid.Parse(tenantID); err != nil {
		log.Fatal().Msgf("Tenant ID is invalid: %s", tenantID)
	}

	config := &cloudinstall.AccessControlConfig{
		TenantID:  segments[0],
		ApiAppUri: serviceMetadata.ApiAppId,
		ApiAppId:  serviceMetadata.ApiAppId,
		CliAppUri: serviceMetadata.CliAppUri,
		CliAppId:  serviceMetadata.CliAppId,
	}

	if config.ApiAppUri == "" {
		if _, err := uuid.Parse(serviceMetadata.Audience); err != nil {
			config.ApiAppUri = serviceMetadata.Audience
		} else {
			config.ApiAppUri = serviceMetadata.ApiAppId
		}
	}

	return config
}

func getCredForTenant(ctx context.Context, tenantId string) azcore.TokenCredential {
	cred, err := cloudinstall.NewMiAwareAzureCLICredential(&azidentity.AzureCLICredentialOptions{TenantID: tenantId})
	if err != nil {
		log.Fatal().Err(err).Msg("Unable to create Azure CLI credential")
	}
	for {
		if _, err := cloudinstall.GetGraphToken(ctx, cred); err != nil {
			fmt.Printf("Run 'az login --tenant %s --allow-no-subscriptions' from another terminal window.\nPress any key when ready...\n\n", tenantId)
			getSingleKey()
			continue
		}
		break
	}
	return cred
}
