package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/spf13/cobra"
)

func newListClustersCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	return &cobra.Command{
		Use:                   "clusters",
		Short:                 "List clusters",
		Long:                  `List clusters.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			clusters := make([]model.Cluster, 0)
			_, err := InvokeRequest(http.MethodGet, "v1/clusters/", nil, &clusters, rootFlags.verbose)
			if err != nil {
				return err
			}

			formattedRun, err := json.MarshalIndent(clusters, "", "  ")
			if err != nil {
				return err
			}

			fmt.Println(string(formattedRun))
			return nil
		},
	}
}
