// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestExchangeAcrRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		require.Equal(t, http.MethodPost, r.Method)
		require.Equal(t, "application/x-www-form-urlencoded", r.Header.Get("Content-Type"))
		require.NoError(t, r.ParseForm())
		assert.Equal(t, "access_token", r.Form.Get("grant_type"))
		assert.Equal(t, "mirror.azurecr.io", r.Form.Get("service"))
		assert.Equal(t, "tenant-id", r.Form.Get("tenant"))
		assert.Equal(t, "aad token with spaces & symbols", r.Form.Get("access_token"))

		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{"refresh_token":"refresh-token"}`))
		require.NoError(t, err)
	}))
	defer server.Close()

	refreshToken, err := exchangeAcrRefreshToken(context.Background(), server.Client(), server.URL, "mirror.azurecr.io", "tenant-id", "aad token with spaces & symbols")

	require.NoError(t, err)
	assert.Equal(t, "refresh-token", refreshToken)
}

func TestExchangeAcrRefreshToken_EmptyRefreshToken(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, err := w.Write([]byte(`{}`))
		require.NoError(t, err)
	}))
	defer server.Close()

	_, err := exchangeAcrRefreshToken(context.Background(), server.Client(), server.URL, "mirror.azurecr.io", "tenant-id", "aad-token")

	require.ErrorContains(t, err, "did not include a refresh token")
}

func TestExchangeAcrRefreshToken_ErrorBodyReadFailure(t *testing.T) {
	client := fakeHTTPDoer{response: &http.Response{
		StatusCode: http.StatusUnauthorized,
		Body:       errReadCloser{},
	}}

	_, err := exchangeAcrRefreshToken(context.Background(), client, "https://mirror.azurecr.io/oauth2/exchange", "mirror.azurecr.io", "tenant-id", "aad-token")

	require.ErrorContains(t, err, "failed to read response body")
}

type fakeHTTPDoer struct {
	response *http.Response
	err      error
}

func (d fakeHTTPDoer) Do(*http.Request) (*http.Response, error) {
	return d.response, d.err
}

type errReadCloser struct{}

func (errReadCloser) Read([]byte) (int, error) {
	return 0, errors.New("read failed")
}

func (errReadCloser) Close() error {
	return nil
}

var _ io.ReadCloser = errReadCloser{}
