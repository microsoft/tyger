package cmd

import (
	"fmt"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/spf13/cobra"
)

func newCreateBufferCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "buffer",
		Short:                 "Creates a buffer.",
		Long:                  `Creates a buffer. Writes the buffer ID to stdout on success.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			bufferResponse := model.Buffer{}
			_, err := InvokeRequest(http.MethodPost, "v1/buffers", nil, &bufferResponse, rootFlags.verbose)
			if err != nil {
				return err
			}

			fmt.Println(bufferResponse.Id)
			return nil
		},
	}

	return cmd
}
