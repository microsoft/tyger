/*
Copyright © 2022 NAME HERE <EMAIL ADDRESS>

*/
package cmd

import (
	"errors"
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
		RunE: func(cmd *cobra.Command, args []string) error {
			context, err := clicontext.GetCliContext()
			if err == nil {
				err = context.Validate()
				if err == nil {
					fmt.Printf("you are logged into %s as %s\n", context.GetServerUri(), context.GetPrincipal())
					return nil
				}
			}

			return errors.New("you are not currently logged in to any Tyger server")
		},
	}
}
