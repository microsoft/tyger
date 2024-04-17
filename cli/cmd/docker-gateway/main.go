// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"

	"github.com/microsoft/tyger/cli/internal/cmd"
	"github.com/spf13/cobra"
)

var (
	// set during build
	version = ""
)

func main() {
	err := newRootCommand().Execute()
	if err != nil {
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	rootCommand := cmd.NewCommonRootCommand(version)
	rootCommand.Use = "docker-gateway"
	rootCommand.Short = "Opens an HTTP listener that proxies requests to listeners on Unix domain sockets."

	rootCommand.Run = func(cmd *cobra.Command, args []string) {
		panic("not implemented")
	}

	return rootCommand
}
