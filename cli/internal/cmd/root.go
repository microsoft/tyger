package cmd

import (
	"os"

	"github.com/spf13/cobra"
)

type rootPersistentFlags struct {
	verbose bool
}

func newRootCommand() *cobra.Command {
	flags := &rootPersistentFlags{}

	cmd := &cobra.Command{
		Use:          "tyger",
		Short:        "A command-line interface to the Tyger control plane.",
		Long:         `A command-line interface to the Tyger control plane.`,
		SilenceUsage: true,
	}

	// hide --help as a flag in the usage output
	cmd.PersistentFlags().BoolP("help", "h", false, "Print usage")
	cmd.PersistentFlags().Lookup("help").Hidden = true

	cmd.PersistentFlags().BoolVarP(&flags.verbose, "verbose", "v", false, "write verbose output to stderr")

	cmd.AddCommand(newLoginCommand(flags))
	cmd.AddCommand(newLogoutCommand(flags))
	cmd.AddCommand(newAccessCommand(flags))
	cmd.AddCommand(newCreateCommand(flags))
	cmd.AddCommand(newGetCommand(flags))
	cmd.AddCommand(newLogsCommand(flags))
	cmd.AddCommand(newListCommand(flags))

	return cmd
}

func Execute() {
	err := newRootCommand().Execute()
	if err != nil {
		os.Exit(1)
	}
}
