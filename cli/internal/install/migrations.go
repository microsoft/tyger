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

func ListDatabaseVersions(ctx context.Context) ([]DatabaseVersion, error) {
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

	pod.Name = fmt.Sprintf("tyger-command-host-%s", randAlphanum(4))
	pod.Spec.Containers[0].Args = []string{"sleep"}

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
		clientset.CoreV1().Pods(TygerNamespace).Delete(ctx, createdPod.Name, v1.DeleteOptions{})
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

	stdout, stderr, err := PodExec(ctx, createdPod.Name, "/app/tyger.server", "list-versions")
	if err != nil {
		errorLog := ""
		if stderr != nil {
			errorLog = stderr.String()
		}

		return nil, fmt.Errorf("failed to exec into pod: %w. logs: %s", err, errorLog)
	}

	versions := []DatabaseVersion{}

	if err := json.Unmarshal(stdout.Bytes(), &versions); err != nil {
		return nil, fmt.Errorf("failed to unmarshal versions: %w", err)
	}

	return versions, nil
}

func ApplyMigrations(ctx context.Context, targetVersion int, latest, waitForCompletion bool) error {
	versions, err := ListDatabaseVersions(ctx)
	if err != nil {
		return err
	}

	first := versions[0]
	if first.Id == targetVersion && first.State == "complete" {
		log.Info().Msgf("The database is already at version %d", first.Id)
	}

	if first.State == "complete" {
		versions = versions[1:]
	}

	if targetVersion != 0 {
		found := false
		for i, v := range versions {
			if v.Id == targetVersion {
				found = false
				versions = versions[:i]
			}
		}

		if !found {
			return fmt.Errorf("target version %d not found", targetVersion)
		}
	}

	if len(versions) == 0 {
		log.Info().Msg("No migrations to apply")
		return nil
	}

	migrations := make([]int, len(versions))
	for i, v := range versions {
		migrations[i] = v.Id
	}

	restConfig, err := getUserRESTConfig(ctx)
	if err != nil {
		return err
	}

	job, err := getMigrationWorkerJobTemplate(ctx, restConfig)
	if err != nil {
		return err
	}

	jobName := fmt.Sprintf("tyger-migration-runner-%s", randAlphanum(4))
	job.Name = jobName
	job.Spec.Template.Name = jobName

	containers := make([]corev1.Container, len(migrations))

	for i, v := range migrations {
		container := job.Spec.Template.Spec.Containers[0]
		container.Args = []string{"migrate", "--target-version", strconv.Itoa(v)}
		container.Name = fmt.Sprintf("migration-%d", v)
		containers[i] = container
	}

	job.Spec.Template.Spec.InitContainers = containers[:len(containers)-1]
	job.Spec.Template.Spec.Containers = containers[len(containers)-1:]

	log.Info().Msgf("Starting migrations...")

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

func randAlphanum(n int) string {
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
