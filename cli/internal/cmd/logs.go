package cmd

import (
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"time"

	"github.com/kaz-yamam0t0/go-timeparser/timeparser"
	"github.com/spf13/cobra"
)

func newLogsCommand(rootFlags *rootPersistentFlags) *cobra.Command {
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
