package cmd

import (
	"github.com/spf13/cobra"
)

func newAccessCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	var accessCmd = &cobra.Command{
		Use:                   "access",
		Short:                 "Get data plane access to a resource.",
		Long:                  `Get data plane access to a resource`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run:                   func(*cobra.Command, []string) {},
	}

	accessCmd.AddCommand(newAccessBufferCommand(rootFlags))
	return accessCmd
}
