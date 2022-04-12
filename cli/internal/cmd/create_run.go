package cmd

import (
	"fmt"
	"log"
	"net/http"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/spf13/cobra"
)

func newCreateRunCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	var flags struct {
		codespec        string
		codespecVersion int
		buffers         map[string]string
		cluster         string
		nodePool        string
		timeout         string
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

			newRun := model.NewRun{
				Codespec: codespecRef,
				Buffers:  flags.buffers,
			}
			if flags.cluster != "" || flags.nodePool != "" {
				newRun.ComputeTarget = &model.RunComputeTarget{Cluster: flags.cluster, NodePool: flags.nodePool}
			}

			if flags.timeout != "" {
				duration, err := time.ParseDuration(flags.timeout)
				if err != nil {
					return err
				}

				seconds := int(duration.Seconds())
				newRun.TimeoutSeconds = &seconds
			}

			run := model.Run{}
			_, err := InvokeRequest(http.MethodPost, "v1/runs", newRun, &run, rootFlags.verbose)
			if err != nil {
				return err
			}

			fmt.Println(run.Id)
			return nil
		},
	}

	cmd.Flags().StringVarP(&flags.codespec, "codespec", "c", "", "The name of the codespec to execute")
	cmd.Flags().StringVar(&flags.cluster, "cluster", "", "The name of the cluster to execute in")
	cmd.Flags().StringVar(&flags.nodePool, "node-pool", "", "The name of the nodepool to execute in")
	cmd.Flags().StringVar(&flags.timeout, "timeout", "", `How log before the run times out. Specified in a sequence of decimal numbers, each with optional fraction and a unit suffix, such as "300s", "1.5h" or "2h45m". Valid time units are "s", "m", "h"`)
	if err := cmd.MarkFlagRequired("codespec"); err != nil {
		log.Panicln(err)
	}
	cmd.Flags().IntVar(&flags.codespecVersion, "version", -1, "The version of the codespec to execute")
	cmd.Flags().StringToStringVarP(&flags.buffers, "buffer", "b", nil, "maps a codespec buffer parameter to a buffer ID")

	return cmd
}
