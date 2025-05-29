// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"bytes"
	"context"
	"fmt"
	"net/url"
	"os"
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
	outputFilePath := ""

	cmd := &cobra.Command{
		Use:                   "init [-f FILE.yml]",
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

			buffer := bytes.Buffer{}
			if err := cloudinstall.PrettyPrintStandaloneAccessControlConfig(accessControlConfig, &buffer); err != nil {
				log.Fatal().Err(err).Send()
			}

			writeToOutputPathOrStdoutFatalOnError(outputFilePath, buffer.Bytes())
		},
	}

	cmd.Flags().StringVarP(&outputFilePath, "file", "f", "", "The path to the output file to save the access control specification. If not specified, the output will be printed to stdout.")

	return cmd
}

func newAccessControlShowCommand() *cobra.Command {
	serverUrl := ""
	outputFilePath := ""

	cmd := &cobra.Command{
		Use:                   "show --server-url URL [-f FILE.yml]",
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

			buffer := bytes.Buffer{}
			if err := cloudinstall.PrettyPrintStandaloneAccessControlConfig(standaloneConfig, &buffer); err != nil {
				log.Fatal().Err(err).Send()
			}

			writeToOutputPathOrStdoutFatalOnError(outputFilePath, buffer.Bytes())
		},
	}

	cmd.Flags().StringVar(&serverUrl, "server-url", "", "The Tyger server URL")
	cmd.MarkFlagRequired("server-url")

	cmd.Flags().StringVarP(&outputFilePath, "file", "f", "", "The path to the output file to save the access control specification. If not specified, the output will be printed to stdout.")

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

			installCommon, err := ParseConfigFileCommon(yamlBytes)
			if err != nil {
				log.Fatal().Err(err).Msgf("Unable to parse configuration file: %s", filePath)
			}

			var desiredAccessControlConfig *cloudinstall.AccessControlConfig
			orgIndex := 0
			switch installCommon.Kind {
			case cloudinstall.ConfigKindCloud:
				c, err := ParseConfig(yamlBytes)
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
				standaloneConfig, err := parseStandaloneAccessControlConfig(yamlBytes)
				if err != nil {
					log.Fatal().Err(err).Msgf("Unable to parse access control specification file: %s", filePath)
				}

				desiredAccessControlConfig = standaloneConfig.AccessControlConfig
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
				cloudConfigAst, err := ParseConfigToAst(yamlBytes)
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

	cmd.Flags().StringVar(&organization, "org", "", "The organization name for which to apply the access control configuration. Does not apply to standalone access control configuration files.")
	return cmd
}

func newAccessControlPrettyPrintCommand() *cobra.Command {
	inputPath := ""
	outputPath := ""
	singleFilePath := ""

	cmd := &cobra.Command{
		Use:                   "pretty-print -f FILE.yml | { -i INPUT.yml [-o OUTPUT.yml] }",
		Short:                 "Pretty print a standalone access control configuration file",
		Long:                  "Pretty print a standalone access control configuration file",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			if singleFilePath != "" {
				if inputPath != "" {
					log.Fatal().Msg("Cannot specify both --input and --file flags")
				}
				if outputPath != "" {
					log.Fatal().Msg("Cannot specify both --output and --file flags")
				}
				outputPath = singleFilePath
				inputPath = singleFilePath
			} else if inputPath == "" {
				log.Fatal().Msg("Either --file or --input must be specified")
			}

			yamlBytes, err := os.ReadFile(inputPath)
			if err != nil {
				log.Fatal().Err(err).Msgf("Unable to read configuration file: %s", inputPath)
			}

			var accessControlConfig *cloudinstall.StandaloneAccessControlConfig
			if installCommon, err := ParseConfigFileCommon(yamlBytes); err == nil && installCommon.Kind == cloudinstall.ConfigKindAccessControl {
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

			writeToOutputPathOrStdoutFatalOnError(outputPath, outputBuffer.Bytes())
		},
	}

	cmd.Flags().StringVarP(&inputPath, "input", "i", "", "The path to the config file to read")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "The path to the config file to create. If not specified and --file is not used, the output will be printed to stdout.")
	cmd.Flags().StringVarP(&singleFilePath, "file", "f", "", "The path to the configuration file to update in-place. Equivalent to --input and --output with the same value.")

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
