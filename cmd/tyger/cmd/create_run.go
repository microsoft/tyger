package cmd

import (
	"fmt"
	"log"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/internal/model"
	"github.com/spf13/cobra"
)

func newCreateRunCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	var flags struct {
		codespec        string
		codespecVersion int
		buffers         map[string]string
	}

	cmd := &cobra.Command{
		Use:                   "run --codespec NAME [--version CODESPEC_VERSION] [--buffer NAME=VALUE] ...]",
		Short:                 "Creates a run.",
		Long:                  `Creates a buffer. Writes the run ID to stdout on success.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {

			codespecRef := flags.codespec
			if cmd.Flag("version").Changed {
				codespecRef = fmt.Sprintf("%s/versions/%d", codespecRef, flags.codespecVersion)
			}

			run := model.Run{
				Codespec: codespecRef,
				Buffers:  flags.buffers,
			}

			_, err := invokeRequest(http.MethodPost, "v1/runs", run, &run, rootFlags.verbose)
			if err != nil {
				return err
			}

			fmt.Println(run.Id)
			return nil
		},
	}

	cmd.Flags().StringVarP(&flags.codespec, "codespec", "c", "", "The name of the codespec to execute")
	if err := cmd.MarkFlagRequired("codespec"); err != nil {
		log.Panicln(err)
	}
	cmd.Flags().IntVar(&flags.codespecVersion, "version", -1, "The version of the codespec to execute")
	cmd.Flags().StringToStringVarP(&flags.buffers, "buffer", "b", nil, "maps a codespec buffer parameter to a buffer ID")

	return cmd
}
