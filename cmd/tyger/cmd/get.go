package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

func newGetCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "get",
		Short:                 "Get a resource",
		Long:                  `Get a resource.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a subcommand is required")
		},
	}

	cmd.AddCommand(newGetCodespecCommand(rootFlags))
	cmd.AddCommand(newGetRunCommand(rootFlags))

	return cmd
}
