// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"time"

	"github.com/microsoft/tyger/cli/internal/cmd"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	// set during build
	version = ""
)

func newRootCommand() *cobra.Command {
	rootCommand := cmd.NewCommonRootCommand(version)
	rootCommand.Use = "buffer-sidecar"

	namespace := ""
	podName := ""
	containerName := ""
	tombstoneFile := ""

	openFileFunc := func(filePath string, flag int, perm fs.FileMode) (*os.File, error) {
		return tryOpenFileUntilContainerExits(namespace, podName, containerName, tombstoneFile, filePath, flag, perm)
	}

	intputCommand := cmd.NewBufferReadCommand(openFileFunc)
	intputCommand.Use = "input"
	writeCommand := cmd.NewBufferWriteCommand(openFileFunc)
	writeCommand.Use = "output"

	relayCommand := &cobra.Command{
		Use: "relay",
	}

	rootCommand.AddCommand(relayCommand)

	listenAddresses := make([]string, 0)
	outputFilePath := ""
	primarySigningPublicKeyPath := ""
	secondarySigningPublicKeyPath := ""
	bufferId := ""
	createListener := func(listenAddress string) (net.Listener, error) {
		u, err := url.Parse(listenAddress)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to parse listen address")
		}

		var listener net.Listener
		switch u.Scheme {
		case "unix":
			tempPath := u.Path + "." + "temp"
			defer os.Remove(tempPath)
			listener, err = net.Listen(u.Scheme, tempPath)
			if err == nil {
				if err := os.Rename(tempPath, u.Path); err != nil {
					return nil, fmt.Errorf("failed to move socket: %w", err)
				}
			}
		case "http":
			listener, err = net.Listen("tcp", u.Host)
		default:
			return nil, fmt.Errorf("unsupported scheme: %s", u.Scheme)
		}

		if err != nil {
			return nil, fmt.Errorf("failed to create listener: %w", err)
		}

		log.Info().Str("address", listenAddress).Msg("Listening for connections")

		return listener, nil
	}

	relayInputCommand := &cobra.Command{
		Use: "input",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel := context.WithCancel(cmd.Context())
			impl := func() error {
				validateSignatureFunc, err := dataplane.CreateSignatureValidationFunc(primarySigningPublicKeyPath, secondarySigningPublicKeyPath)
				if err != nil {
					return err
				}

				var outputWriter io.Writer
				if outputFilePath != "" {
					var err error
					outputFile, err := openFileFunc(outputFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
					if err != nil {
						if err == context.Canceled {
							log.Warn().Msg("OpenFile operation canceled. Will discard input")
							outputWriter = io.Discard
							go func() {
								// give some time for a client to connect and pass in the data that will be discarded instead of just closing the listner.
								time.Sleep(time.Minute)
								cancel()
							}()
						} else {
							return fmt.Errorf("failed to open output file: %w", err)
						}
					} else {
						defer outputFile.Close()
						outputWriter = outputFile
					}
				} else {
					outputWriter = os.Stdout
				}

				listeners := make([]net.Listener, 0, len(listenAddresses))
				for _, listenAddress := range listenAddresses {
					listener, err := createListener(listenAddress)
					if err != nil {
						return err
					}

					listeners = append(listeners, listener)
				}

				return dataplane.RelayInputServer(ctx, listeners, bufferId, outputWriter, validateSignatureFunc)
			}

			if err := impl(); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	relayInputCommand.Flags().StringVarP(&outputFilePath, "output", "o", outputFilePath, "The file write to. If not specified, data is written to standard out.")
	relayInputCommand.Flags().StringSliceVarP(&listenAddresses, "listen", "l", listenAddresses, "The address to listen on. Can be specified multiple times.")
	relayInputCommand.MarkFlagRequired("listen")
	relayInputCommand.Flags().StringVarP(&bufferId, "buffer", "b", bufferId, "The buffer ID")
	relayInputCommand.MarkFlagRequired("buffer")
	relayInputCommand.Flags().StringVarP(&primarySigningPublicKeyPath, "primary-public-signing-key", "p", primarySigningPublicKeyPath, "The path to the primary signing public key file")
	relayInputCommand.MarkFlagRequired("primary-public-signing-key")
	relayInputCommand.Flags().StringVarP(&secondarySigningPublicKeyPath, "secondary-public-signing-key", "s", secondarySigningPublicKeyPath, "The path to the secondary signing public key file")
	relayCommand.AddCommand(relayInputCommand)

	inputFilePath := ""
	relayOutputCommand := &cobra.Command{
		Use: "output",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel := context.WithCancel(cmd.Context())
			impl := func() error {
				validateSignatureFunc, err := dataplane.CreateSignatureValidationFunc(primarySigningPublicKeyPath, secondarySigningPublicKeyPath)
				if err != nil {
					return err
				}
				readerChan := make(chan io.ReadCloser, 1)
				errorChan := make(chan error, 1)
				go func() {
					if inputFilePath == "" {
						readerChan <- os.Stdin
					}
					log.Info().Msgf("Opening file %s for reading", inputFilePath)
					inputFile, err := openFileFunc(inputFilePath, os.O_RDONLY, 0)
					if err != nil {
						if err == context.Canceled {
							log.Warn().Msg("OpenFile operation canceled. Will return an empty response body.")
							readerChan <- io.NopCloser(bytes.NewReader([]byte{}))
							go func() {
								// give some time for a client to connect and observe the empty reponse instead of just closing the listener
								time.Sleep(time.Minute)
								cancel()
							}()
						} else {
							errorChan <- err
						}
					} else {
						log.Info().Str("file", inputFilePath).Msg("Opened file for reading")
						readerChan <- inputFile
					}
				}()

				listeners := make([]net.Listener, 0, len(listenAddresses))
				for _, listenAddress := range listenAddresses {
					listener, err := createListener(listenAddress)
					if err != nil {
						return err
					}

					listeners = append(listeners, listener)
				}

				return dataplane.RelayOutputServer(ctx, listeners, bufferId, readerChan, errorChan, validateSignatureFunc)
			}

			if err := impl(); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}
	relayOutputCommand.Flags().StringVarP(&inputFilePath, "input", "i", inputFilePath, "The file to read from. If not specified, data is read from standard in.")
	relayOutputCommand.Flags().StringSliceVarP(&listenAddresses, "listen", "l", listenAddresses, "The address to listen on. Can be specified multiple times.")
	relayOutputCommand.MarkFlagRequired("listen")
	relayOutputCommand.Flags().StringVarP(&bufferId, "buffer", "b", bufferId, "The buffer ID")
	relayOutputCommand.MarkFlagRequired("buffer")
	relayOutputCommand.Flags().StringVarP(&primarySigningPublicKeyPath, "primary-public-signing-key", "p", primarySigningPublicKeyPath, "The path to the primary signing public key file")
	relayOutputCommand.MarkFlagRequired("primary-public-signing-key")
	relayOutputCommand.Flags().StringVarP(&secondarySigningPublicKeyPath, "secondary-public-signing-key", "s", secondarySigningPublicKeyPath, "The path to the secondary signing public key file")

	relayCommand.AddCommand(relayOutputCommand)

	var destinationAddress string
	connectionTimeoutString := "10m"
	socketAdaptCommand := &cobra.Command{
		Use: "socket-adapt",
		Run: func(cmd *cobra.Command, args []string) {
			ctx, cancel := context.WithCancel(cmd.Context())
			defer cancel()

			connectionTimeout, err := time.ParseDuration(connectionTimeoutString)
			if err != nil {
				log.Fatal().Err(err).Msg("Invalid connection timeout duration")
			}

			var conn net.Conn

			for startTime := time.Now(); ; {
				localConn, err := (&net.Dialer{}).DialContext(ctx, "tcp", destinationAddress)
				if err == nil {
					log.Info().Str("address", destinationAddress).Msg("Connected to address")
					conn = localConn
					break
				}

				if time.Since(startTime) > connectionTimeout {
					log.Fatal().Err(err).Msg("Timeout exceeded. Failed to connect to address.")
				}

				log.Warn().Err(err).Msg("Failed to connect to address. Retrying in 1 second")
				time.Sleep(time.Second)
			}

			defer conn.Close()

			var outputWriter io.Writer
			if outputFilePath != "" {
				outputFile, err := openFileFunc(outputFilePath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
				if err != nil {
					log.Fatal().Err(err).Msg("Failed to open output file")
				}
				defer outputFile.Close()
				outputWriter = outputFile
			} else {
				outputWriter = io.Discard
			}

			var inputReader io.Reader
			if inputFilePath != "" {
				inputFile, err := openFileFunc(inputFilePath, os.O_RDONLY, 0)
				if err != nil {
					log.Fatal().Err(err).Msg("Failed to open input file")
				}
				defer inputFile.Close()
				inputReader = inputFile
			} else {
				inputReader = bytes.NewBuffer([]byte{})
			}

			errorChan := make(chan error, 2)
			go func() {
				defer func() {
					conn.(*net.TCPConn).CloseWrite()
				}()
				defer close(errorChan)
				n, err := io.Copy(conn, inputReader)
				if err != nil {
					errorChan <- fmt.Errorf("failed to copy from input file to socket: %w", err)
				} else {
					log.Info().Int64("bytes", n).Msg("Finished copying from file to socket")
				}
			}()

			n, err := io.Copy(outputWriter, conn)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to copy from socket to output file")
			}

			log.Info().Int64("bytes", n).Msg("Finished copying from socket to file")

			err = <-errorChan
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to copy from input file to socket")
			}
		},
	}

	socketAdaptCommand.Flags().StringVarP(&destinationAddress, "address", "s", destinationAddress, "The address of the socket to connect to")
	socketAdaptCommand.MarkFlagRequired("address")
	socketAdaptCommand.Flags().StringVarP(&inputFilePath, "input", "i", inputFilePath, "The file to read from. If not specified, if not specified, there will be no data written to the socket.")
	socketAdaptCommand.Flags().StringVarP(&outputFilePath, "output", "o", outputFilePath, "The file write to. If not specified, data read from the socket will be discarded")
	socketAdaptCommand.Flags().StringVar(&connectionTimeoutString, "connection-timeout", connectionTimeoutString, "The timeout for connecting to the socket")

	rootCommand.AddCommand(socketAdaptCommand)

	commands := []*cobra.Command{intputCommand, writeCommand, relayInputCommand, relayOutputCommand, socketAdaptCommand}
	for _, command := range commands {
		command.Flags().StringVar(&namespace, "namespace", "", "The namespace of the pod to watch")
		command.Flags().StringVar(&podName, "pod", "", "The name of the pod to watch")
		command.Flags().StringVar(&containerName, "container", "", "The name of the container to watch")
		command.Flags().StringVar(&tombstoneFile, "tombstone", "", "The file that signals when the main container has exited")

		command.MarkFlagsRequiredTogether("namespace", "pod", "container")
		command.MarkFlagsMutuallyExclusive("tombstone", "namespace")
		command.MarkFlagsMutuallyExclusive("tombstone", "pod")
		command.MarkFlagsMutuallyExclusive("tombstone", "container")

		command.Long += `
While waiting to open the named pipe, this command will either watch the specified Kubernetes container for completion or will wait for the tombstone file to be created.
If it completes before the pipe is opened, the command will abandon opening the pipe and will treat the contents as empty.
The reason for this is to avoid hanging indefinitely if the container completes without touching the pipe.`

		rootCommand.AddCommand(command)
	}

	return rootCommand
}

func main() {
	err := newRootCommand().Execute()
	if err != nil {
		os.Exit(1)
	}
}
