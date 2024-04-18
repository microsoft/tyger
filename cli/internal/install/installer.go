package install

import (
	"context"
	"errors"
	"io"
)

var (
	ErrAlreadyLoggedError = errors.New("already logged error")
	ErrDependencyFailed   = errors.New("dependency failed")
)

var (
	// Set during build but we provide defaults so that there is some value when debugging.
	// We will need to update these from time to time. Alternatively, you can set the registry
	// values using the --set command-line argument.
	ContainerRegistry string = "tyger.azurecr.io"
	ContainerImageTag string = "v0.4.0-112-g428a5e8"
)

type Installer interface {
	QuickValidateConfig() bool

	InstallTyger(ctx context.Context) error
	UninstallTyger(ctx context.Context, deleteData bool) error

	ListDatabaseVersions(ctx context.Context, all bool) ([]DatabaseVersion, error)
	ApplyMigrations(ctx context.Context, targetVersion int, latest bool, offline bool, wait bool) error
	GetMigrationLogs(ctx context.Context, id int, destination io.Writer) error
}

type DatabaseVersion struct {
	Id          int    `json:"id"`
	Description string `json:"description"`
	State       string `json:"state"`
}
