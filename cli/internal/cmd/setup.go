package cmd

import (
	"errors"
	"os"

	"github.com/microsoft/tyger/cli/internal/setup"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

func NewSetupCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "setup",
		Aliases:               []string{"install"},
		Short:                 "Setup cloud infrastructure and the Tyger service",
		Long:                  "Setup cloud infrastructure and the Tyger service",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newsetupCloudCommand())

	return cmd
}

func newsetupCloudCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "cloud",
		Short:                 "Setup cloud infrastructure",
		Long:                  "Setup cloud infrastructure",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			configBytes, err := os.ReadFile("/workspaces/tyger/config.yml")
			if err != nil {
				log.Fatal().Err(err).Msg("failed to read config file")
			}

			var config setup.EnvironmentConfig
			if err = yaml.UnmarshalStrict(configBytes, &config, yaml.DisallowUnknownFields); err != nil {
				log.Fatal().Err(err).Msg("failed to unmarshal config file")
			}

			options := &setup.Options{
				// SkipClusterSetup: true,
			}

			setup.SetupInfrastructure(&config, options)
		},
	}

	return cmd
}
