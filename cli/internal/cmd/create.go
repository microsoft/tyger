package cmd

import (
	"errors"

	"github.com/spf13/cobra"
)

func newCreateCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "create",
		Short:                 "Create a resource",
		Long:                  `Create a resource.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a subcommand is required")
		},
	}

	cmd.AddCommand(newCreateBufferCommand(rootFlags))
	cmd.AddCommand(newCreateCodespecCommand(rootFlags))
	cmd.AddCommand(newCreateRunCommand(rootFlags))

	return cmd
}
