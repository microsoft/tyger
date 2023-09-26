package install

import (
	"errors"
	"strings"

	"github.com/rs/zerolog/log"
)

type ErrorSink struct {
	Errors []error
}

func (e *ErrorSink) Add(err error) {
	e.Errors = append(e.Errors, err)
}

func (e *ErrorSink) AsError() error {
	if len(e.Errors) == 0 {
		return nil
	}

	messages := make([]string, len(e.Errors))
	for i, err := range e.Errors {
		messages[i] = err.Error()
	}

	return errors.New(strings.Join(messages, "\n"))
}

func (e *ErrorSink) HasErrors() bool {
	return len(e.Errors) > 0
}

func (e *ErrorSink) LogFatalIfErrors() {
	if !e.HasErrors() {
		return
	}

	for _, err := range e.Errors {
		log.Error().Err(err).Send()
	}
}
