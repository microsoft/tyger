package dataplane

import (
	"context"
	"io"
	"net"
	"net/http"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/rs/zerolog/log"
)

func RelayReadServer(ctx context.Context, listener net.Listener, containerId string, outputWriter io.Writer) error {
	addRoutes := func(r *chi.Mux, complete context.CancelFunc) {
		r.Put("/", func(w http.ResponseWriter, r *http.Request) {
			defer complete()
			if outputWriter == io.Discard {
				log.Warn().Msg("Discarding input data")
				w.WriteHeader(http.StatusAccepted)
				return
			}

			_, err := io.Copy(outputWriter, r.Body)
			if err != nil {
				log.Error().Err(err).Msg("transfer failed")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			w.WriteHeader(http.StatusAccepted)
		})
	}

	return relayServer(ctx, listener, addRoutes)
}

func RelayWriteServer(ctx context.Context, listener net.Listener, containerId string, inputReaderChan <-chan io.ReadCloser, errorChan <-chan error) error {
	addRoutes := func(r *chi.Mux, complete context.CancelFunc) {
		r.Get("/", func(w http.ResponseWriter, r *http.Request) {
			var inputReader io.ReadCloser
			select {
			case inputReader = <-inputReaderChan:
			case err := <-errorChan:
				log.Error().Err(err).Msg("failed to open reader")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			defer inputReader.Close()
			defer complete()

			_, err := io.Copy(w, inputReader)
			if err != nil {
				log.Error().Err(err).Msg("transfer failed")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}
		})
	}

	return relayServer(ctx, listener, addRoutes)
}

func relayServer(ctx context.Context, listener net.Listener, addHandlers func(mux *chi.Mux, transferComplete context.CancelFunc)) error {
	ctx, cancel := context.WithCancel(ctx)

	r := chi.NewRouter()
	r.Use(createRequestLoggerMiddleware())
	r.Head("/", func(w http.ResponseWriter, r *http.Request) {
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
		err := server.Serve(listener)
		if err != nil && err != http.ErrServerClosed {
			errChan <- err
		}
	}()

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
