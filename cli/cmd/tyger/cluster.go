package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/tyger"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/tyger/model"
	"github.com/spf13/cobra"
)

func newClusterCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "cluster",
		Aliases:               []string{"clusters"},
		Short:                 "Manage clusters",
		Long:                  `Manage clusters`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newClusterListCommand(rootFlags))

	return cmd
}

func newClusterListCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	var flags struct {
		limit uint
	}

	cmd := &cobra.Command{
		Use:                   "list [--limit LIMIT]",
		Short:                 "List clusters",
		Long:                  `List clusters.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusters := make([]model.Cluster, 0)
			_, err := tyger.InvokeRequest(http.MethodGet, "v1/clusters/", nil, &clusters, rootFlags.verbose)
			if err != nil {
				return err
			}

			formattedClusters, err := json.MarshalIndent(clusters, "", "  ")
			if err != nil {
				return err
			}

			fmt.Println(string(formattedClusters))
			return nil
		},
	}

	cmd.Flags().UintVarP(&flags.limit, "limit", "l", 20, "The maximum number of runs to retrieve")
	return cmd
}
