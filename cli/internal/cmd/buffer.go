package cmd

import (
	"errors"
	"fmt"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/spf13/cobra"
)

func newBufferCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "buffer",
		Aliases:               []string{"buffers"},
		Short:                 "Manage buffers",
		Long:                  `Manage buffers.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newBufferCreateCommand(rootFlags))
	cmd.AddCommand(newBufferAccessCommand(rootFlags))

	return cmd
}

func newBufferCreateCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "create",
		Short:                 "Create a buffer",
		Long:                  `Create a buffer. Writes the buffer ID to stdout on success.`,
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

func newBufferAccessCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	var flags struct {
		writeable bool
	}

	cmd := &cobra.Command{
		Use:                   "access BUFFER_ID [--write]",
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
