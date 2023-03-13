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

func main() {
	cmd := cmdline.NewCommonRootCommand(commit)
	cmd.Use = "buffer-proxy"
	originalPreRunE := cmd.PersistentPreRunE
	originalPreRun := cmd.PersistentPreRun

	cmd.PersistentPreRunE = func(cmd *cobra.Command, args []string) error {
		if originalPreRunE != nil {
			err := originalPreRunE(cmd, args)
			if err != nil {
				return err
			}
		} else if originalPreRun != nil {
			originalPreRun(cmd, args)
		}

		cmdline.WarnIfRunningInPowerShell()
		return nil
	}

	cmd.AddCommand(newWriteCommand())
	cmd.AddCommand(newReadCommand())
	cmd.AddCommand(newGenerateCommand())

	err := cmd.Execute()
	if err != nil {
		os.Exit(1)
	}
}
