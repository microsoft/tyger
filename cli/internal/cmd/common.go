package cmd

import (
	"fmt"
	"os"
	"runtime"

	"github.com/mitchellh/go-ps"
	"github.com/rs/zerolog/log"
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

func warnIfRunningInPowerShell() {
	parentPid := os.Getppid()
	parentProcess, err := ps.FindProcess(parentPid)
	if err == nil && parentProcess != nil {
		switch parentProcess.Executable() {
		case "pwsh", "pwsh.exe", "powershell.exe":
			var suggestion string
			if runtime.GOOS == "windows" {
				suggestion = "Consider using cmd or bash."
			} else {
				suggestion = "Consider using a different shell."
			}

			log.Warn().Msgf("PowerShell I/O redirection may corrupt binary data. See https://github.com/PowerShell/PowerShell/issues/1908. %s", suggestion)
		}
	}
}
