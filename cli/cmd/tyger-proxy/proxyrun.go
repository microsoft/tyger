package main

import (
	"io"
	"os"
	"path"
	"path/filepath"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/controlplane"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/logging"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/proxy"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

const proxyIsListeningMessage = "Proxy is listening"

func newProxyRunCommand(optionsFilePath *string, options *proxy.ProxyOptions) *cobra.Command {
	cmd := &cobra.Command{
		Use:   "run",
		Short: "Run the proxy",
		Long:  `Runs the proxy. If the process is successful in starting the proxy, it will stay running indefinitely.`,
		Args:  cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			if err := readProxyOptions(*optionsFilePath, options); err != nil {
				log.Fatal().Err(err).Msg("failed to read proxy options")
			}

			exitIfRunning(options, true)

			var logFile *os.File
			if options.LogPath != "" {
				if isPathDirectoryIntent(options.LogPath) {
					f, err := createLogFileInDirectory(options.LogPath)
					if err != nil {
						log.Fatal().Err(err).Msg("failed to create log file")
					}

					logFile = f
				} else {
					absPath, err := filepath.Abs(options.LogPath)
					if err != nil {
						log.Fatal().Err(err).Msg("failed to get absolute path of log file")
					}
					if err := os.MkdirAll(path.Dir(absPath), 0755); err != nil {
						log.Fatal().Err(err).Msg("failed to create log file directory")
					}

					f, err := os.OpenFile(absPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
					if err != nil {
						log.Fatal().Err(err).Msg("failed to create log file")
					}

					logFile = f
				}

				defer logFile.Close()
				options.LogPath = logFile.Name()
				sink := logging.GetLogSinkFromContext(cmd.Context())
				sink = io.MultiWriter(sink, logFile)

				log.Logger = log.Output(sink)
				log.Info().Str("path", logFile.Name()).Msg("Logging to file")
			}

			serviceInfo, err := controlplane.Login(options.AuthConfig)
			if err != nil {
				log.Fatal().Err(err).Msg("failed to get service info")
			}

			_, err = proxy.RunProxy(serviceInfo, options, log.Logger)
			if err != nil {
				if err == proxy.ErrProxyAlreadyRunning {
					log.Info().Int("port", options.Port).Msg("A proxy is already running at this address.")
					return
				}

				log.Fatal().Err(err).Msg("failed to start proxy")
			}

			log.Info().Int("port", options.Port).Msg(proxyIsListeningMessage)

			// wait indefinitely
			<-(make(chan any))
		},
	}

	addFileFlag(cmd, optionsFilePath)
	return cmd
}
