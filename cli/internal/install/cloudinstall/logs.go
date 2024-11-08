// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"bytes"
	"context"
	"io"
	"time"

	v1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/watch"
	"k8s.io/client-go/kubernetes"
)

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
