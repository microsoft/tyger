// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/microsoft/tyger/cli/internal/cmd"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/fields"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

var (
	// set during build
	version = ""
)

func main() {
	rootCmd := newRootCommand()
	if err := rootCmd.Execute(); err != nil {
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	rootCommand := cmd.NewCommonRootCommand(version)
	rootCommand.Use = "worker-waiter"
	rootCommand.Short = "Waits for worker pods to be ready and DNS to resolve"

	var labelSelector string
	var hostnames []string
	var namespace string
	var pollInterval time.Duration

	rootCommand.Flags().StringVar(&labelSelector, "label-selector", "", "The label selector for worker pods (e.g., tyger-worker=123)")
	rootCommand.Flags().StringSliceVar(&hostnames, "hostname", nil, "Hostnames to wait for DNS resolution (can be specified multiple times)")
	rootCommand.Flags().StringVar(&namespace, "namespace", "", "The Kubernetes namespace to watch pods in")
	rootCommand.Flags().DurationVar(&pollInterval, "poll-interval", 1*time.Second, "Interval between DNS resolution attempts")

	rootCommand.MarkFlagRequired("label-selector")
	rootCommand.MarkFlagRequired("namespace")

	rootCommand.RunE = func(cmd *cobra.Command, args []string) error {
		ctx, cancel := signal.NotifyContext(cmd.Context(), syscall.SIGINT, syscall.SIGTERM)
		defer cancel()

		return waitForWorkers(ctx, namespace, labelSelector, hostnames, pollInterval)
	}

	return rootCommand
}

func waitForWorkers(ctx context.Context, namespace, labelSelector string, hostnames []string, pollInterval time.Duration) error {
	log.Info().
		Str("namespace", namespace).
		Str("labelSelector", labelSelector).
		Strs("hostnames", hostnames).
		Msg("Starting worker waiter")

	// Wait for pods to be ready
	if err := waitForPodsReady(ctx, namespace, labelSelector, len(hostnames), pollInterval); err != nil {
		return fmt.Errorf("failed waiting for pods: %w", err)
	}

	// Wait for DNS resolution
	for _, hostname := range hostnames {
		if err := waitForDNS(ctx, hostname, pollInterval); err != nil {
			return fmt.Errorf("failed waiting for DNS resolution of %s: %w", hostname, err)
		}
	}

	log.Info().Msg("All workers ready and DNS resolved")
	return nil
}

func waitForPodsReady(ctx context.Context, namespace, labelSelector string, expectedCount int, pollInterval time.Duration) error {
	config, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("failed to get in-cluster config: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(config)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		pods, err := clientset.CoreV1().Pods(namespace).List(ctx, metav1.ListOptions{
			LabelSelector: labelSelector,
			FieldSelector: fields.Everything().String(),
		})
		if err != nil {
			log.Warn().Err(err).Msg("Error listing pods, retrying...")
			time.Sleep(pollInterval)
			continue
		}

		if len(pods.Items) < expectedCount {
			log.Info().
				Int("found", len(pods.Items)).
				Int("expected", expectedCount).
				Msg("Waiting for worker pods to be created...")
			time.Sleep(pollInterval)
			continue
		}

		readyCount := 0
		for _, pod := range pods.Items {
			for _, condition := range pod.Status.Conditions {
				if condition.Type == "Ready" && condition.Status == "True" {
					readyCount++
					break
				}
			}
		}

		if readyCount >= expectedCount {
			log.Info().Int("count", readyCount).Msg("All worker pods are ready")
			return nil
		}

		log.Info().
			Int("ready", readyCount).
			Int("expected", expectedCount).
			Msg("Waiting for worker pods to be ready")
		time.Sleep(pollInterval)
	}
}

func waitForDNS(ctx context.Context, hostname string, pollInterval time.Duration) error {
	log.Info().Str("hostname", hostname).Msg("Waiting for hostname to resolve")

	resolver := &net.Resolver{}

	for {
		if err := ctx.Err(); err != nil {
			return err
		}

		addrs, err := resolver.LookupHost(ctx, hostname)
		if err == nil && len(addrs) > 0 {
			log.Info().
				Str("hostname", hostname).
				Strs("addresses", addrs).
				Msg("Hostname resolved successfully")
			return nil
		}

		if err != nil {
			if log.Logger.GetLevel() <= zerolog.DebugLevel {
				log.Debug().Err(err).Str("hostname", hostname).Msg("DNS lookup failed, retrying...")
			}
		}

		time.Sleep(pollInterval)
	}
}
