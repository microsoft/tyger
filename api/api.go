package api

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"dev.azure.com/msresearch/compimag/_git/tyger/internal/buffers"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/config"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/database"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/k8s"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/requestid"
	oapimiddleware "github.com/deepmap/oapi-codegen/pkg/chi-middleware"
	"github.com/etherlabsio/healthcheck"
	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Api struct {
	baseUri       string
	repository    database.Repository
	bufferManager buffers.BufferManager
	k8sManager    k8s.K8sManager
}

// make sure we conform to ServerInterface
var _ ServerInterface = (*Api)(nil)

func NewApi(baseUri string, repository database.Repository, bufferManager buffers.BufferManager, k8sManager k8s.K8sManager) *Api {
	return &Api{
		baseUri:       baseUri,
		repository:    repository,
		bufferManager: bufferManager,
		k8sManager:    k8sManager,
	}
}

func BuildRouter(config config.ConfigSpec, repository database.Repository, bufferManager buffers.BufferManager, k8sManager k8s.K8sManager) (http.Handler, error) {

	api := NewApi(config.BaseUri, repository, bufferManager, k8sManager)

	r := chi.NewRouter()

	// Middlewares to apply to all chi routes.
	// Note that at the end of this function, we add middlewares that apply
	// before chi.

	r.Use(
		requestid.Middleware,
		createLoggerMiddleware(log.Logger),
		middleware.Recoverer,
	)

	swagger, err := GetSwagger()
	if err != nil {
		return nil, fmt.Errorf("error loading swagger spec: %v", err)
	}

	// We only want to run this validator on routes that are part of the swagger specification
	validatorMiddleware := oapimiddleware.OapiRequestValidator(swagger)

	handlerOptions := ChiServerOptions{
		BaseRouter: r,
		Middlewares: []MiddlewareFunc{
			func(next http.HandlerFunc) http.HandlerFunc {
				return validatorMiddleware(next).ServeHTTP
			}},
	}

	HandlerWithOptions(api, handlerOptions)

	r.Handle("/healthcheck", healthcheck.Handler(
		healthcheck.WithChecker("Dabase", healthcheck.CheckerFunc(repository.HealthCheck)),
		healthcheck.WithChecker("Buffers", healthcheck.CheckerFunc(bufferManager.HealthCheck)),
		healthcheck.WithChecker("Kubernetes", healthcheck.CheckerFunc(k8sManager.HealthCheck)),
		healthcheck.WithChecker("Storage", healthcheck.CheckerFunc(func(ctx context.Context) error {
			// TODO: create a healthcheck endpoint on the storage server
			resp, err := http.Get(fmt.Sprintf("%s/v1/blobs?subject=0000000000000000000000000000000000000000&_limit=1", config.MrdStorageUri))
			if err != nil {
				log.Ctx(ctx).Err(err).Msg("storage server health check failed")
				return errors.New("unable to connect to storage server")
			}
			if resp.StatusCode != http.StatusOK {
				log.Ctx(ctx).Err(fmt.Errorf("storage server response: %d", resp.StatusCode)).Msg("storage server health check failed")
				return errors.New("unable to connect to storage server")
			}
			return nil
		}))))

	r.NotFound(api.HandleRouteNotFound)

	// wrap with middleware to apply before chi.
	handler := stripSlashesMiddleware(r)

	return handler, nil
}

func (api *Api) HandleRouteNotFound(w http.ResponseWriter, r *http.Request) {
	writeError(w, http.StatusBadRequest, "RouteNotFound", "The provided URI pattern is not recognized.")
}

func writeInternalServerError(w http.ResponseWriter, r *http.Request, err error) {
	log.Ctx(r.Context()).Err(err).Send()
	writeError(w, http.StatusInternalServerError, "InternalServerError", "An internal error has occured.")
}

func writeNotFound(w http.ResponseWriter) {
	writeError(w, http.StatusNotFound, "NotFound", "Resource not found")
}

func writeError(w http.ResponseWriter, statusCode int, errorCode, message string) {
	writeJson(w, statusCode, Error{ErrorInfo{errorCode, message}})
}

func writeJson(w http.ResponseWriter, statusCode int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(statusCode)

	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)

	if err := enc.Encode(v); err != nil {
		http.Error(w, err.Error(), http.StatusInternalServerError)
	}
}

func stripSlashesMiddleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		r.URL.Path = strings.TrimSuffix(r.URL.Path, "/")
		next.ServeHTTP(w, r)
	})
}

func createLoggerMiddleware(logger zerolog.Logger) func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(rw http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(rw, r.ProtoMajor)
			start := time.Now().UTC()
			defer func() {
				log.Ctx(r.Context()).Info().
					Int("status", ww.Status()).
					Int("bytes", ww.BytesWritten()).
					Str("method", r.Method).
					Str("path", r.URL.Path).
					Str("query", r.URL.RawQuery).
					Str("ip", r.RemoteAddr).
					Str("user-agent", r.UserAgent()).
					Dur("latency", time.Since(start)).
					Msg("request completed")
			}()

			next.ServeHTTP(ww, r)
		}
		return http.HandlerFunc(fn)
	}
}
