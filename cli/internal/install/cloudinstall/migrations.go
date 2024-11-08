// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"sort"
	"strconv"
	"time"

	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

const (
	migrationRunnerLabelKey = "tyger-migration"
	commandHostLabelKey     = "tyger-command-host"
)

func runWithMinimalTygerInstallation[T any](ctx context.Context, inst *Installer, fn func(ctx context.Context, inst *Installer) (T, error)) (T, error) {
	var zeroT T
	installationRevision, err := inst.GetTygerInstallationRevision(ctx)
	if err != nil {
		return zeroT, err
	}

	if installationRevision == 0 {
		inst.Config.Api.Helm.Tyger.Values["onlyMigrationDependencies"] = true
		_, _, err = inst.InstallTygerHelmChart(ctx, false)
		if err != nil {
			return zeroT, err
		}
		inst.Config.Api.Helm.Tyger.Values["onlyMigrationDependencies"] = false

		defer func() {
			inst.UninstallTyger(ctx, false)
		}()
	}

	return fn(ctx, inst)
}

func (inst *Installer) ListDatabaseVersions(ctx context.Context, allVersions bool) ([]install.DatabaseVersion, error) {
	return runWithMinimalTygerInstallation(ctx, inst, func(ctx context.Context, inst *Installer) ([]install.DatabaseVersion, error) {
		job, err := inst.getMigrationRunnerJobDefinition(ctx)
		if err != nil {
			return nil, err
		}

		job.Name = fmt.Sprintf("tyger-command-host-%s", RandomAlphanumString(4))

		job.Spec.TTLSecondsAfterFinished = Ptr(int32(0))

		job.Spec.Template.Spec.Containers[0].Command = []string{"/app/bin/sleep", "5m"}
		job.Spec.Template.Spec.Containers[0].Args = []string{}

		if job.Spec.Template.Labels == nil {
			job.Spec.Template.Labels = map[string]string{}
		}
		job.Spec.Template.Labels[commandHostLabelKey] = "true"

		restConfig, err := inst.GetUserRESTConfig(ctx)
		if err != nil {
			return nil, err
		}

		clientset, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
		}

		log.Debug().Msg("Creating pod to list database versions")

		createdJob, err := clientset.BatchV1().Jobs(TygerNamespace).Create(ctx, job, v1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create pod: %w", err)
		}

		defer func() {
			clientset.BatchV1().Jobs(TygerNamespace).Delete(context.Background(), createdJob.Name, v1.DeleteOptions{
				PropagationPolicy: Ptr(v1.DeletePropagationBackground),
			})
		}()

		var pod *corev1.Pod

		err = wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
			podList, err := clientset.CoreV1().Pods(createdJob.Namespace).List(ctx, v1.ListOptions{
				LabelSelector: v1.FormatLabelSelector(createdJob.Spec.Selector),
			})

			if err != nil {
				return false, fmt.Errorf("failed to list migration runner pods: %w", err)
			}

			if len(podList.Items) == 0 {
				return false, nil
			}

			p, err := clientset.CoreV1().Pods(TygerNamespace).Get(ctx, podList.Items[0].Name, v1.GetOptions{})
			if err != nil {
				return false, err
			}

			switch p.Status.Phase {
			case corev1.PodFailed:
				return false, fmt.Errorf("pod failed: %s", p.Status.Message)
			case corev1.PodSucceeded:
				return false, fmt.Errorf("pod exited: %s", p.Status.Message)
			case corev1.PodRunning:
				pod = p
				return true, nil
			}

			log.Debug().Msgf("Waiting for pod to be ready. Current status: %s", p.Status.Phase)

			return false, nil
		})

		if err != nil {
			return nil, fmt.Errorf("failed to wait for pod to be ready: %w", err)
		}

		log.Debug().Msg("Invoking command in pod")

		return inst.getDatabaseVersionsFromPod(ctx, pod.Name, allVersions)
	})
}

func (inst *Installer) getDatabaseVersionsFromPod(ctx context.Context, podName string, allVersions bool) ([]install.DatabaseVersion, error) {
	stdout, stderr, err := inst.PodExec(ctx, podName, "/app/bin/tyger-server", "database", "list-versions")
	if err != nil {
		errorLog := ""
		if stderr != nil {
			errorLog = stderr.String()
		}

		return nil, fmt.Errorf("failed to exec into pod: %w. stderr: %s", err, errorLog)
	}

	versions := []install.DatabaseVersion{}

	if err := json.Unmarshal(stdout.Bytes(), &versions); err != nil {
		return nil, fmt.Errorf("failed to unmarshal versions: %w", err)
	}

	if !allVersions {
		// filter out the "complete" versions
		for i := len(versions) - 1; i >= 0; i-- {
			if versions[i].State == "complete" {
				versions = versions[i+1:]
				break
			}
		}
	}

	return versions, nil
}

