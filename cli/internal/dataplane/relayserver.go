// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/rs/zerolog/log"
)

const (
	errorCodeHeaderName = "x-ms-error-code"
)

func RelayInputServer(
	ctx context.Context,
	primaryListener net.Listener,
	secondaryListener net.Listener,
	bufferId string,
	outputWriter io.Writer,
	validateSignatureFunc ValidateSignatureFunc,
) error {
	addRoutes := func(r *chi.Mux, complete context.CancelFunc) {
		writing := atomic.Bool{}
		r.Put("/", func(w http.ResponseWriter, r *http.Request) {
			if err := ValidateSas(bufferId, SasActionCreate, r.URL.Query(), validateSignatureFunc); err != nil {
				switch err {
				case ErrInvalidSas:
					w.Header().Set(errorCodeHeaderName, "AuthenticationFailed")
					w.WriteHeader(http.StatusForbidden)
					return
				case ErrSasActionNotAllowed:
					w.Header().Set(errorCodeHeaderName, "AuthorizationPermissionMismatch")
					w.WriteHeader(http.StatusForbidden)
					return
				default:
					panic(fmt.Sprintf("unexpected error: %v", err))
				}
			}

			if writing.Swap(true) {
				log.Error().Msg("concurrent writes are not supported")
				w.WriteHeader(http.StatusBadRequest)
				return
			}

			defer complete()
			if outputWriter == io.Discard {
				log.Warn().Msg("Discarding input data")
				w.WriteHeader(http.StatusAccepted)
				return
			}
			err := copyToPipe(outputWriter, r.Body)
			if err != nil {
				log.Error().Err(err).Msg("transfer failed")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusAccepted)
		})
	}

	return relayServer(ctx, primaryListener, secondaryListener, addRoutes)
}

func RelayOutputServer(
	ctx context.Context,
	primaryListener net.Listener,
	secondaryListener net.Listener,
	containerId string,
	inputReaderChan <-chan io.ReadCloser,
	errorChan <-chan error,
	validateSignatureFunc ValidateSignatureFunc,
) error {
	addRoutes := func(r *chi.Mux, complete context.CancelFunc) {
		reading := atomic.Bool{}
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			if err := ValidateSas(containerId, SasActionRead, r.URL.Query(), validateSignatureFunc); err != nil {
				switch err {
				case ErrInvalidSas:
					w.Header().Set(errorCodeHeaderName, "AuthenticationFailed")
					w.WriteHeader(http.StatusForbidden)
					return
				case ErrSasActionNotAllowed:
					w.Header().Set(errorCodeHeaderName, "AuthorizationPermissionMismatch")
					w.WriteHeader(http.StatusForbidden)
					return
				default:
					panic(fmt.Sprintf("unexpected error: %v", err))
				}
			}

			var inputReader io.ReadCloser
			select {
			case inputReader = <-inputReaderChan:
			case err := <-errorChan:
				log.Error().Err(err).Msg("failed to open reader")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			defer inputReader.Close()
			if reading.Swap(true) {
				log.Error().Msg("concurrent reads are not supported")
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			defer complete()

			_, err := io.Copy(w, inputReader)
			if err != nil {
				log.Error().Err(err).Msg("transfer failed")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		})
	}

	return relayServer(ctx, primaryListener, secondaryListener, addRoutes)
}

func relayServer(ctx context.Context, primaryListener net.Listener, secondaryListener net.Listener, addHandlers func(mux *chi.Mux, transferComplete context.CancelFunc)) error {
	ctx, cancel := context.WithCancel(ctx)

	r := chi.NewRouter()
	r.Use(createRequestLoggerMiddleware())
	r.Head("/", func(w http.ResponseWriter, r *http.Request) {
		if secondaryListener != nil {
			w.Header().Set("x-ms-secondary-endpoint", fmt.Sprintf("http://%s", secondaryListener.Addr()))
		}
		w.WriteHeader(http.StatusOK)
	})

	addHandlers(r, cancel)

	server := &http.Server{
		Handler: r,
		BaseContext: func(l net.Listener) context.Context {
			return ctx
		},
	}

	errChan := make(chan error, 1)
	go func() {
		err := server.Serve(primaryListener)
		if err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

	if secondaryListener != nil {
		go func() {
			err := server.Serve(secondaryListener)
			if err != nil && err != http.ErrServerClosed {
				errChan <- err
			}
		}()
	}

	select {
	case <-ctx.Done():
		return server.Shutdown(context.Background())
	case err := <-errChan:
		return err
	}
}

func createRequestLoggerMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(rw http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(rw, r.ProtoMajor)
			start := time.Now().UTC()
			defer func() {
				log.Ctx(r.Context()).Info().
					Int("status", ww.Status()).
					Str("method", r.Method).
					Str("url", client.RedactUrl(r.URL).String()).
					Float32("latencyMs", float32(time.Since(start).Microseconds())/1000.0).
					Int("bytesWritten", ww.BytesWritten()).
					Msg("Request handled")
			}()

			next.ServeHTTP(ww, r)
		}
		return http.HandlerFunc(fn)
	}
}

type SasAction int

const (
	SasActionRead   SasAction = iota
	SasActionCreate SasAction = iota
)

const CurrentSasVersion = "0.1.0"

var (
	ErrInvalidSas          = errors.New("the SAS token is not valid")
	ErrSasActionNotAllowed = errors.New("the requested action is not permissed with the given SAS token")
)

func ValidateSas(containerId string, action SasAction, queryString url.Values, validateSignature ValidateSignatureFunc) error {
	var (
		sv       string
		sp       string
		st       string
		stParsed time.Time
		se       string
		seParsed time.Time
		sig      string
		sigBytes []byte
	)

	if sv = queryString.Get("sv"); sv != CurrentSasVersion {
		return ErrInvalidSas
	}

	if sp = queryString.Get("sp"); sp == "" {
		return ErrInvalidSas
	}

	if st = queryString.Get("st"); st == "" {
		return ErrInvalidSas
	}

	var err error
	if stParsed, err = time.Parse(time.RFC3339, st); err != nil {
		return ErrInvalidSas
	}

	if se = queryString.Get("se"); se == "" {
		return ErrInvalidSas
	}

	if seParsed, err = time.Parse(time.RFC3339, se); err != nil {
		return ErrInvalidSas
	}

	if sig = queryString.Get("sig"); sig == "" {
		return ErrInvalidSas
	}

	now := time.Now().UTC()

	if now.Before(stParsed) || now.After(seParsed) {
		return ErrInvalidSas
	}

	stringToSign := strings.Join([]string{
		sv,
		containerId,
		sp,
		st,
		se}, "\n")

	sigBytes, err = base64.StdEncoding.DecodeString(sig)
	if err != nil {
		return ErrInvalidSas
	}

	if !validateSignature([]byte(stringToSign), sigBytes) {
		return ErrInvalidSas
	}

	switch action {
	case SasActionRead:
		if !strings.Contains(sp, "r") {
			return ErrSasActionNotAllowed
		}
	case SasActionCreate:
		if !strings.Contains(sp, "c") {
			return ErrSasActionNotAllowed
		}
	default:
		panic(fmt.Sprintf("unknown action: %v", action))
	}

	return nil
}
