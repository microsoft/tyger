package requestid

import (
	"context"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/internal/uniqueid"
	"github.com/rs/zerolog/log"
)

// Key to use when setting the request ID.
type ctxKeyRequestID int

// requestIDKey is the key that holds the unique request ID in a request context.
const requestIDKey ctxKeyRequestID = 0

// requestIDHeader is the name of the HTTP Header that contains the request id.
var requestIDHeader = "X-Request-Id"

// Creates an ID for the request. This will be stored in the forwarded context
// and is added as a context field to the logger.
func Middleware(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := uniqueid.NewId()
		w.Header().Add(requestIDHeader, id)

		logger := log.With().Str("requestId", id).Logger()

		ctx := r.Context()
		ctx = context.WithValue(ctx, requestIDKey, id)
		ctx = logger.WithContext(ctx)

		next.ServeHTTP(w, r.WithContext(ctx))
	})
}

// Gets the ID for the current request or empty if there is none.
func GetRequestId(ctx context.Context) string {
	id, _ := ctx.Value(requestIDKey).(string)
	return id
}
