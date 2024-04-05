// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/microsoft/tyger/cli/internal/cmd"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
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

	readCommand := cmd.NewBufferReadCommand(openFileFunc)
	writeCommand := cmd.NewBufferWriteCommand(openFileFunc)

	relayCommand := &cobra.Command{
		Use: "relay",
	}

	rootCommand.AddCommand(relayCommand)

	listenAddress := ""
	outputFilePath := ""
	primarySigningPublicKeyPath := ""
	secondarySigningPublicKeyPath := ""
	bufferId := ""
	createListener := func() (net.Listener, error) {
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
		default:
			listener, err = net.Listen(u.Scheme, u.Host)
		}

		if err != nil {
			return nil, fmt.Errorf("failed to create listener: %w", err)
		}

		log.Info().Str("address", listenAddress).Msg("Listening for connections")
		return listener, nil
	}

	relayReadCommand := &cobra.Command{
		Use: "read",
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

				listener, err := createListener()
				if err != nil {
					return err
				}

				return dataplane.RelayReadServer(ctx, listener, bufferId, outputWriter, validateSignatureFunc)
			}

			if err := impl(); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}

	relayReadCommand.Flags().StringVarP(&outputFilePath, "output", "o", outputFilePath, "The file write to. If not specified, data is written to standard out.")
	relayReadCommand.Flags().StringVarP(&listenAddress, "listen", "l", listenAddress, "The address to listen on.")
	relayReadCommand.MarkFlagRequired("listen")
	relayReadCommand.Flags().StringVarP(&bufferId, "buffer", "b", bufferId, "The buffer ID")
	relayReadCommand.MarkFlagRequired("buffer")
	relayReadCommand.Flags().StringVarP(&primarySigningPublicKeyPath, "primary-public-signing-key", "p", primarySigningPublicKeyPath, "The path to the primary signing public key file")
	relayReadCommand.MarkFlagRequired("primary-cert")
	relayReadCommand.Flags().StringVarP(&secondarySigningPublicKeyPath, "secondary-public-signing-key", "s", secondarySigningPublicKeyPath, "The path to the secondary signing public key file")
	relayCommand.AddCommand(relayReadCommand)

	inputFilePath := ""
	relayWriteCommand := &cobra.Command{
		Use: "write",
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

				listener, err := createListener()
				if err != nil {
					return err
				}

				return dataplane.RelayWriteServer(ctx, listener, bufferId, readerChan, errorChan, validateSignatureFunc)
			}

			if err := impl(); err != nil {
				log.Fatal().Err(err).Send()
			}
		},
	}
	relayWriteCommand.Flags().StringVarP(&inputFilePath, "input", "i", inputFilePath, "The file to read from. If not specified, data is read from standard in.")
	relayWriteCommand.Flags().StringVarP(&listenAddress, "listen", "l", listenAddress, "The address to listen on.")
	relayWriteCommand.MarkFlagRequired("listen")
	relayWriteCommand.Flags().StringVarP(&bufferId, "buffer", "b", bufferId, "The buffer ID")
	relayWriteCommand.MarkFlagRequired("buffer")
	relayWriteCommand.Flags().StringVarP(&primarySigningPublicKeyPath, "primary-public-signing-key", "p", primarySigningPublicKeyPath, "The path to the primary signing public key file")
	relayWriteCommand.MarkFlagRequired("primary-cert")
	relayWriteCommand.Flags().StringVarP(&secondarySigningPublicKeyPath, "secondary-public-signing-key", "s", secondarySigningPublicKeyPath, "The path to the secondary signing public key file")

	relayCommand.AddCommand(relayWriteCommand)

	commands := []*cobra.Command{readCommand, writeCommand, relayReadCommand, relayWriteCommand}
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

