package cmd

import (
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/clicontext"
	"github.com/spf13/cobra"
)

func newLogoutCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	return &cobra.Command{
		Use:                   "logout",
		Short:                 "Logout from a server",
		Long:                  `Logout from a server.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return clicontext.Logout()
		},
	}
}
