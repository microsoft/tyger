// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func NewIdentitiesCommand(parentCommand *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "identities",
		Short:                 "Manage Entra ID identities",
		Long:                  "Manage Entra ID identities",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newIdentitiesInstallCommand())
	cmd.AddCommand(newRbacCommand())
	return cmd
}

func newIdentitiesInstallCommand() *cobra.Command {
	serverUrl := ""
	flags := newSingleOrgCommonFlags()
	flags.configPathOptional = true
	flags.skipLoginAndValidateSubscription = true

	cmd := &cobra.Command{
		Use:                   "install -f CONFIG.yml | --server-url URL",
		Short:                 "Install Entra ID identities",
		Long:                  "Install Entra ID identities",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {

			ctx := cmd.Context()

			var authConfig *cloudinstall.AuthConfig
			if serverUrl != "" {
				var stopFunc context.CancelFunc
				ctx, stopFunc = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

				go func() {
					<-ctx.Done()
					stopFunc()
					log.Ctx(ctx).Warn().Msg("Canceling...")
				}()

				authConfig = getOrgAuthConfigFromServerUrl(ctx, serverUrl)
			} else {
				var installer install.Installer
				ctx, installer = commonPrerun(cmd.Context(), &flags)
				cloudInstaller := CheckCloudInstaller(installer)
				org := cloudInstaller.Config.GetSingleOrg()
				authConfig = org.Api.Auth
			}

			cred := getOrgTenantCred(ctx, authConfig)

			log.Ctx(ctx).Info().Msg("Starting identities install")

			if err := cloudinstall.InstallIdentities(ctx, authConfig, cred); err != nil {
				if !errors.Is(err, install.ErrAlreadyLoggedError) {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}

			log.Ctx(ctx).Info().Msg("Install complete")
		},
	}

	addCommonFlags(cmd, &flags)
	cmd.Flags().StringVar(&serverUrl, "server-url", "", "The URL of the Tyger server")
	cmd.MarkFlagsMutuallyExclusive("server-url", "file")
	cmd.MarkFlagsMutuallyExclusive("server-url", "org")

	return cmd
}

func newRbacCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "rbac",
		Short:                 "Manage RBAC assignments",
		Long:                  "Manage RBAC assignments",
		DisableFlagsInUseLine: true,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newRbacShowCommand())
	cmd.AddCommand(newRbacApplyCommand())
	return cmd
}

func newRbacShowCommand() *cobra.Command {
	serverUrl := ""
	cmd := &cobra.Command{
		Use:                   "show",
		Short:                 "Show RBAC assignments",
		Long:                  "Show RBAC assignments",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			authConfig := getOrgAuthConfigFromServerUrl(cmd.Context(), serverUrl)
			cred := getOrgTenantCred(cmd.Context(), authConfig)
			rbacConfig, err := cloudinstall.GetRbacAssignments(cmd.Context(), cred, serverUrl, authConfig, true)
			if err != nil {
				log.Fatal().Err(err).Msg("Unable to get RBAC assignments")
			}

			enc := yaml.NewEncoder(os.Stdout)
			enc.SetIndent(2)

			if err := enc.Encode(rbacConfig); err != nil {
				log.Fatal().Err(err).Msg("Unable to marshal RBAC assignments")
			}
		},
	}

	cmd.Flags().StringVar(&serverUrl, "server-url", "", "The URL of the Tyger server")
	cmd.MarkFlagRequired("server-url")

	return cmd
}

func newRbacApplyCommand() *cobra.Command {
	filePath := ""
	cmd := &cobra.Command{
		Use:                   "apply -f RBAC.yml",
		Short:                 "Apply RBAC assignments",
		Long:                  "Apply RBAC assignments",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			f, err := os.Open(filePath)
			if err != nil {
				log.Fatal().Err(err).Msg("Unable to open RBAC assignments file")
			}
			defer f.Close()

			var rbacConfig cloudinstall.TygerRbacConfig
			dec := yaml.NewDecoder(f)
			if err := dec.Decode(&rbacConfig); err != nil {
				log.Fatal().Err(err).Msg("Unable to decode RBAC assignments file")
			}
			if rbacConfig.ServerUrl == "" {
				log.Fatal().Msg("Server URL is required in the RBAC assignments file")
			}
			authConfig := getOrgAuthConfigFromServerUrl(cmd.Context(), rbacConfig.ServerUrl)
			cred := getOrgTenantCred(cmd.Context(), authConfig)
			if normalizedAssignments, err := cloudinstall.ApplyRbacAssignments(cmd.Context(), cred, &rbacConfig, authConfig); err != nil {
				log.Fatal().Err(err).Msg("Unable to apply RBAC assignments")
			} else {
				enc := yaml.NewEncoder(os.Stdout)
				enc.SetIndent(2)

				if err := enc.Encode(normalizedAssignments); err != nil {
					log.Fatal().Err(err).Msg("Unable to marshal RBAC assignments")
				}
			}

		},
	}

	cmd.Flags().StringVarP(&filePath, "file", "f", "", "The path to the RBAC assignments file")
	cmd.MarkFlagRequired("file")

	return cmd
}

func getOrgAuthConfigFromServerUrl(ctx context.Context, serverUrl string) *cloudinstall.AuthConfig {
	serviceMetadata, err := controlplane.GetServiceMetadata(ctx, serverUrl)
	if err != nil {
		log.Fatal().Err(err).Msg("Unable to get service metadata")
	}

	parsedAuthority, err := url.Parse(serviceMetadata.Authority)
	if err != nil {
		log.Fatal().Err(err).Msg("Unable to parse authority URL")
	}

	segments := strings.Split(strings.Trim(parsedAuthority.Path, "/"), "/")
	if len(segments) != 1 {
		log.Fatal().Msgf("Authority path is invalid: %s", parsedAuthority.Path)
	}
	tenantID := segments[0]

	return &cloudinstall.AuthConfig{
		TenantID:  tenantID,
		ApiAppUri: serviceMetadata.Audience,
		CliAppUri: serviceMetadata.CliAppUri,
	}
}

func getOrgTenantCred(ctx context.Context, authConfig *cloudinstall.AuthConfig) azcore.TokenCredential {
	cred, err := cloudinstall.NewMiAwareAzureCLICredential(&azidentity.AzureCLICredentialOptions{TenantID: authConfig.TenantID})
	if err != nil {
		log.Fatal().Err(err).Msg("Unable to create Azure CLI credential")
	}
	for {
		if _, err := cloudinstall.GetGraphToken(ctx, cred); err != nil {
			fmt.Printf("Run 'az login --tenant %s --allow-no-subscriptions' from another terminal window.\nPress any key when ready...\n\n", authConfig.TenantID)
			getSingleKey()
			continue
		}
		break
	}
	return cred
}
