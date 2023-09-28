package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"time"

	"github.com/microsoft/tyger/cli/internal/cmd"
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

	readCommand := cmd.NewBufferReadCommand(func(filePath string, flag int, perm fs.FileMode) (*os.File, error) {
		return tryOpenFileUntilContainerExits(&namespace, &podName, &containerName, filePath, flag, perm)
	})

	writeCommand := cmd.NewBufferWriteCommand(func(filePath string, flag int, perm fs.FileMode) (*os.File, error) {
		return tryOpenFileUntilContainerExits(&namespace, &podName, &containerName, filePath, flag, perm)
	})

	commands := []*cobra.Command{readCommand, writeCommand}
	for _, command := range commands {
		command.Flags().StringVar(&namespace, "namespace", "", "The namespace of the pod to watch")
		command.MarkFlagRequired("namespace")
		command.Flags().StringVar(&podName, "pod", "", "The name of the pod to watch")
		command.MarkFlagRequired("pod")
		command.Flags().StringVar(&containerName, "container", "", "The name of the container to watch")
		command.MarkFlagRequired("container")

		command.Long += `
While waiting to open the named pipe, this command will watch the specified container for completion.
If it completes before the pipe is opened, the command will abandon opening the pipe and will treat the contents as empty.
The reason for this is to avoid hanging indefinitely if the container completes without touching the pipe.`

		rootCommand.AddCommand(command)
	}

	return rootCommand
}

func tryOpenFileUntilContainerExits(namespace, podName, containerName *string, filePath string, flag int, perm fs.FileMode) (*os.File, error) {
	// Create Kubernetes client
	config, err := rest.InClusterConfig()
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to load in-cluster Kubernetes config")
	}
	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to create Kubernetes clientset")
	}

	// Create cancellable contexts
	openFileCtx, openFileCancel := context.WithCancel(context.Background())
	watchCtx, watchCancel := context.WithCancel(context.Background())
	defer watchCancel()

	// begin watching for container completion
	go func() {
		err := watchUntilContainerCompletion(watchCtx, clientset, *namespace, *podName, *containerName)
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

	// try to open the file with the cancellable context
	inputFile, err := openFileWithCtx(openFileCtx, filePath, flag, perm)
	if err != nil {
		return nil, err
	}

	return inputFile, nil
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
