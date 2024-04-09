package dockerinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"github.com/docker/docker/client"
	"github.com/microsoft/tyger/cli/internal/install"
)

func ListDatabaseVersions(ctx context.Context, allVersions bool) ([]install.DatabaseVersion, error) {
	config := GetDockerEnvironmentConfigFromContext(ctx)
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("error creating docker client: %w", err)
	}

	containerName := "tyger-migration-runner"

	if err := startMigrationRunner(ctx, dockerClient, config, containerName, []string{"database", "list-versions"}); err != nil {
		return nil, err
	}

	exitCode, err := waitForContainerToComplete(ctx, dockerClient, containerName)

	if err != nil {
		return nil, err
	}

	stdOut, stdErr := &bytes.Buffer{}, &bytes.Buffer{}
	if err := getContainerLogs(ctx, dockerClient, containerName, stdOut, stdErr); err != nil {
		return nil, fmt.Errorf("error getting container logs: %w", err)
	}

	if exitCode != 0 {
		return nil, fmt.Errorf("container exited with non-zero exit code: %d\n%s", exitCode, stdErr.String())
	}

	versions := []install.DatabaseVersion{}
	if err := json.Unmarshal(stdOut.Bytes(), &versions); err != nil {
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
