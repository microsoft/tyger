// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"errors"
	"os"

	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func NewCloudCommand(parentCommand *cobra.Command) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "cloud",
		Short:                 "Manage cloud infrastructure",
		Long:                  "Manage cloud infrastructure",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newCloudInstallCommand())
	cmd.AddCommand(newCloudUninstallCommand())

	return cmd
}

func newCloudInstallCommand() *cobra.Command {
	flags := commonFlags{}
	cmd := cobra.Command{
		Use:                   "install",
		Short:                 "Install cloud infrastructure",
		Long:                  "Install cloud infrastructure",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx, installer := commonPrerun(cmd.Context(), &flags)
			cloudInstaller := CheckCloudInstaller(installer)

			log.Info().Msg("Starting cloud install")
			if err := cloudInstaller.InstallCloud(ctx); err != nil {
				if err != install.ErrAlreadyLoggedError {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}
			log.Info().Msg("Install complete")
		},
	}

	addCommonFlags(&cmd, &flags)
	return &cmd
}

func newCloudUninstallCommand() *cobra.Command {
	flags := commonFlags{}
	cmd := cobra.Command{
		Use:                   "uninstall",
		Short:                 "Uninstall cloud infrastructure",
		Long:                  "Uninstall cloud infrastructure",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx, installer := commonPrerun(cmd.Context(), &flags)
			cloudInstaller := CheckCloudInstaller(installer)

			log.Info().Msg("Starting cloud uninstall")

			if err := cloudInstaller.UninstallCloud(ctx); err != nil {
				if err != install.ErrAlreadyLoggedError {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}

			log.Info().Msg("Uninstall complete")
		},
	}

	addCommonFlags(&cmd, &flags)
	return &cmd
}

func addCommonFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringVarP(&flags.configPath, "file", "f", "", "path to config file")
	cmd.Flags().StringToStringVar(&flags.setOverrides, "set", nil, "override config values (e.g. --set cloud.subscriptionID=1234 --set cloud.resourceGroup=mygroup)")
}

func CheckCloudInstaller(installer install.Installer) *cloudinstall.Installer {
	cloudInstaller, ok := installer.(*cloudinstall.Installer)

	if !ok {
		log.Fatal().Msgf("This command is only supported on configurations where the `kind` field is `%s`.", cloudinstall.EnvironmentKindCloud)
	}

	return cloudInstaller
}
