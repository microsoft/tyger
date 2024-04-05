package install

import "errors"

var (
	ErrAlreadyLoggedError = errors.New("already logged error")
	ErrDependencyFailed   = errors.New("dependency failed")
)
