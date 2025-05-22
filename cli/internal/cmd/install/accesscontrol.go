// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/google/uuid"
	"github.com/microsoft/tyger/cli/internal/controlplane"
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

	cmd.AddCommand(newAccessControlInitCommand())
	cmd.AddCommand(newAccessControlShowCommand())
	cmd.AddCommand(newAccessControlApplyCommand())
	return cmd
}

func newAccessControlInitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "init",
		Short:                 "Initialize an access control specification file",
		Long:                  "Initialize an access control specification file",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			accessControlConfig := &cloudinstall.AccessControlConfig{
				ApiAppUri: "api://tyger-server",
				CliAppUri: "api://tyger-cli",
			}

			if err := cloudinstall.PrettyPrintAccessControlConfig(accessControlConfig, os.Stdout); err != nil {
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

			if err := cloudinstall.PrettyPrintAccessControlConfig(accessControlConfig, os.Stdout); err != nil {
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
	cmd := &cobra.Command{
		Use:                   "apply -f access-control.yml",
		Short:                 "Apply the access control configuration",
		Long:                  "Apply the access control configuration",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			spec, err := cloudinstall.ParseAccessControlConfigFromFile(filePath)
			if err != nil {
				log.Fatal().Err(err).Msgf("Unable to load access control specification file: %s", filePath)
			}

			if spec.TenantID == "" {
				log.Fatal().Msg("Tenant ID is required in the access control specification file")
			}
			cred := getCredForTenant(cmd.Context(), spec.TenantID)
			normalizedSpec, err := cloudinstall.ApplyAccessControlConfig(cmd.Context(), spec, cred)
			if err != nil {
				log.Fatal().Err(err).Msg("Unable to apply access control specification")
			}

			f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				log.Fatal().Err(err).Msgf("Unable to open file for writing: %s", filePath)
			}
			defer f.Close()

			if err := cloudinstall.PrettyPrintAccessControlConfig(normalizedSpec, f); err != nil {
				log.Fatal().Err(err).Msg("Unable to pretty print access control specification")
			}
		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "The path to the access control specification file")
	cmd.MarkFlagRequired("file")

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
