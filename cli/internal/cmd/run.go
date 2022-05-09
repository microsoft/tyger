package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/kaz-yamam0t0/go-timeparser/timeparser"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
)

func newRunCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "run",
		Aliases:               []string{"runs"},
		Short:                 "Manage runs",
		Long:                  `Manage runs`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newRunCreateCommand(rootFlags))
	cmd.AddCommand(newRunShowCommand(rootFlags))
	cmd.AddCommand(newRunLogsCommand(rootFlags))
	cmd.AddCommand(newRunListCommand(rootFlags))

	return cmd
}

func newRunCreateCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	type codeTargetFlags struct {
		codespec        string
		codespecVersion int
		buffers         map[string]string
		nodePool        string
		replicas        int
	}
	var flags struct {
		job     codeTargetFlags
		worker  codeTargetFlags
		cluster string
		timeout string
	}

	getCodespecRef := func(ctf codeTargetFlags) string {
		if ctf.codespecVersion != math.MinInt {
			return fmt.Sprintf("%s/versions/%d", ctf.codespec, ctf.codespecVersion)
		}
		return ctf.codespec
	}

	cmd := &cobra.Command{
		Use: `create --codespec NAME [--version CODESPEC_VERSION] [[--buffer NAME=VALUE] ...] [--replicas COUNT]  [--node-pool NODEPOOL]
		[ --worker-codespec NAME [--worker-version CODESPEC_VERSION] [--worker-replicas COUNT]  [--worker-node-pool NODEPOOL] ]
		[--timeout DURATION] [--cluster CLUSTER]`,
		Short:                 "Creates a run.",
		Long:                  `Creates a buffer. Writes the run ID to stdout on success.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {

			jobRunCodeTarget := model.RunCodeTarget{
				Codespec: getCodespecRef(flags.job),
				Buffers:  flags.job.buffers,
				NodePool: flags.job.nodePool,
				Replicas: flags.job.replicas,
			}

			var workerCodetarget *model.RunCodeTarget
			cmd.Flags().VisitAll(func(f *pflag.Flag) {
				if workerCodetarget == nil && f.Changed && strings.HasPrefix(f.Name, "worker") {
					workerCodetarget = &model.RunCodeTarget{}
				}
			})

			if workerCodetarget != nil {
				if flags.worker.codespec == "" {
					return errors.New("--worker-codespec must be specified if a worker is specified")
				}
				workerCodetarget.Codespec = getCodespecRef(flags.worker)
				workerCodetarget.NodePool = flags.worker.nodePool
				workerCodetarget.Replicas = flags.worker.replicas
			}

			newRun := model.NewRun{
				Job:     jobRunCodeTarget,
				Worker:  workerCodetarget,
				Cluster: flags.cluster,
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

	cmd.Flags().StringVarP(&flags.job.codespec, "codespec", "c", "", "The name of the job codespec to execute")
	if err := cmd.MarkFlagRequired("codespec"); err != nil {
		log.Panicln(err)
	}
	cmd.Flags().IntVar(&flags.job.codespecVersion, "version", math.MinInt, "The version of the job codespec to execute")
	cmd.Flags().IntVarP(&flags.job.replicas, "replicas", "r", 1, "The number of parallel job replicas. Defaults to 1.")
	cmd.Flags().StringVar(&flags.job.nodePool, "node-pool", "", "The name of the nodepool to execute the job in")
	cmd.Flags().StringToStringVarP(&flags.job.buffers, "buffer", "b", nil, "maps a codespec buffer parameter to a buffer ID")

	cmd.Flags().StringVar(&flags.worker.codespec, "worker-codespec", "", "The name of the optional worker codespec to execute")
	cmd.Flags().IntVar(&flags.worker.codespecVersion, "worker-version", math.MinInt, "The version of the optional worker codespec to execute")
	cmd.Flags().IntVar(&flags.worker.replicas, "worker-replicas", 1, "The number of parallel worker replicas. Defaults to 1 if a worker is specified.")
	cmd.Flags().StringVar(&flags.worker.nodePool, "worker-node-pool", "", "The name of the nodepool to execute the optional worker codespec in")

	cmd.Flags().StringVar(&flags.cluster, "cluster", "", "The name of the cluster to execute in")
	cmd.Flags().StringVar(&flags.timeout, "timeout", "", `How log before the run times out. Specified in a sequence of decimal numbers, each with optional fraction and a unit suffix, such as "300s", "1.5h" or "2h45m". Valid time units are "s", "m", "h"`)

	return cmd
}

func newRunShowCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	return &cobra.Command{
		Use:                   "show ID",
		Aliases:               []string{"get"},
		Short:                 "Show the details of a run",
		Long:                  `Show the details of a run.`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("run name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			run := model.Run{}
			_, err := InvokeRequest(http.MethodGet, fmt.Sprintf("v1/runs/%s", args[0]), nil, &run, rootFlags.verbose)
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

func newRunListCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	var flags struct {
		limit int
		since string
	}

	cmd := &cobra.Command{
		Use:                   "list [--since DATE/TIME] [--limit COUNT]",
		Short:                 "List runs",
		Long:                  `List runs. Runs are sorted by descending created time.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			queryOptions := url.Values{}
			if flags.limit > 0 {
				queryOptions.Add("limit", strconv.Itoa(flags.limit))
			} else {
				flags.limit = math.MaxInt
			}
			if flags.since != "" {
				now := time.Now()
				tm, err := timeparser.ParseTimeStr(flags.since, &now)
				if err != nil {
					return fmt.Errorf("failed to parse time %s", flags.since)
				}
				queryOptions.Add("since", tm.UTC().Format(time.RFC3339Nano))
			}

			firstPage := true
			totalPrinted := 0

			for uri := fmt.Sprintf("v1/runs?%s", queryOptions.Encode()); uri != ""; {
				page := model.RunPage{}
				_, err := InvokeRequest(http.MethodGet, uri, nil, &page, rootFlags.verbose)
				if err != nil {
					return err
				}

				if firstPage && page.NextLink == "" {
					formattedRuns, err := json.MarshalIndent(page.Items, "  ", "  ")
					if err != nil {
						return err
					}

					fmt.Println(string(formattedRuns))
					return nil
				}

				if firstPage {
					fmt.Print("[\n  ")
				}

				for i, r := range page.Items {
					if !firstPage || i != 0 {
						fmt.Print(",\n  ")
					}

					formattedRun, err := json.MarshalIndent(r, "  ", "  ")
					if err != nil {
						return err
					}

					fmt.Print(string(formattedRun))
					totalPrinted++
					if totalPrinted == flags.limit {
						goto End
					}
				}

				firstPage = false
				uri = strings.TrimLeft(page.NextLink, "/")
			}
		End:
			fmt.Println("\n]")

			return nil
		},
	}

	cmd.Flags().StringVarP(&flags.since, "since", "s", "", "Results before this datetime (specified in local time) are not included")
	cmd.Flags().IntVarP(&flags.limit, "limit", "l", 1000, "The maximum number of runs to list. Default 1000")

	return cmd
}

func newRunLogsCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	var flags struct {
		timestamps bool
		tailLines  int
		since      string
		follow     bool
	}

	cmd := &cobra.Command{
		Use:                   "logs RUNID",
		Short:                 "Get the logs of a run",
		Long:                  `Get the logs of a run.`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("run ID"),
		RunE: func(cmd *cobra.Command, args []string) error {
			queryOptions := url.Values{}
			if flags.timestamps {
				queryOptions.Add("timestamps", "true")
			}
			if flags.tailLines >= 0 {
				queryOptions.Add("tailLines", strconv.Itoa(flags.tailLines))
			}
			if flags.since != "" {
				now := time.Now()
				tm, err := timeparser.ParseTimeStr(flags.since, &now)
				if err != nil {
					return fmt.Errorf("failed to parse time %s", flags.since)
				}
				queryOptions.Add("since", tm.UTC().Format(time.RFC3339Nano))
			}
			if flags.follow {
				queryOptions.Add("follow", "true")
			}

			resp, err := InvokeRequest(http.MethodGet, fmt.Sprintf("v1/runs/%s/logs?%s", args[0], queryOptions.Encode()), nil, nil, rootFlags.verbose)
			if err != nil {
				return err
			}

			_, err = io.Copy(os.Stdout, resp.Body)
			return err
		},
	}

	cmd.Flags().BoolVar(&flags.timestamps, "timestamps", false, "Include timestamps on each line in the log output")
	cmd.Flags().IntVar(&flags.tailLines, "tail", -1, "Lines of recent log file to display")
	cmd.Flags().StringVarP(&flags.since, "since", "s", "", "Lines before this datetime (specified in local time) are not included")
	cmd.Flags().BoolVarP(&flags.follow, "follow", "f", false, "Specify if the logs should be streamed")

	return cmd
}
