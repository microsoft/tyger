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
