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
	flags := newMultiOrgFlags()
	skipShared := false
	cmd := cobra.Command{
		Use:                   "install -f CONFIG.yml [--org ORGANIZATION] [--skip-shared]",
		Short:                 "Install cloud infrastructure",
		Long:                  "Install cloud infrastructure",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx, installer := commonPrerun(cmd.Context(), &flags)
			cloudInstaller := CheckCloudInstaller(installer)

			log.Ctx(ctx).Info().Msg("Starting cloud install")
			if err := cloudInstaller.InstallCloud(ctx, skipShared); err != nil {
				if !errors.Is(err, install.ErrAlreadyLoggedError) {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}
			log.Ctx(ctx).Info().Msg("Install complete")
		},
	}

	addCommonFlags(&cmd, &flags)
	cmd.Flags().BoolVar(&skipShared, "skip-shared", false, "skip shared resources (i.e. resources that are not specific to an organization)")

	return &cmd
}

func newCloudUninstallCommand() *cobra.Command {
	flags := newMultiOrgFlags()
	all := false
	cmd := cobra.Command{
		Use:                   "uninstall -f CONFIG.yml",
		Short:                 "Uninstall cloud infrastructure",
		Long:                  "Uninstall cloud infrastructure",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx, installer := commonPrerun(cmd.Context(), &flags)
			cloudInstaller := CheckCloudInstaller(installer)
			if !all {
				for _, org := range cloudInstaller.Config.Organizations {
					if org.SingleOrganizationCompatibilityMode {
						log.Fatal().Msgf("The '%s' organization is in single-organization compatibility mode and cannot be uninstalled individually. The entire environment must be removed using the --all flag.", org.Name)
					}
				}
			}

			log.Ctx(ctx).Info().Msg("Starting cloud uninstall")

			if err := cloudInstaller.UninstallCloud(ctx, all); err != nil {
				if !errors.Is(err, install.ErrAlreadyLoggedError) {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}

			log.Ctx(ctx).Info().Msg("Uninstall complete")
		},
	}

	addCommonFlags(&cmd, &flags)
	cmd.Flags().BoolVar(&all, "all", false, "uninstall the entire cloud infrastructure, including shared resources")
	cmd.MarkFlagsMutuallyExclusive("all", "org")
	return &cmd
}

func addCommonFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringVarP(&flags.configPath, "file", "f", "", "path to the installation configuration YAML file")
	if !flags.configPathOptional {
		cmd.MarkFlagRequired("file")
	}
	if flags.singleOrg != nil {
		cmd.Flags().StringVarP(flags.singleOrg, "org", "o", "", "the organization this command will affect")
	} else if flags.multiOrg != nil {
		cmd.Flags().StringArrayVarP(flags.multiOrg, "org", "o", nil, "restrict this command to the specified organizations")
	} else {
		panic("either singleOrg or multiOrg must be set")
	}

	cmd.Flags().StringToStringVar(&flags.setOverrides, "set", nil, "override config values (e.g. --set cloud.subscriptionID=1234 --set cloud.resourceGroup=mygroup)")
}

func CheckCloudInstaller(installer install.Installer) *cloudinstall.Installer {
	cloudInstaller, ok := installer.(*cloudinstall.Installer)

	if !ok {
		log.Fatal().Msgf("This command is only supported on configurations where the `kind` field is `%s`.", cloudinstall.EnvironmentKindCloud)
	}

	return cloudInstaller
}
