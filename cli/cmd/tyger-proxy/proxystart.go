package main

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/proxy"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

func newProxyStartCommand(optionsFilePath *string, options *proxy.ProxyOptions) *cobra.Command {
	cmd := &cobra.Command{
		SilenceUsage: true,
		Use:          "start",
		Short:        "Checks that a proxy is running on the specified port and if not, starts one in a separate process.",
		Args:         cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			err := readProxyOptions(*optionsFilePath, options)
			if err != nil {
				log.Fatal().Err(err).Msg("failed to read proxy options")
			}

			if options.LogPath == "" {
				log.Fatal().Msg("When calling start, the options file must specify the `logPath`")
			}

			exitIfRunning(options, true)

			if isPathDirectoryIntent(options.LogPath) {
				logFile, err := createLogFileInDirectory(options.LogPath)
				if err != nil {
					log.Fatal().Err(err).Msg("Unable to create log file")
				}
				logFile.Close()
				options.LogPath = logFile.Name()
			}

			// start the proxy in a separate process

			processCommand := exec.Command(os.Args[0], "run", "--file", "-", "--log-format", "json", "--log-level", "info")

			optionsBytes, err := yaml.Marshal(options)
			if err != nil {
				log.Fatal().Err(err).Msg("unable to marshal options to YAML")
			}

			processCommand.Stdin = bytes.NewBuffer(optionsBytes)

			if err := processCommand.Start(); err != nil {
				log.Fatal().Err(err).Msg("failed to start process")
			}

			exitStatusChan := make(chan int)
			go func() {
				err := processCommand.Wait()
				if err != nil {
					if exitError, ok := err.(*exec.ExitError); ok {
						exitStatusChan <- exitError.ExitCode()
					} else {
						log.Fatal().Err(err).Msg("unexpected error running process")
					}
				} else {
					exitStatusChan <- 0
				}
			}()

			for i := 0; i < 30; i++ {
				select {
				case exitCode := <-exitStatusChan:
					switch exitCode {
					case 0:
						exitIfRunning(options, true)
						fallthrough
					default:
						copyLogs(options.LogPath)
						log.Fatal().Int("exitCode", exitCode).Msg("failed to start proxy")
					}

				case <-time.After(time.Second):
					if options.Port == 0 {
						port, err := getPortFromLogs(options.LogPath)
						if err == nil && port != 0 {
							options.Port = port
						}
					}
					exitIfRunning(options, false)
				}
			}

			copyLogs(options.LogPath)
			log.Fatal().Msg("Timed out waiting for proxy to start")
		},
	}

	addFileFlag(cmd, optionsFilePath)
	return cmd
}

func getPortFromLogs(logFilePath string) (int, error) {
	f, err := os.Open(logFilePath)
	if err != nil {
		return 0, err
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parsedEntry := make(map[string]any)
		if err := json.Unmarshal([]byte(line), &parsedEntry); err != nil {
			return 0, fmt.Errorf("failed to parse log entry: %w", err)
		}

		if message, ok := parsedEntry["message"].(string); ok && message == proxyIsListeningMessage {
			if port, ok := parsedEntry["port"].(float64); ok {
				return int(port), nil
			}
		}
	}

	return 0, nil
}

func copyLogs(logFilePath string) {
	f, err := os.Open(logFilePath)
	if err != nil {
		log.Error().Err(err).Msg("Failed to copy logs")
		return
	}
	defer f.Close()

	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := scanner.Text()
		parsedEntry := make(map[string]any)
		if err := json.Unmarshal([]byte(line), &parsedEntry); err != nil {
			log.Error().Err(err).Str("line", line).Msg("failed to parse log entry")
		}

		levelString, ok := parsedEntry["level"].(string)
		if !ok {
			log.Error().Str("line", line).Msg("failed to parse log entry")
		}

		level, err := zerolog.ParseLevel(levelString)
		if err != nil {
			log.Error().Err(err).Str("line", line).Msg("failed to parse log entry")
		}

		event := log.WithLevel(level)
		for key, value := range parsedEntry {
			if key != "level" && key != "message" {
				event = event.Interface(key, value)
			}
		}

		if message, ok := parsedEntry["message"].(string); ok {
			event.Msg(message)
		} else {
			event.Send()
		}
	}
}
