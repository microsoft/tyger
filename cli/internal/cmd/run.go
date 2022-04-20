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
	var flags struct {
		codespec        string
		codespecVersion int
		buffers         map[string]string
		cluster         string
		nodePool        string
		timeout         string
	}

	cmd := &cobra.Command{
		Use:                   "create --codespec NAME [--version CODESPEC_VERSION] [--buffer NAME=VALUE] ...]",
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
		previous   bool
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
			if flags.previous {
				queryOptions.Add("previous", "true")
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
	cmd.Flags().BoolVarP(&flags.previous, "previous", "p", false, "If the run has restarted, get the logs from the previous attempt")

	return cmd
}
