package main

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"time"

	"github.com/fsnotify/fsnotify"
	"github.com/rs/zerolog/log"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

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
