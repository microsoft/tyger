package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

func newListCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "list",
		Short:                 "List resources",
		Long:                  `List resources.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a subcommand is required")
		},
	}

	cmd.AddCommand(newListClustersCommand(rootFlags))

	return cmd
}
