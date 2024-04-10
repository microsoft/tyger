package dockerinstall

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/client"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
)

const (
	migrationRangeLabel                  = "tyger-migration-range"
	migrationRunnerContainerSuffix       = "tyger-migration-runner"
	migrationListVersionsContainerSuffix = "tyger-migration-list-versions"
)

func (i *Installer) ListDatabaseVersions(ctx context.Context, allVersions bool) ([]install.DatabaseVersion, error) {
	containerName := i.resourceName("tyger-migration-list-versions")

	if err := i.startMigrationRunner(ctx, containerName, []string{"database", "list-versions"}, nil); err != nil {
		return nil, err
	}

	defer func() {
		if err := i.removeContainer(ctx, containerName); err != nil {
			log.Error().Err(err).Msg("Failed to delete container")
		}
	}()

	exitCode, err := i.waitForContainerToComplete(ctx, containerName)

	if err != nil {
		return nil, err
	}

	stdOut, stdErr := &bytes.Buffer{}, &bytes.Buffer{}
	if err := i.getContainerLogs(ctx, containerName, stdOut, stdErr); err != nil {
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

func (i *Installer) ApplyMigrations(ctx context.Context, targetVersion int, latest, offline, waitForCompletion bool) error {
	versions, err := i.ListDatabaseVersions(ctx, true)
	if err != nil {
		return err
	}

	current := 0
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

	containerName := i.resourceName(migrationRunnerContainerSuffix)
	args := []string{"database", "migrate", "--target-version", strconv.Itoa(targetVersion)}
	if offline {
		args = append(args, "--offline")
	}

	labels := map[string]string{
		migrationRangeLabel: fmt.Sprintf("%d-%d", current+1, targetVersion),
	}

	if err := i.startMigrationRunner(ctx, containerName, args, labels); err != nil {
		return err
	}

	if !waitForCompletion {
		log.Info().Msg("Migrations started successfully. Not waiting for them to complete.")
		return nil
	}

	log.Info().Msg("Waiting for migrations to complete...")

	exitCode, err := i.waitForContainerToComplete(ctx, containerName)
	if err != nil {
		return err
	}

	if exitCode != 0 {
		return fmt.Errorf("migration failed")
	}

	log.Info().Msg("Migrations applied successfully")
	return nil
}

func (i *Installer) GetMigrationLogs(ctx context.Context, id int, destination io.Writer) error {
	logsNotAvailableErr := fmt.Errorf("logs for migration %d are not available", id)
	migrationContainer, err := i.client.ContainerInspect(ctx, i.resourceName(migrationRunnerContainerSuffix))
	if err != nil {
		if client.IsErrNotFound(err) {
			return logsNotAvailableErr
		}

		return err
	}

	rangeString := migrationContainer.Config.Labels[migrationRangeLabel]
	var start, end int
	if _, err := fmt.Sscanf(rangeString, "%d-%d", &start, &end); err != nil {
		return logsNotAvailableErr
	}

	if id < start || id > end {
		return logsNotAvailableErr
	}

	logs := &bytes.Buffer{}
	if err := i.getContainerLogs(ctx, i.resourceName(migrationRunnerContainerSuffix), io.Discard, logs); err != nil {
		return logsNotAvailableErr
	}

	scanner := bufio.NewScanner(logs)
	for scanner.Scan() {
		var parsed map[string]any
		err := json.Unmarshal(scanner.Bytes(), &parsed)
		if err != nil {
			fmt.Fprintln(destination, scanner.Text())
			continue
		}

		scope := parsed["migrationVersionScope"]
		if scope == nil {
			fmt.Fprintln(destination, scanner.Text())
			continue
		}
		scopeId := int(scope.(float64))
		if scopeId < id {
			continue
		}
		if scopeId == id {
			fmt.Fprintln(destination, scanner.Text())
			continue
		}
		if scopeId > id {
			break
		}
	}

	return nil
}

func (i *Installer) startMigrationRunner(ctx context.Context, containerName string, args []string, labels map[string]string) error {
	containerSpec := containerSpec{
		ContainerConfig: &container.Config{
			Image: i.Config.ControlPlaneImage,
			User:  fmt.Sprintf("%d:%d", i.Config.GetUserIdInt(), i.Config.GetGroupIdInt()),
			Env: []string{
				fmt.Sprintf("Urls=http://unix:%s/control-plane/tyger.sock", i.Config.InstallationPath),
				fmt.Sprintf("Database__ConnectionString=Host=%s/database; Username=tyger-server", i.Config.InstallationPath),
				"Database__AutoMigrate=true",
				"Database__TygerServerRoleName=tyger-server",
				"Compute__Docker__Enabled=true",
			},
			Cmd:    args,
			Labels: labels,
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   "bind",
					Source: i.Config.InstallationPath,
					Target: i.Config.InstallationPath,
				},
			},
			NetworkMode: "none",
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyDisabled,
			},
		},
	}

	if err := i.createContainer(ctx, containerName, &containerSpec, false); err != nil {
		return fmt.Errorf("error creating migration runner container: %w", err)
	}

	if err := i.client.ContainerStart(ctx, containerName, container.StartOptions{}); err != nil {
		return err
	}

	return nil
}
