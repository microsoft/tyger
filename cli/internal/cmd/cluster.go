package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"

	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/spf13/cobra"
)

func NewClusterCommand() *cobra.Command {
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

	cmd.AddCommand(newClusterListCommand())

	return cmd
}

func newClusterListCommand() *cobra.Command {
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
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodGet, "v1/clusters/", nil, &clusters)
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
