// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"errors"
	"fmt"
	"os"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
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
	flags := commonFlags{}
	cmd := &cobra.Command{
		Use:                   "install",
		Short:                 "Install Entra ID identities",
		Long:                  "Install Entra ID identities",
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			ctx := commonPrerun(cmd.Context(), &flags)

			config := cloudinstall.GetCloudEnvironmentConfigFromContext(ctx)
			cred, err := cloudinstall.NewMiAwareAzureCLICredential(&azidentity.AzureCLICredentialOptions{TenantID: config.Api.Auth.TenantID})
			if err != nil {
				return err
			}
			for {
				ctx = cloudinstall.SetAzureCredentialOnContext(ctx, cred)
				if _, err := cloudinstall.GetGraphToken(ctx, cred); err != nil {
					fmt.Printf("Run 'az login --tenant %s --allow-no-subscriptions' from another terminal window.\nPress any key when ready...\n\n", config.Api.Auth.TenantID)
					getSingleKey()
					continue
				}
				break
			}

			log.Info().Msg("Starting identities install")

			if err := cloudinstall.InstallIdentities(ctx, cred); err != nil {
				if err != install.ErrAlreadyLoggedError {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}

			log.Info().Msg("Install complete")

			return nil
		},
	}

	return cmd
}
