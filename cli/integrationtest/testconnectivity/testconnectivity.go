// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build integrationtest

package main

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/spf13/cobra"
)

const port = 29477

func newRootCommand() *cobra.Command {
	cmd := &cobra.Command{}

	cmd.AddCommand(newWorkerCommand())
	cmd.AddCommand(newJobCommand())
	cmd.AddCommand(newSocketCommand())
	return cmd
}

func newWorkerCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "worker",
		Short: "worker",
		Run: func(cmd *cobra.Command, args []string) {
			http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
				fmt.Fprint(w, os.Getenv("HOSTNAME"))
			})

			log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
		},
	}

	return cmd
}

func newJobCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "job",
		Short: "job",
		Run: func(cmd *cobra.Command, args []string) {
			nodesString, ok := os.LookupEnv("TYGER_WORKER_NODES")
			if !ok {
				log.Fatal("TYGER_WORKER_NODES missing")
			}

			var hostnames []string
			if err := json.Unmarshal([]byte(nodesString), &hostnames); err != nil {
				log.Fatal(err)
			}

			workerEndpointsString, ok := os.LookupEnv("TYGER_TESTWORKER_WORKER_ENDPOINT_ADDRESSES")
			if !ok {
				log.Fatal("TYGER_TESTWORKER_WORKER_ENDPOINT_ADDRESSES missing")
			}

			if len(hostnames) <= 1 {
				log.Fatal("Expected several hostnames")
			}

			var workerEndpoints []string
			if err := json.Unmarshal([]byte(workerEndpointsString), &workerEndpoints); err != nil {
				log.Fatal(err)
			}

			if len(workerEndpoints) != len(hostnames) {
				log.Fatalf("Number of worker endpoints (%d) does not match number of hostnames (%d)", len(workerEndpoints), len(hostnames))
			}

			results := make(map[string]string)
			for _, addr := range workerEndpoints {
				resp, err := retryablehttp.Get(fmt.Sprintf("http://%s", addr))
				if err != nil {
					log.Fatal(err)
				}
				defer resp.Body.Close()

				bytes, err := io.ReadAll(resp.Body)
				if err != nil {
					log.Fatal(err)
				}

				results[strings.ToLower(string(bytes))] = ""
			}

			if len(results) != len(hostnames) {
				log.Fatalf("Did not get expected number of unique responses. Expected %d. Actual %d.", len(hostnames), len(results))
			}
		},
	}

	return cmd
}

func newSocketCommand() *cobra.Command {
	port := 0
	delayString := ""
	cmd := &cobra.Command{
		Use:   "socket",
		Short: "socket",
		RunE: func(cmd *cobra.Command, args []string) error {
			if delayString != "" {
				delay, err := time.ParseDuration(delayString)
				if err != nil {
					return fmt.Errorf("failed to parse delay: %w", err)
				}

				log.Printf("Delaying for %s", delayString)
				time.Sleep(delay)
			}

			listner, err := net.Listen("tcp", fmt.Sprintf(":%d", port))
			if err != nil {
				return fmt.Errorf("failed to listen: %w", err)
			}
			defer listner.Close()
			log.Println("Listening on", listner.Addr().String())

			conn, err := listner.Accept()
			if err != nil {
				return fmt.Errorf("failed to accept: %w", err)
			}

			defer conn.Close()

			log.Println("Accepted connection from", conn.RemoteAddr().String())
			buffer := make([]byte, 1024)
			for {
				n, err := conn.Read(buffer)
				if n > 0 {
					for i := 0; i < n; i++ {
						buffer[i] = buffer[i] + 1
					}
					if _, err := conn.Write(buffer[:n]); err != nil {
						return fmt.Errorf("failed to write: %w", err)
					}
				}
				if err != nil {
					if err == io.EOF {
						return nil
					}
					return fmt.Errorf("failed to read: %w", err)
				}
			}
		},
	}

	cmd.Flags().IntVar(&port, "port", 0, "port")
	cmd.Flags().StringVar(&delayString, "delay", "", "delay")
	return cmd
}

func main() {
	if err := newRootCommand().Execute(); err != nil {
		log.Fatal(err)
	}
}
