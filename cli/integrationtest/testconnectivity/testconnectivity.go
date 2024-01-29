// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build integrationtest

package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"

	"github.com/hashicorp/go-retryablehttp"
)

const port = 29477

func main() {
	workerFlag := flag.Bool("worker", false, "worker")
	jobFlag := flag.Bool("job", false, "job")

	flag.Parse()

	if *workerFlag == *jobFlag {
		log.Fatal("Exactly one of -worker or -job must be specified")
	}

	if *workerFlag {
		http.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
			fmt.Fprint(w, os.Getenv("HOSTNAME"))
		})

		log.Fatal(http.ListenAndServe(fmt.Sprintf(":%d", port), nil))
	}

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
}
