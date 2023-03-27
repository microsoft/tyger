package cmd

import (
	"os"
	"runtime"

	"github.com/mitchellh/go-ps"
	"github.com/rs/zerolog/log"
)

func WarnIfRunningInPowerShell() {
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
