// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/dustin/go-humanize"
	"github.com/spf13/cobra"
	"go.uber.org/ratelimit"
)

func main() {
	var (
		f   string
		n   int32
		rps int
	)

	rootCmd := &cobra.Command{
		Use:   "loader",
		Short: "A simple load testing tool for Tyger",
		Run: func(cmd *cobra.Command, args []string) {
			reportProgressEvery := n / 100
			if reportProgressEvery == 0 {
				reportProgressEvery = 1
			}

			limiter := ratelimit.New(rps)
			wg := sync.WaitGroup{}

			start := time.Now()
			startString := start.Format(time.RFC3339)

			progressChannel := make(chan progress, 10)
			wg.Add(1)
			go func() {
				defer wg.Done()

				getCountsString := func() (string, bool) {
					cmd := exec.Command("tyger", "run", "counts", "--since", startString)
					b, err := cmd.Output()
					exitOnCmdError(err)

					countByStatus := map[string]int64{}
					if err := json.Unmarshal(b, &countByStatus); err != nil {
						panic(err)
					}
					entries := make([]string, 0, len(countByStatus))
					for _, k := range []string{"pending", "running", "succeeded", "failed", "canceled"} {
						v := countByStatus[string(k)]
						if v != 0 {
							entries = append(entries, fmt.Sprintf("%s: %s", k, humanize.Comma(v)))
						}
					}

					terminal := (countByStatus["pending"] + countByStatus["running"]) == 0

					return strings.Join(entries, ", "), terminal
				}

				var countsString string
				var terminal bool
				var lastTimestamp time.Time
				for p := range progressChannel {
					lastTimestamp = p.timestamp
					countsString, terminal = getCountsString()
					var localCountsString string
					if p.i != p.n || terminal {
						localCountsString = fmt.Sprintf("(%s)", countsString)
					}
					fmt.Printf("\r\033[K%v: Created %s (%d%%) @ %d RPS %s", p.timestamp.Sub(start).Round(time.Second), humanize.Comma(int64(p.i)), p.i*100/n, p.cycleRps, localCountsString)
				}

				fmt.Println()
				endCreateTime := lastTimestamp

				for i := 0; !terminal; i++ {
					if i > 0 {
						countsString, terminal = getCountsString()
					}
					fmt.Printf("\r\033[K+%v: %s", time.Since(endCreateTime).Round(time.Second), countsString)
					if !terminal {
						time.Sleep(1 * time.Second)
					}
				}

			}()

			preCount := atomic.Int32{}
			postCount := atomic.Int32{}
			cycleStart := start
			for range rps * 2 {
				wg.Add(1)
				go func() {
					defer wg.Done()
					for {
						i := preCount.Add(1)
						if i > n {
							return
						}

						limiter.Take()
						cmd := exec.Command("tyger", "run", "create", "-f", f)
						_, err := cmd.Output()
						exitOnCmdError(err)

						i = postCount.Add(1)

						if i%int32(reportProgressEvery) == 0 || i == n {
							cycleEnd := time.Now()
							cycleElapsed := cycleEnd.Sub(cycleStart)
							cycleRps := int(math.Round(float64(reportProgressEvery) / cycleElapsed.Seconds()))
							cycleStart = cycleEnd
							progressChannel <- progress{i, n, cycleEnd, cycleRps}
							if i == n {
								close(progressChannel)
								return
							}
						}
					}
				}()
			}

			wg.Wait()
			fmt.Println()
		},
	}

	rootCmd.Flags().StringVarP(&f, "file", "f", "", "Path to the file to use for the run")
	rootCmd.MarkFlagRequired("file")
	rootCmd.Flags().Int32VarP(&n, "number", "n", 10_000, "Number of runs to create")
	rootCmd.Flags().IntVarP(&rps, "rps", "r", 30, "Requests per second")

	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

type progress struct {
	i         int32
	n         int32
	timestamp time.Time
	cycleRps  int
}

func exitOnCmdError(err error) {
	if err == nil {
		return
	}

	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		fmt.Fprintln(os.Stderr, string(exitError.Stderr))
	} else {
		fmt.Fprintln(os.Stderr, err)
	}

	os.Exit(1)
}