func (inst *Installer) ApplyMigrations(ctx context.Context, targetVersion int, latest, offline, waitForCompletion bool) error {
	_, err := runWithMinimalTygerInstallation(ctx, inst, func(ctx context.Context, inst *Installer) (any, error) {
		versions, err := inst.ListDatabaseVersions(ctx, true)
		if err != nil {
			return nil, err
		}

		current := -1
		for i := len(versions) - 1; i >= 0; i-- {
			if versions[i].State == "complete" {
				current = versions[i].Id
				break
			}
		}

		if latest {
			targetVersion = versions[len(versions)-1].Id
			if current == targetVersion {
				log.Info().Msg("The database is already at the latest version")
				return nil, nil
			}
		} else {
			if targetVersion <= current {
				log.Info().Msgf("The database is already at version %d", targetVersion)
				return nil, nil
			}

			if targetVersion > versions[len(versions)-1].Id {
				return nil, fmt.Errorf("target version %d is greater than the latest version %d", targetVersion, versions[len(versions)-1].Id)
			}
		}

		if len(versions) == 0 {
			log.Info().Msg("No migrations to apply")
			return nil, nil
		}

		migrations := make([]int, 0)
		for i, v := range versions {
			if versions[i].Id > current && versions[i].Id <= targetVersion {
				migrations = append(migrations, v.Id)
			}
		}

		restConfig, err := inst.GetUserRESTConfig(ctx)
		if err != nil {
			return nil, err
		}

		clientset, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
		}

		existingMigrationJobs, err := clientset.BatchV1().Jobs(TygerNamespace).List(ctx, v1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", migrationRunnerLabelKey, "true"),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list jobs: %w", err)
		}

		for _, existingJob := range existingMigrationJobs.Items {
			if existingJob.Status.Succeeded == 0 && existingJob.Status.Failed == 0 {
				return nil, fmt.Errorf("an existing migration is already in progress")
			}
		}

		job, err := inst.getMigrationRunnerJobDefinition(ctx)
		if err != nil {
			return nil, err
		}

		jobName := fmt.Sprintf("tyger-migration-runner-%s", RandomAlphanumString(4))
		job.Name = jobName
		job.Spec.Template.Name = jobName

		if job.Labels == nil {
			job.Labels = map[string]string{}
		}
		job.Labels[migrationRunnerLabelKey] = "true"
		job.Spec.Template.Labels[migrationRunnerLabelKey] = "true"

		containers := make([]corev1.Container, len(migrations))

		for i, v := range migrations {
			container := job.Spec.Template.Spec.Containers[0]
			container.Args = []string{"database", "migrate", "--target-version", strconv.Itoa(v)}
			if offline {
				container.Args = append(container.Args, "--offline")
			}
			container.Name = fmt.Sprintf("migration-%d", v)
			containers[i] = container
		}

		job.Spec.Template.Spec.InitContainers = containers[:len(containers)-1]
		job.Spec.Template.Spec.Containers = containers[len(containers)-1:]

		log.Info().Msgf("Starting %d migrations...", len(migrations))

		_, err = clientset.BatchV1().Jobs(TygerNamespace).Create(ctx, job, v1.CreateOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to create job: %w", err)
		}

		if waitForCompletion {
			log.Info().Msg("Waiting for migrations to complete...")

			err = wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
				j, err := clientset.BatchV1().Jobs(TygerNamespace).Get(ctx, jobName, v1.GetOptions{})
				if err != nil {
					return false, err
				}

				if j.Status.Succeeded == 1 {
					return true, nil
				}

				if j.Status.Failed > 0 {
					return false, fmt.Errorf("migration failed")
				}

				return false, nil
			})

			if err != nil {
				return nil, fmt.Errorf("failed to wait for migrations to complete: %w", err)
			}

			log.Info().Msg("Migrations applied successfully")
		} else {
			log.Info().Msg("Migrations started successfully. Not waiting for them to complete.")
		}

		if targetVersion != versions[len(versions)-1].Id {
			log.Warn().Msg("There are more migrations available.")
		}

		return nil, nil
	})

	return err
}

func (inst *Installer) GetMigrationLogs(ctx context.Context, id int, destination io.Writer) error {
	_, err := runWithMinimalTygerInstallation(ctx, inst, func(ctx context.Context, inst *Installer) (any, error) {
		restConfig, err := inst.GetUserRESTConfig(ctx)
		if err != nil {
			return nil, err
		}

		clientset, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
		}

		coreV1 := clientset.CoreV1()
		pods, err := coreV1.Pods(TygerNamespace).List(ctx, v1.ListOptions{
			LabelSelector: fmt.Sprintf("%s=%s", migrationRunnerLabelKey, "true"),
		})
		if err != nil {
			return nil, fmt.Errorf("failed to list pods: %w", err)
		}

		sort.Slice(pods.Items, func(i, j int) bool {
			return !pods.Items[i].CreationTimestamp.Before(&pods.Items[j].CreationTimestamp)
		})

		for _, pod := range pods.Items {
			allContainers := make([]corev1.Container, 0, len(pod.Spec.InitContainers)+len(pod.Spec.Containers))
			allContainers = append(allContainers, pod.Spec.InitContainers...)
			allContainers = append(allContainers, pod.Spec.Containers...)

			for _, container := range allContainers {
				if container.Name == fmt.Sprintf("migration-%d", id) {
					logsRequest := coreV1.Pods(TygerNamespace).GetLogs(pod.Name, &corev1.PodLogOptions{
						Container: container.Name,
					})

					readCloser, err := logsRequest.Stream(ctx)
					if err != nil {
						log.Debug().Err(err).Msg("Failed to get logs stream")
						continue
					}

					defer readCloser.Close()
					io.Copy(destination, readCloser)
					return nil, nil
				}
			}
		}

		return nil, fmt.Errorf("logs for migration %d are not available", id)
	})

	return err
}

func RandomAlphanumString(n int) string {
	letters := []rune("abcdefghijklmnopqrstuvwxyz0123456789")
	b := make([]rune, n)
	for i := range b {
		nBig, err := rand.Int(rand.Reader, big.NewInt(int64(len(letters))))
		if err != nil {
			panic(err)
		}
		b[i] = letters[nBig.Int64()]
	}
	return string(b)
}
