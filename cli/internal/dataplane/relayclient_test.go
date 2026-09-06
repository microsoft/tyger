// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/stretchr/testify/require"
)

func TestPartiallyBufferedReaderReadWithoutRewinding(t *testing.T) {
	inputBytes := make([]byte, 2048)
	for i := range inputBytes {
		inputBytes[i] = byte(i % 256)
	}

	testCases := []struct {
		capacity int
		copySize int
	}{
		{1, 1},
		{4, 4},
		{16, 4},
		{16, 32},
		{7, 3},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%d:%d", tc.capacity, tc.copySize), func(t *testing.T) {
			inputReader := bytes.NewReader(inputBytes)

			bufReader := NewPartiallyBufferedReader(inputReader, tc.capacity)
			outputBuffer := &bytes.Buffer{}
			unoptimizedCopy(outputBuffer, bufReader, tc.copySize)

			require.True(t, bytes.Equal(inputBytes, outputBuffer.Bytes()))
		})
	}
}

func TestPartiallyBufferedReaderBasicRewind(t *testing.T) {
	inputBytes := make([]byte, 128)
	for i := range inputBytes {
		inputBytes[i] = byte(i)
	}

	testCases := []struct {
		capacity        int
		initialReadSize int
		copySize        int
	}{
		{128, 128, 128},
		{128, 127, 512},
		{128, 127, 126},
		{128, 129, 512},
		{32, 32, 32},
		{32, 31, 32},
		{32, 31, 22},
		{32, 31, 22},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%d:%d:%d", tc.capacity, tc.initialReadSize, tc.copySize), func(t *testing.T) {
			inputReader := bytes.NewReader(inputBytes)

			bufReader := NewPartiallyBufferedReader(inputReader, tc.capacity)

			b := make([]byte, tc.initialReadSize)

			n, err := bufReader.Read(b)
			require.NoError(t, err)
			require.Equal(t, min(len(b), len(inputBytes)), n)

			require.NoError(t, bufReader.Rewind())
			out := bytes.Buffer{}
			unoptimizedCopy(&out, bufReader, tc.copySize)

			require.True(t, bytes.Equal(inputBytes, out.Bytes()))
		})
	}

}
func TestPartiallyBufferedReaderCannotRewind(t *testing.T) {
	inputBytes := make([]byte, 128)
	for i := range inputBytes {
		inputBytes[i] = byte(i)
	}

	testCases := []struct {
		capacity        int
		initialReadSize int
	}{
		{64, 65},
		{64, 129},
	}

	for _, tc := range testCases {
		t.Run(fmt.Sprintf("%d:%d", tc.capacity, tc.initialReadSize), func(t *testing.T) {
			inputReader := bytes.NewReader(inputBytes)

			bufReader := NewPartiallyBufferedReader(inputReader, tc.capacity)

			b := make([]byte, tc.initialReadSize)
			n, err := bufReader.Read(b)
			require.NoError(t, err)
			require.Equal(t, min(len(b), len(inputBytes)), n)

			require.Error(t, bufReader.Rewind())
		})
	}
}

// Like io.Copy but without any optimizations
func unoptimizedCopy(dst io.Writer, src io.Reader, bufferSize int) error {
	buf := make([]byte, bufferSize)
	_, err := io.CopyBuffer(&simpleWriter{dst}, &simpleReader{src}, buf)
	return err
}

func Test(t *testing.T) {
	var b io.Reader = &simpleReader{&bytes.Buffer{}}

	if _, ok := b.(io.WriterTo); ok {
		require.FailNow(t, "b is a WriterTo")
	}
}

// Hides io.WriterTo implementation
type simpleReader struct {
	io.Reader
}

// Hides io.ReaderFrom implementation
type simpleWriter struct {
	io.Writer
}

// trackingReadCloser wraps an io.ReadCloser to track whether Close was called.
type trackingReadCloser struct {
	io.ReadCloser
	closed *bool
}

func (t *trackingReadCloser) Close() error {
	*t.closed = true
	return t.ReadCloser.Close()
}

// TestRelayWriteClosesResponseBody verifies that relayWrite closes the HTTP response body.
func TestRelayWriteClosesResponseBody(t *testing.T) {
	// Create a test server that returns a 202 Accepted response for PUT
	// and 200 OK for HEAD (ping).
	// We wrap the response body to track if Close is called.
	var putBodyClosed bool
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.Method {
		case http.MethodHead:
			w.WriteHeader(http.StatusOK)
		case http.MethodPut:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte("OK"))
		default:
			w.WriteHeader(http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()

	// Create a ContainerClient that points to our test server.
	serverURL, _ := url.Parse(server.URL)
	// We need to create a minimal ContainerClient with a custom HTTP client
	// that uses our test server.
	testClient := &ContainerClient{
		innerClient: retryablehttp.NewClient(),
		baseUrl:     serverURL,
		currentAccessUrl: &url.URL{
			Scheme: "http",
			Host:   serverURL.Host,
			Path:   "",
		},
		currentAccessUrlQuery: url.Values{},
		getNewAccessUrl:       nil,
	}

	// Override the HTTP client to use a custom transport that tracks body closure.
	// Only track the PUT response body (not the HEAD from ping).
	putBodyClosed = false
	testClient.innerClient.HTTPClient = &http.Client{
		Transport: &trackingTransport{
			base:   http.DefaultTransport,
			closed: &putBodyClosed,
			method: http.MethodPut,
		},
	}

	ctx := context.Background()
	err := relayWrite(ctx, testClient, client.TygerConnectionTypeTcp, bytes.NewReader([]byte("test data")))
	require.NoError(t, err)
	require.True(t, putBodyClosed, "PUT response body should be closed on success")
}

// trackingTransport wraps an http.RoundTripper to track response body closure.
type trackingTransport struct {
	base   http.RoundTripper
	closed *bool
	method string
}

func (t *trackingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	// Only wrap the response body for the specified method (e.g., PUT).
	if t.method == "" || req.Method == t.method {
		resp.Body = &trackingReadCloser{
			ReadCloser: resp.Body,
			closed:     t.closed,
		}
	}
	return resp, nil
}
