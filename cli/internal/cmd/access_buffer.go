package cmd

import (
	"fmt"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/spf13/cobra"
)

func newAccessBufferCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	var flags struct {
		writeable bool
	}

	cmd := &cobra.Command{
		Use:                   "buffer BUFFER_ID [--write]",
		Short:                 "Get a URI to be able to read or write to a buffer",
		Long:                  `Get a URI to be able to read or write to a buffer`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("buffer ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			bufferAccess := model.BufferAccess{}
			uri := fmt.Sprintf("v1/buffers/%s/access?writeable=%t", args[0], flags.writeable)
			_, err := InvokeRequest(http.MethodPost, uri, nil, &bufferAccess, rootFlags.verbose)

			if err != nil {
				return err
			}

			fmt.Println(bufferAccess.Uri)

			return nil
		},
	}

	cmd.Flags().BoolVarP(&flags.writeable, "write", "w", false, "request write access instead of read-only access to the buffer.")

	return cmd
}
