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

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
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

				authConfig = &cloudinstall.AuthConfig{
					TenantID:  tenantID,
					ApiAppUri: serviceMetadata.Audience,
					CliAppUri: serviceMetadata.CliAppUri,
				}
			} else {
				var installer install.Installer
				ctx, installer = commonPrerun(cmd.Context(), &flags)
				cloudInstaller := CheckCloudInstaller(installer)
				org := cloudInstaller.Config.GetSingleOrg()
				authConfig = org.Api.Auth
			}

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
