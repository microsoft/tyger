// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"time"

	"github.com/microsoft/tyger/cli/internal/install"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/cli-runtime/pkg/genericiooptions"
	"k8s.io/client-go/discovery"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	kubectl "k8s.io/kubectl/pkg/cmd/logs"
	"k8s.io/kubectl/pkg/polymorphichelpers"
)

func (inst *Installer) GetServerLogs(ctx context.Context, options install.ServerLogOptions) error {
	restConfig, err := inst.GetUserRESTConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get REST config: %w", err)
	}

	clientSet, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	deployment, err := clientSet.AppsV1().Deployments(TygerNamespace).Get(ctx, "tyger-server", metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get Tyger deployment: %w", err)
	}

	logOptions := kubectl.NewLogsOptions(genericiooptions.IOStreams{Out: options.Destination, ErrOut: options.Destination})
	logOptions.RESTClientGetter = &restClientGetterImpl{restConfig}
	logOptions.Namespace = TygerNamespace
	logOptions.AllPodLogsForObject = polymorphichelpers.AllPodLogsForObjectFn
	logOptions.AllPods = true
	logOptions.ConsumeRequestFn = kubectl.DefaultConsumeRequest
	logOptions.Follow = options.Follow
	if options.TailLines > 0 {
		logOptions.Tail = int64(options.TailLines)
		logOptions.TailSpecified = true
	}

	if podLogOptions, err := logOptions.ToLogOptions(); err != nil {
		return fmt.Errorf("failed to convert log options: %w", err)
	} else {
		logOptions.Options = podLogOptions
	}

	logOptions.Object = deployment

	return logOptions.RunLogs()
}

// Implement the RESTClientGetter interface
type restClientGetterImpl struct {
	Config *rest.Config
}

func (r *restClientGetterImpl) ToRESTConfig() (*rest.Config, error) {
	return r.Config, nil
}

func (r *restClientGetterImpl) ToDiscoveryClient() (discovery.CachedDiscoveryInterface, error) {
	panic("not implemented")
}

func (r *restClientGetterImpl) ToRESTMapper() (meta.RESTMapper, error) {
	panic("not implemented")
}

func (r *restClientGetterImpl) ToRawKubeConfigLoader() clientcmd.ClientConfig {
	panic("not implemented")
}

func followPodsLogsUntilContextCanceled(ctx context.Context, clientset kubernetes.Interface, namespace string, labelSelector string) (map[string][]byte, error) {
	var (
		followedPods = make(map[string]*bytes.Buffer)
	)

	pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
		LabelSelector: labelSelector,
	})
	if err != nil {
		return nil, err
	}

	for _, pod := range pods.Items {
		b := &bytes.Buffer{}
		followedPods[pod.Name] = b
		go followPodLogs(ctx, clientset, namespace, pod.Name, b)
	}

	latestResourceVersion := pods.ResourceVersion
	watcher, err := clientset.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
		LabelSelector:   labelSelector,
		Watch:           true,
		ResourceVersion: latestResourceVersion,
	})
	if err != nil {
		return nil, err
	}
	defer watcher.Stop()

	for {
		select {
		case <-ctx.Done():
			returnMap := make(map[string][]byte)
			for k, v := range followedPods {
				returnMap[k] = v.Bytes()
			}
			return returnMap, nil
		case event, ok := <-watcher.ResultChan():
			if !ok {
				watcher, err = clientset.CoreV1().Pods(namespace).Watch(ctx, metav1.ListOptions{
					LabelSelector:   labelSelector,
					Watch:           true,
					ResourceVersion: latestResourceVersion,
				})
				if err != nil {
					return nil, err
				}
				continue
			}
			pod, ok := event.Object.(*v1.Pod)
			if !ok {
				continue
			}
			switch event.Type {
			case watch.Added:
				if _, ok := followedPods[pod.Name]; !ok {
					b := &bytes.Buffer{}
					followedPods[pod.Name] = b
					go followPodLogs(ctx, clientset, namespace, pod.Name, b)
				}
			case watch.Bookmark:
				latestResourceVersion = pod.ResourceVersion
			}
		}
	}
}

func followPodLogs(ctx context.Context, clientset kubernetes.Interface, namespace, podName string, writer io.Writer) {
	for {
		req := clientset.CoreV1().Pods(namespace).GetLogs(podName, &v1.PodLogOptions{
			Follow: true,
		})

		stream, err := req.Stream(ctx)
		if err == nil {
			n, _ := io.Copy(writer, stream)
			stream.Close()
			if n > 0 {
				return
			}
		}

		time.Sleep(1 * time.Second)
	}
}
