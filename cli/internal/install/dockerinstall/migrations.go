// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dockerinstall

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
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

func (inst *Installer) ListDatabaseVersions(ctx context.Context, allVersions bool) ([]install.DatabaseVersion, error) {
	if err := inst.createDatabaseContainer(ctx); err != nil {
		return nil, fmt.Errorf("error creating database container: %w", err)
	}

	containerName := inst.resourceName("tyger-migration-list-versions")

	if err := inst.startMigrationRunner(ctx, containerName, []string{"database", "list-versions"}, nil); err != nil {
		return nil, err
	}

	defer func() {
		if err := inst.removeContainer(ctx, containerName); err != nil {
			log.Error().Err(err).Msg("Failed to delete container")
		}
	}()

	exitCode, err := inst.waitForContainerToComplete(ctx, containerName)

	if err != nil {
		return nil, err
	}

	stdOut, stdErr := &bytes.Buffer{}, &bytes.Buffer{}
	if err := inst.getContainerLogs(ctx, containerName, false, -1, stdOut, stdErr); err != nil {
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

func (inst *Installer) ApplyMigrations(ctx context.Context, targetVersion int, latest, offline, waitForCompletion bool) error {
	if err := inst.createDatabaseContainer(ctx); err != nil {
		return fmt.Errorf("error creating database container: %w", err)
	}

	versions, err := inst.ListDatabaseVersions(ctx, true)
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

	containerName := inst.resourceName(migrationRunnerContainerSuffix)
	args := []string{"database", "migrate", "--target-version", strconv.Itoa(targetVersion)}
	if offline {
		args = append(args, "--offline")
	}

	labels := map[string]string{
		migrationRangeLabel: fmt.Sprintf("%d-%d", current+1, targetVersion),
	}

	if err := inst.startMigrationRunner(ctx, containerName, args, labels); err != nil {
		return err
	}

	if !waitForCompletion {
		log.Info().Msg("Migrations started successfully. Not waiting for them to complete.")
		return nil
	}

	log.Info().Msg("Waiting for migrations to complete...")

	exitCode, err := inst.waitForContainerToComplete(ctx, containerName)
	if err != nil {
		return err
	}

	if exitCode != 0 {
		return fmt.Errorf("migration failed")
	}

	log.Info().Msg("Migrations applied successfully")
	return nil
}

func (inst *Installer) GetMigrationLogs(ctx context.Context, id int, destination io.Writer) error {
	if err := inst.createDatabaseContainer(ctx); err != nil {
		return fmt.Errorf("error creating database container: %w", err)
	}

	logsNotAvailableErr := fmt.Errorf("logs for migration %d are not available", id)
	migrationContainer, err := inst.client.ContainerInspect(ctx, inst.resourceName(migrationRunnerContainerSuffix))
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
	if err := inst.getContainerLogs(ctx, inst.resourceName(migrationRunnerContainerSuffix), false, -1, io.Discard, logs); err != nil {
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

func (inst *Installer) initializeDatabase(ctx context.Context) error {
	containerName := inst.resourceName("database-init")
	args := []string{"database", "init"}
	if inst.Config.InitialDatabaseVersion != nil {
		args = append(args, "--target-version", strconv.Itoa(*inst.Config.InitialDatabaseVersion))
	}

	if err := inst.startMigrationRunner(ctx, containerName, args, nil); err != nil {
		return fmt.Errorf("error starting running migration runner: %w", err)
	}

	defer func() {
		if err := inst.removeContainer(ctx, containerName); err != nil {
			log.Error().Err(err).Msg("error removing migration runner container")
		}
	}()

	exitCode, err := inst.waitForContainerToComplete(ctx, containerName)
	if err != nil {
		return fmt.Errorf("error waiting for migration runner to complete: %w", err)
	}

	stdOut, stdErr := &bytes.Buffer{}, &bytes.Buffer{}
	if err := inst.getContainerLogs(ctx, containerName, false, -1, stdOut, stdErr); err != nil {
		return fmt.Errorf("error getting container logs: %w", err)
	}

	parsedLines, err := install.ParseJsonLogs(stdErr.Bytes())
	if err != nil {
		return fmt.Errorf("error parsing logs: %w", err)
	}

	found := false
	for _, parsedLine := range parsedLines {
		if category, ok := parsedLine["category"].(string); ok {

			if category == "Tyger.ControlPlane.Database.Migrations.MigrationRunner[DatabaseMigrationRequired]" {
				if message, ok := parsedLine["message"].(string); ok {
					log.Error().Msg(message)
				}
				if args, ok := parsedLine["args"].(map[string]any); ok {
					if version, ok := args["requiredVersion"].(float64); ok {
						log.Error().Msgf("Run `tyger api uninstall -f <CONFIG_PATH>` followed by `tyger api migrations apply --target-version %d --offline --wait -f <CONFIG_PATH>` followed by `tyger api install -f <CONFIG_PATH>` ", int(version))
						return install.ErrAlreadyLoggedError
					}
				}
			}

			if category == "Tyger.ControlPlane.Database.Migrations.MigrationRunner[NewerDatabaseVersionsExist]" {
				log.Warn().Msgf("The database schema should be upgraded. Run `tyger api migrations list` to see the available migrations and `tyger api migrations apply` to apply them.")
				found = true
				break
			}

			if category == "Tyger.ControlPlane.Database.Migrations.MigrationRunner[UsingMostRecentDatabaseVersion]" {
				log.Debug().Msg("Database schema is up to date")
				found = true
				break
			}
		}
	}

	if exitCode == 0 {
		if !found {
			return errors.New("failed to find expected migration log message")
		}
		return nil
	}

	return fmt.Errorf("migration runner failed with exit code %d: %s", exitCode, stdErr.String())
}

func (inst *Installer) startMigrationRunner(ctx context.Context, containerName string, args []string, labels map[string]string) error {
	translatedInstallationPath := inst.translateToHostPath(inst.Config.InstallationPath)

	containerSpec := containerSpec{
		ContainerConfig: &container.Config{
			Image: inst.Config.ControlPlaneImage,
			User:  fmt.Sprintf("%d:%d", inst.Config.GetUserIdInt(), inst.Config.GetGroupIdInt()),
			Env: []string{
				fmt.Sprintf("Urls=http://unix:%s/control-plane/tyger.sock", inst.Config.InstallationPath),
				fmt.Sprintf("Database__Host=%s/database", inst.Config.InstallationPath),
				"Database__Username=tyger-server",
				"Database__AutoMigrate=true",
				"Database__TygerServerRoleName=tyger-server",
				"Compute__Docker__Enabled=true",
				fmt.Sprintf("Buffers__LocalStorage__DataPlaneEndpoint=http+unix://%s/data-plane/tyger.data.sock", inst.Config.InstallationPath),
				fmt.Sprintf("Buffers__LocalStorage__TcpDataPlaneEndpoint=http://localhost:%d", inst.Config.DataPlanePort),
			},
			Cmd:    args,
			Labels: labels,
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   "bind",
					Source: translatedInstallationPath,
					Target: inst.Config.InstallationPath,
				},
			},
			NetworkMode: "none",
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyDisabled,
			},
		},
	}

	if err := inst.createContainer(ctx, containerName, &containerSpec, false); err != nil {
		return fmt.Errorf("error creating migration runner container: %w", err)
	}

	if err := inst.client.ContainerStart(ctx, containerName, container.StartOptions{}); err != nil {
		return err
	}

	return nil
}
