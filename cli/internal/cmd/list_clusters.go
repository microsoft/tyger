package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/spf13/cobra"
)

func newListClustersCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	var flags struct {
		limit uint
	}

	cmd := &cobra.Command{
		Use:                   "clusters [--limit LIMIT]",
		Short:                 "List clusters",
		Long:                  `List clusters.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusters := make([]model.Cluster, 0)
			_, err := InvokeRequest(http.MethodGet, "v1/clusters/", nil, &clusters, rootFlags.verbose)
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
