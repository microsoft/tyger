package main

import (
	"os"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/cmdline"
	"github.com/spf13/cobra"
)

var (
	// set during build
	commit = ""
)

func newRootCommand() *cobra.Command {
	cmd := cmdline.NewCommonRootCommand(commit)
	cmd.Use = "tyger"
	cmd.Short = "A command-line interface to the Tyger control plane."
	cmd.Long = `A command-line interface to the Tyger control plane.`

	cmd.AddCommand(newLoginCommand())
	cmd.AddCommand(newLogoutCommand())
	cmd.AddCommand(newBufferCommand())
	cmd.AddCommand(newCodespecCommand())
	cmd.AddCommand(newRunCommand())
	cmd.AddCommand(newClusterCommand())

	return cmd
}

func main() {
	err := newRootCommand().Execute()
	if err != nil {
		os.Exit(1)
	}
}
