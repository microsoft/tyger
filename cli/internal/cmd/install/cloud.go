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
	skipShared := false
	orgFlags := install.OrgFlags{}
	cmd := cobra.Command{
		Use:                   "install -f CONFIG.yml [--org ORGANIZATION] [--skip-shared]",
		Short:                 "Install cloud infrastructure",
		Long:                  "Install cloud infrastructure",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx, installer := commonPrerun(cmd.Context(), &flags)
			cloudInstaller := CheckCloudInstaller(installer)
			if err := cloudInstaller.ApplyOrgFilter(orgFlags); err != nil {
				log.Fatal().Err(err).Send()
			}

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
	addOrgFlags(&cmd, &orgFlags, true)
	cmd.Flags().BoolVar(&skipShared, "skip-shared", false, "skip shared resources (i.e. resources that are not specific to an organization)")

	return &cmd
}

func newCloudUninstallCommand() *cobra.Command {
	flags := commonFlags{}
	cmd := cobra.Command{
		Use:                   "uninstall -f CONFIG.yml",
		Short:                 "Uninstall cloud infrastructure",
		Long:                  "Uninstall cloud infrastructure",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			ctx, installer := commonPrerun(cmd.Context(), &flags)
			cloudInstaller := CheckCloudInstaller(installer)

			log.Ctx(ctx).Info().Msg("Starting cloud uninstall")

			if err := cloudInstaller.UninstallCloud(ctx); err != nil {
				if !errors.Is(err, install.ErrAlreadyLoggedError) {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}

			log.Ctx(ctx).Info().Msg("Uninstall complete")
		},
	}

	addCommonFlags(&cmd, &flags)
	return &cmd
}

func addCommonFlags(cmd *cobra.Command, flags *commonFlags) {
	cmd.Flags().StringVarP(&flags.configPath, "file", "f", "", "path to the installation configuration YAML file")
	cmd.MarkFlagRequired("file")
	cmd.Flags().StringToStringVar(&flags.setOverrides, "set", nil, "override config values (e.g. --set cloud.subscriptionID=1234 --set cloud.resourceGroup=mygroup)")
}

func addOrgFlags(cmd *cobra.Command, orgFlags *install.OrgFlags, allowMany bool) {
	if allowMany {
		cmd.Flags().StringArrayVarP(&orgFlags.SpecifiedOrgs, "org", "o", nil, "the organization names")
		cmd.Flags().BoolVar(&orgFlags.AllOrgs, "all-orgs", false, "apply to all organizations")
		cmd.MarkFlagsMutuallyExclusive("org", "all-orgs")
	} else {
		cmd.Flags().StringArrayVarP(&orgFlags.SpecifiedOrgs, "org", "o", nil, "the organization name")
	}
}

func CheckCloudInstaller(installer install.Installer) *cloudinstall.Installer {
	cloudInstaller, ok := installer.(*cloudinstall.Installer)

	if !ok {
		log.Fatal().Msgf("This command is only supported on configurations where the `kind` field is `%s`.", cloudinstall.EnvironmentKindCloud)
	}

	return cloudInstaller
}
