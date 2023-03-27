package main

import (
	"os"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/cmd"
	"github.com/spf13/cobra"
)

var (
	// set during build
	commit = ""
)

func newRootCommand() *cobra.Command {
	rootCommand := cmd.NewCommonRootCommand(commit)
	rootCommand.Use = "tyger"
	rootCommand.Short = "A command-line interface to the Tyger control plane."
	rootCommand.Long = `A command-line interface to the Tyger control plane.`

	rootCommand.AddCommand(cmd.NewLoginCommand())
	rootCommand.AddCommand(cmd.NewLogoutCommand())
	rootCommand.AddCommand(cmd.NewBufferCommand())
	rootCommand.AddCommand(cmd.NewCodespecCommand())
	rootCommand.AddCommand(cmd.NewRunCommand())
	rootCommand.AddCommand(cmd.NewClusterCommand())

	return rootCommand
}

func main() {
	err := newRootCommand().Execute()
	if err != nil {
		os.Exit(1)
	}
}
