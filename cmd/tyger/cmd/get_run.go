package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/internal/model"
	"github.com/spf13/cobra"
)

func newGetRunCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	return &cobra.Command{
		Use:                   "run ID",
		Short:                 "Get a run",
		Long:                  `Get a run.`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("run name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			run := model.Run{}
			_, err := invokeRequest(http.MethodGet, fmt.Sprintf("v1/runs/%s", args[0]), nil, &run, rootFlags.verbose)
			if err != nil {
				return err
			}

			formattedRun, err := json.MarshalIndent(run, "", "  ")
			if err != nil {
				return err
			}

			fmt.Println(string(formattedRun))
			return nil
		},
	}
}
