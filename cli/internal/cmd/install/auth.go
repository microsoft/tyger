// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"bytes"
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
	"gopkg.in/yaml.v3"
	"k8s.io/utils/ptr"
)

func NewAuthCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "auth",
		Short:                 "Manage access control",
		Long:                  "Manage access control",
		DisableFlagsInUseLine: true,
	}

	cmd.AddCommand(newAuthInitCommand())
	cmd.AddCommand(newAuthShowCommand())
	cmd.AddCommand(newAuthApplyCommand())
	return cmd
}

func newAuthInitCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "init",
		Short:                 "Initialize an access control specification file",
		Long:                  "Initialize an access control specification file",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			authSpec := &cloudinstall.TygerAuthSpec{
				AuthConfig: cloudinstall.AuthConfig{
					RbacEnabled: ptr.To(true),
					ApiAppUri:   "api://tyger-server",
					CliAppUri:   "api://tyger-cli",
				},
			}

			if err := cloudinstall.PrettyPrintAuthSpec(authSpec, os.Stdout); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	return cmd
}

func newAuthShowCommand() *cobra.Command {
	serverUrl := ""

	cmd := &cobra.Command{
		Use:                   "show",
		Short:                 "Show the access control specification",
		Long:                  "Show the access control specification",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			authConfig := getAuthConfigFromServerUrl(cmd.Context(), serverUrl)
			cred := getCredForTenant(cmd.Context(), authConfig.TenantID)
			authSpec, err := cloudinstall.GetAuthSpec(cmd.Context(), authConfig, cred)
			if err != nil {
				log.Fatal().Err(err).Send()
			}

			if err := cloudinstall.PrettyPrintAuthSpec(authSpec, os.Stdout); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	cmd.Flags().StringVar(&serverUrl, "server-url", "", "The server URL to use for authentication")
	cmd.MarkFlagRequired("server-url")

	return cmd
}

func newAuthApplyCommand() *cobra.Command {
	filePath := ""
	cmd := &cobra.Command{
		Use:                   "apply -f auth.yml",
		Short:                 "Apply the access control specification",
		Long:                  "Apply the access control specification",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			contents, err := os.ReadFile(filePath)
			if err != nil {
				log.Fatal().Err(err).Msgf("Unable to read file: %s", filePath)
			}

			buf := bytes.NewBuffer(contents)
			spec := &cloudinstall.TygerAuthSpec{}
			decoder := yaml.NewDecoder(buf)
			decoder.KnownFields(true)
			if err := decoder.Decode(spec); err != nil {
				log.Fatal().Err(err).Msgf("Unable to decode file: %s", filePath)
			}

			if spec.TenantID == "" {
				log.Fatal().Msg("Tenant ID is required in the access control specification file")
			}
			cred := getCredForTenant(cmd.Context(), spec.TenantID)
			normalizedSpec, err := cloudinstall.ApplyAuthSpec(cmd.Context(), spec, cred)
			if err != nil {
				log.Fatal().Err(err).Msg("Unable to apply access control specification")
			}

			f, err := os.OpenFile(filePath, os.O_WRONLY|os.O_TRUNC, 0644)
			if err != nil {
				log.Fatal().Err(err).Msgf("Unable to open file for writing: %s", filePath)
			}
			defer f.Close()

			if err := cloudinstall.PrettyPrintAuthSpec(normalizedSpec, f); err != nil {
				log.Fatal().Err(err).Msg("Unable to pretty print access control specification")
			}
		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "The path to the access control specification file")
	cmd.MarkFlagRequired("file")

	return cmd
}

func getAuthConfigFromServerUrl(ctx context.Context, serverUrl string) *cloudinstall.AuthConfig {
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

	config := &cloudinstall.AuthConfig{
		RbacEnabled: &serviceMetadata.RbacEnabled,
		TenantID:    segments[0],
		ApiAppUri:   serviceMetadata.ApiAppId,
		ApiAppId:    serviceMetadata.ApiAppId,
		CliAppUri:   serviceMetadata.CliAppUri,
		CliAppId:    serviceMetadata.CliAppId,
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
