package requestid

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"

	"github.com/stretchr/testify/require"
)

func TestAddsHeader(t *testing.T) {
	require := require.New(t)
	nextCalled := false
	nextHandler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		nextCalled = true
		require.NotEmpty(GetRequestId(r.Context()))
	})

	handler := Middleware(nextHandler)

	recorder := httptest.NewRecorder()
	handler.ServeHTTP(recorder, httptest.NewRequest("GET", "http://abc", nil))
	require.True(nextCalled)
	require.NotEmpty(recorder.Result().Header.Get(requestIDHeader))
}

func TestAddsRequestIdToLogEntries(t *testing.T) {
	require := require.New(t)
	existingLogger := log.Logger
	defer func() { log.Logger = existingLogger }()

	buf := bytes.Buffer{}
	log.Logger = zerolog.New(&buf)

	nextHandler := http.HandlerFunc(func(rw http.ResponseWriter, r *http.Request) {
		log.Ctx(r.Context()).Info().Msg("hello")
	})

	handler := Middleware(nextHandler)
	handler.ServeHTTP(httptest.NewRecorder(), httptest.NewRequest("GET", "http://abc", nil))

	require.NotEmpty(buf.String())
	loggedMap := make(map[string]string)
	require.Nil(json.Unmarshal(buf.Bytes(), &loggedMap))
	require.NotEmpty(loggedMap["requestId"])
}
