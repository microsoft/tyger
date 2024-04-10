package dockerinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strconv"

	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
)

func (i *Installer) ListDatabaseVersions(ctx context.Context, allVersions bool) ([]install.DatabaseVersion, error) {
	containerName := "tyger-migration-list-versions"

	if err := i.startMigrationRunner(ctx, containerName, []string{"database", "list-versions"}); err != nil {
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

	containerName := "tyger-migration-runner"
	args := []string{"database", "migrate", "--target-version", strconv.Itoa(targetVersion)}
	if offline {
		args = append(args, "--offline")
	}

	if err := i.startMigrationRunner(ctx, containerName, args); err != nil {
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
	panic("not implemented")
}
