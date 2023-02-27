package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

func exactlyOneArg(argName string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("one %s positional argument is required", argName)
		}
		if len(args) > 1 {
			return fmt.Errorf("unexpected positional arguments after the %s: %v", argName, args[1:])
		}
		return nil
	}
}
