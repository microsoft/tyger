package install

import (
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"math/big"
	"strconv"
	"time"

	"github.com/rs/zerolog/log"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/kubernetes"
)

type DatabaseVersion struct {
	Id          int    `json:"id"`
	Description string `json:"description"`
	State       string `json:"state"`
}

const (
	migrationRunnerLabelKey = "tyger-migration"
	commandHostLabelKey     = "tyger-command-host"
)

func ListDatabaseVersions(ctx context.Context, allVersions bool) ([]DatabaseVersion, error) {
	restConfig, err := getUserRESTConfig(ctx)
	if err != nil {
		return nil, err
	}

	job, err := getMigrationWorkerJobTemplate(ctx, restConfig)
	if err != nil {
		return nil, err
	}

	pod := &corev1.Pod{
		ObjectMeta: job.Spec.Template.ObjectMeta,
		Spec:       job.Spec.Template.Spec,
	}

	pod.Name = fmt.Sprintf("tyger-command-host-%s", RandomAlphanumString(4))
	pod.Spec.Containers[0].Command = []string{"/app/sleep", "5m"}
	pod.Spec.Containers[0].Args = []string{}

	if pod.Labels == nil {
		pod.Labels = map[string]string{}
	}
	pod.Labels[commandHostLabelKey] = "true"

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	log.Debug().Msg("Creating pod to list database versions")

	createdPod, err := clientset.CoreV1().Pods(TygerNamespace).Create(ctx, pod, v1.CreateOptions{})
	if err != nil {
		return nil, fmt.Errorf("failed to create pod: %w", err)
	}

	defer func() {
		clientset.CoreV1().Pods(TygerNamespace).Delete(context.Background(), createdPod.Name, v1.DeleteOptions{})
	}()

	err = wait.PollUntilContextCancel(ctx, 2*time.Second, true, func(ctx context.Context) (bool, error) {
		p, err := clientset.CoreV1().Pods(TygerNamespace).Get(ctx, createdPod.Name, v1.GetOptions{})
		if err != nil {
			return false, err
		}

		switch p.Status.Phase {
		case corev1.PodFailed:
			return false, fmt.Errorf("pod failed: %s", p.Status.Message)
		case corev1.PodSucceeded:
			return false, fmt.Errorf("pod exited: %s", p.Status.Message)
		case corev1.PodRunning:
			return true, nil
		}

		log.Debug().Msgf("Waiting for pod to be ready. Current status: %s", p.Status.Phase)

		return false, nil
	})

	if err != nil {
		return nil, fmt.Errorf("failed to wait for pod to be ready: %w", err)
	}

	log.Debug().Msg("Invoking command in pod")

	return getDatabaseVersionsFromPod(ctx, createdPod.Name, allVersions)
}

func getDatabaseVersionsFromPod(ctx context.Context, podName string, allVersions bool) ([]DatabaseVersion, error) {
	stdout, stderr, err := PodExec(ctx, podName, "/app/tyger.server", "database", "list-versions")
	if err != nil {
		errorLog := ""
		if stderr != nil {
			errorLog = stderr.String()
		}

		return nil, fmt.Errorf("failed to exec into pod: %w. stderr: %s", err, errorLog)
	}

	versions := []DatabaseVersion{}

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

func ApplyMigrations(ctx context.Context, targetVersion int, latest, waitForCompletion bool) error {
	versions, err := ListDatabaseVersions(ctx, true)
	if err != nil {
		return err
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
			return nil
		}
	} else {
		if targetVersion <= current {
			log.Info().Msgf("The database is already at version %d", targetVersion)
			return nil
		}

		if targetVersion > versions[len(versions)-1].Id {
			return fmt.Errorf("target version %d is greater than the latest version %d", targetVersion, versions[len(versions)-1].Id)
		}
	}

	if len(versions) == 0 {
		log.Info().Msg("No migrations to apply")
		return nil
	}

	migrations := make([]int, 0)
	for i, v := range versions {
		if versions[i].Id > current && versions[i].Id <= targetVersion {
			migrations = append(migrations, v.Id)
		}
	}

	restConfig, err := getUserRESTConfig(ctx)
	if err != nil {
		return err
	}

	job, err := getMigrationWorkerJobTemplate(ctx, restConfig)
	if err != nil {
		return err
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
		container.Name = fmt.Sprintf("migration-%d", v)
		containers[i] = container
	}

	job.Spec.Template.Spec.InitContainers = containers[:len(containers)-1]
	job.Spec.Template.Spec.Containers = containers[len(containers)-1:]

	log.Info().Msgf("Starting %d migrations...", len(migrations))

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	_, err = clientset.BatchV1().Jobs(TygerNamespace).Create(ctx, job, v1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create job: %w", err)
	}

	if waitForCompletion {
		log.Info().Msg("Waiting for migrations to complete")

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
			return fmt.Errorf("failed to wait for migrations to complete: %w", err)
		}
	}

	return nil
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
