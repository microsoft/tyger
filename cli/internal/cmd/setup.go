package cmd

import (
	"errors"

	"github.com/microsoft/tyger/cli/internal/setup"
	"github.com/spf13/cobra"
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

			config := &setup.Config{
				EnvironmentName:               "js",
				SubscriptionID:                "biomedicalimaging-nonprod",
				Location:                      "westus2",
				AttachedContainerRegistries:   []string{"eminence"},
				ClusterUserPrincipalObjectIds: []string{},

				Clusters: []*setup.ClusterConfig{
					{
						Name:              "js",
						Location:          "westus2",
						KubernetesVersion: "1.27",
						UserNodePools: []*setup.NodePoolConfig{
							{
								Name:     "cpunp",
								VMSize:   "Standard_DS2_v2",
								MinCount: 0,
								MaxCount: 5,
								Count:    0,
							},
							{
								Name:     "gpunp",
								VMSize:   "Standard_NC6s_v3",
								MinCount: 0,
								MaxCount: 10,
								Count:    0,
							},
						},
						ControlPlane: &setup.ControlPlaneClusterConfig{
							LogStorage: &setup.StorageAccountConfig{
								Name: "jstygerlog",
							},
							DnsLabel: "js-tyger",
						},
					},
				},

				Buffers: []*setup.StorageAccountConfig{
					{
						Name: "jstygerbuf",
					},
				},
			}

			options := &setup.Options{
				// SkipClusterSetup: true,
			}

			setup.SetupInfrastructure(config, options)
		},
	}

	return cmd
}