func tryOpenFileUntilContainerExits(namespace, podName, containerName, tombstoneFilePath string, filePath string, flag int, perm fs.FileMode) (*os.File, error) {
	// Create cancellable contexts
	openFileCtx, openFileCancel := context.WithCancel(context.Background())
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()

	if tombstoneFilePath != "" {
		// begin watching for tombstone file
		go func() {
			err := watchUntilTombstoneFileCreated(watchCtx, tombstoneFilePath)
			if err != nil {
				if err == context.Canceled {
					return
				}
				log.Warn().Err(err).Msg("Tombstone file watcher failed with unexpected error.")
				return
			}

			log.Info().Msg("Tombstone file created.")
			openFileCancel()
		}()
	} else {
		// Create Kubernetes client
		config, err := rest.InClusterConfig()
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to load in-cluster Kubernetes config")
		}
		clientset, err := kubernetes.NewForConfig(config)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to create Kubernetes clientset")
		}

		// begin watching for container completion
		go func() {
			err := watchUntilContainerCompletion(watchCtx, clientset, namespace, podName, containerName)
			if err != nil {
				if err == context.Canceled {
					log.Debug().Msg("Container completion watcher canceled.")
					return
				}
				log.Warn().Err(err).Msg("Container completion watcher failed with unexpected error.")
				return
			}

			log.Info().Msg("Target container completed.")
			openFileCancel()
		}()
	}

	// try to open the file with the cancellable context
	inputFile, err := openFileWithCtx(openFileCtx, filePath, flag, perm)
	if err != nil {
		return nil, err
	}

	return inputFile, nil
}

func watchUntilTombstoneFileCreated(watchCtx context.Context, tombstoneFilePath string) error {
	// creates a new file watcher
	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		return fmt.Errorf("failed to create file watcher: %w", err)
	}
	defer watcher.Close()

	// watch the directory containing the tombstone file
	err = watcher.Add(path.Dir(tombstoneFilePath))
	if err != nil {
		return fmt.Errorf("failed to watch directory: %w", err)
	}

	if _, err := os.Stat(tombstoneFilePath); err == nil {
		return nil
	}

	for {
		select {
		case <-watchCtx.Done():
			return watchCtx.Err()
		case event, ok := <-watcher.Events:
			if !ok {
				return errors.New("file watcher closed")
			}
			if event.Op&fsnotify.Create == fsnotify.Create && event.Name == tombstoneFilePath {
				return nil
			}
		case err, ok := <-watcher.Errors:
			if !ok {
				return errors.New("file watcher closed")
			}
			return fmt.Errorf("file watcher error: %w", err)
		}
	}

}

func main() {
	err := newRootCommand().Execute()
	if err != nil {
		os.Exit(1)
	}
}

func watchUntilContainerCompletion(ctx context.Context, clientset *kubernetes.Clientset, namespace, podName, containerName string) error {
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		default:
			watcher, err := clientset.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
				FieldSelector: fmt.Sprintf("metadata.name=%s", podName),
			})
			if err != nil {
				if errors.Is(err, context.Canceled) && ctx.Err() == context.Canceled {
					return ctx.Err()
				}
				log.Warn().Err(err).Msg("Failed to create pod watcher. Restarting...")
				goto SleepAndRestartWatch
			}

			for {
				select {
				case <-ctx.Done():
					watcher.Stop()
					return ctx.Err()
				case event, ok := <-watcher.ResultChan():
					if !ok {
						log.Warn().Msg("Pod watcher closed. Restarting...")
						goto SleepAndRestartWatch
					}
					switch event.Type {
					case watch.Modified, watch.Added:
						pod := event.Object.(*corev1.Pod)

						for _, containerStatus := range pod.Status.ContainerStatuses {
							if containerStatus.Name == containerName {
								if containerStatus.State.Terminated != nil {
									return nil
								}
							}
						}
					case watch.Error:
						log.Warn().Any("object", event.Object).Msg("Pod watcher error. Restarting...")
						goto SleepAndRestartWatch
					default:
						log.Warn().Str("type", string(event.Type)).Msg("Unexpected pod watcher event. Ignoring...")
					}
				}

			}

		SleepAndRestartWatch:
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(5 * time.Second):
				continue
			}
		}
	}
}

func openFileWithCtx(ctx context.Context, path string, flag int, perm fs.FileMode) (*os.File, error) {
	resultChan := make(chan *os.File, 1)
	errChan := make(chan error, 1)

	go func() {
		file, err := os.OpenFile(path, flag, perm)
		if err != nil {
			errChan <- err
			return
		}
		resultChan <- file
	}()

	select {
	case <-ctx.Done():
		return nil, ctx.Err()
	case file := <-resultChan:
		return file, nil
	case err := <-errChan:
		return nil, err
	}
}
