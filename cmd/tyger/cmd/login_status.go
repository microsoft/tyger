/*
Copyright © 2022 NAME HERE <EMAIL ADDRESS>

*/
package cmd

import (
	"fmt"

	"dev.azure.com/msresearch/compimag/_git/tyger/cmd/tyger/cmd/clicontext"
	"github.com/spf13/cobra"
)

func newLoginStatusCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	return &cobra.Command{
		Use:                   "status",
		Short:                 "Get the login status",
		Long:                  `Get the login status.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			context, err := clicontext.GetCliContext()
			if err == nil {
				err = context.Validate()
				if err == nil {
					fmt.Printf("You are logged into %s\n", context.ServerUri)
					return
				}
			}

			fmt.Println("You are not currently logged in to any Tyger server.")
		},
	}
}
