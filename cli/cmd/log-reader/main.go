package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/microsoft/tyger/cli/internal/cmd"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	// set during build
	version = ""
	newline = []byte{'\n'}
	space   = []byte{' '}
)

func createRequestLoggerMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(rw http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(rw, r.ProtoMajor)
			start := time.Now().UTC()
			defer func() {
				log.Ctx(r.Context()).Info().
					Int("status", ww.Status()).
					Str("method", r.Method).
					Str("url", r.URL.String()).
					Float32("latencyMs", float32(time.Since(start).Microseconds())/1000.0).
					Int("bytesWritten", ww.BytesWritten()).
					Msg("Request handled")
			}()

			next.ServeHTTP(ww, r)
		}
		return http.HandlerFunc(fn)
	}
}

func run(ctx context.Context) error {
	l, err := net.Listen("tcp", ":7000")
	if err != nil {
		return fmt.Errorf("failed to listen: %w", err)
	}
	defer l.Close()

	log.Info().Msg("Listening on " + l.Addr().String())

	r := chi.NewRouter()
	r.Use(createRequestLoggerMiddleware())
	r.Get("/logs", func(w http.ResponseWriter, r *http.Request) {
		query := r.URL.Query()
		container := query.Get("container")

		logDir := fmt.Sprintf("/logs/%s", container)

		entries, err := os.ReadDir(logDir)
		if err != nil {
			log.Error().Err(err).Msg("failed to read directory")
			w.WriteHeader(http.StatusInternalServerError)
			return
		}

		slices.SortFunc(entries, func(a, b os.DirEntry) int {
			aNum, _ := strconv.Atoi(strings.Split(a.Name(), ".")[0])
			bNum, _ := strconv.Atoi(strings.Split(b.Name(), ".")[0])
			if aNum == bNum {
				return strings.Compare(b.Name(), a.Name())
			}

			return bNum - aNum
		})

		started := false

		var lastRestartNum *int

		for _, entry := range entries {
			if entry.IsDir() {
				continue
			}

			restartNum, err := strconv.Atoi(strings.Split(entry.Name(), ".")[0])
			if err != nil {
				log.Error().Err(err).Msgf("failed to parse file name %q", entry.Name())
				w.WriteHeader(http.StatusInternalServerError)
			}

			if lastRestartNum != nil && restartNum != *lastRestartNum {
				return
			}

			lastRestartNum = &restartNum

			f, err := os.Open(filepath.Join(logDir, entry.Name()))
			if err != nil {
				log.Error().Err(err).Msg("failed to open file")
				w.WriteHeader(http.StatusInternalServerError)
				return
			}

			if !started {
				started = true
				w.WriteHeader(http.StatusOK)
			}

			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := scanner.Text()
				spaceIndex := strings.IndexByte(line, ' ')
				if spaceIndex != 30 {
					ts, err := time.Parse(time.RFC3339Nano, line[:spaceIndex])
					if err != nil {
						log.Error().Err(err).Msg("failed to parse timestamp")
						return
					}

					w.Write([]byte(ts.Format("2006-01-02T15:04:05.000000000Z07:00")))
					w.Write(space)
				} else {
					w.Write([]byte(line[:spaceIndex+1]))
				}

				line = line[spaceIndex+1:]
				spaceIndex = strings.IndexByte(line, ' ')

				remain := line[spaceIndex+1:]
				w.Write([]byte(remain))
				w.Write(newline)
			}

			if err := scanner.Err(); err != nil {
				log.Error().Err(err).Msg("error reading file")
				return
			}

			f.Close()
		}
	})

	server := &http.Server{
		Handler: r,
		BaseContext: func(l net.Listener) context.Context {
			return ctx
		},
	}

	go func() {
		<-ctx.Done()
		log.Info().Msg("Shutting down server...")
		_ = server.Shutdown(ctx)
	}()

	return server.Serve(l)
}

func main() {
	rootCommand := cmd.NewCommonRootCommand(version)
	rootCommand.Use = "log-reader"

	rootCommand.Run = func(cmd *cobra.Command, args []string) {
		ctx := cmd.Context()
		var stopFunc context.CancelFunc
		ctx, stopFunc = signal.NotifyContext(ctx, os.Interrupt, syscall.SIGTERM)

		go func() {
			<-ctx.Done()
			stopFunc()
			log.Warn().Msg("Canceling...")
		}()

		if err := run(ctx); err != nil {
			log.Fatal().Err(err).Send()
		}
	}

	err := rootCommand.Execute()
	if err != nil {
		os.Exit(1)
	}
}
