package cmd

import (
	"dev.azure.com/msresearch/compimag/_git/tyger/cmd/tyger/cmd/clicontext"
	"github.com/spf13/cobra"
)

func newLoginCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	loginCmd := &cobra.Command{
		Use:   "login SERVER_URL",
		Short: "Login to a server",
		Long: `Login to the Tyger server at the given URL.
Subsequent commands will be performed against this server.`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("server url"),
		RunE: func(cmd *cobra.Command, args []string) error {
			serverUri := args[0]
			ctx := clicontext.CliContext{ServerUri: serverUri}
			if err := ctx.Validate(); err != nil {
				return err
			}

			return clicontext.WriteCliContext(ctx)
		},
	}

	loginCmd.AddCommand(newLoginStatusCommand(rootFlags))

	return loginCmd
}
