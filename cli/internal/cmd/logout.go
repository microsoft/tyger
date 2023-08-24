package cmd

import (
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/spf13/cobra"
)

func NewLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:                   "logout",
		Short:                 "Logout from a server",
		Long:                  `Logout from a server.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return controlplane.Logout()
		},
	}
}
