// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"
	"errors"
	"io"
	"strings"
	"sync"
)

var (
	ErrAlreadyLoggedError = errors.New("already logged error")
	ErrDependencyFailed   = errors.New("dependency failed")
)

var (
	// Set during build
	ContainerRegistry          string = ""
	ContainerRegistryDirectory string = ""
	ContainerImageTag          string = ""

	GetNormalizedContainerRegistryDirectory = sync.OnceValue(func() string {
		normalized := ContainerRegistryDirectory
		if !strings.HasPrefix(normalized, "/") {
			normalized = "/" + normalized
		}
		if !strings.HasSuffix(normalized, "/") {
			normalized = normalized + "/"
		}
		return normalized
	})
)

type Installer interface {
	QuickValidateConfig() bool

	InstallTyger(ctx context.Context) error
	UninstallTyger(ctx context.Context, deleteData bool, preserveRunContainers bool) error

	GetServerLogs(ctx context.Context, options ServerLogOptions) error

	ListDatabaseVersions(ctx context.Context, all bool) ([]DatabaseVersion, error)
	ApplyMigrations(ctx context.Context, targetVersion int, latest bool, offline bool, wait bool) error
	GetMigrationLogs(ctx context.Context, id int, destination io.Writer) error
}

type DatabaseVersion struct {
	Id          int    `json:"id"`
	Description string `json:"description"`
	State       string `json:"state"`
}

type ServerLogOptions struct {
	Follow      bool
	TailLines   int
	DataPlane   bool
	Destination io.Writer
}
