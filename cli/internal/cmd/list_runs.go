package cmd

import (
	"encoding/json"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/kaz-yamam0t0/go-timeparser/timeparser"
	"github.com/spf13/cobra"
)

func newListRunsCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	var flags struct {
		limit int
		since string
	}

	cmd := &cobra.Command{
		Use:                   "runs [--since DATE/TIME] [--limit COUNT]",
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
