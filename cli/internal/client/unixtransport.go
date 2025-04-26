// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package client

import (
	"context"
	"encoding/base32"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// support for using HTTP over unix sockets

const UnixTransportHostPrefix = "unix----"

var (
	errNotSocketHost = errors.New("not a socket path host")
	encoding         = base32.StdEncoding.WithPadding(base32.NoPadding)
)

func makeUnixDialer(next dialContextFunc) dialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {
		host, _, err := net.SplitHostPort(address)
		if err != nil {
			host = address
		}

		if socketPath, err := decodeUnixPathFromHost(host); err == nil {
			network, address = "unix", socketPath
		}

		return next(ctx, network, address)
	}
}

type unixAwareTransport struct {
	next http.RoundTripper
}

func makeUnixAwareTransport(next http.RoundTripper) http.RoundTripper {
	return &unixAwareTransport{next: next}
}

func (t *unixAwareTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	if req.URL == nil {
		return t.next.RoundTrip(req)
	}

	var scheme string
	switch req.URL.Scheme {
	case "http+unix":
		scheme = "http"
	case "https+unix":
		scheme = "https"
	default:
		return t.next.RoundTrip(req)
	}

	parts := strings.SplitN(req.URL.Path, ":", 2)
	var socketPath string
	var requestPath string

	switch len(parts) {
	case 1:
		socketPath = parts[0]
		requestPath = ""
	case 2:
		socketPath = parts[0]
		requestPath = parts[1]
	default:
		return nil, errors.New("unix transport: invalid path")
	}

	req = req.Clone(req.Context())

	req.URL.Scheme = scheme
	req.URL.Host = encodeUnixPathToHost(socketPath)
	req.URL.Path = requestPath

	return t.next.RoundTrip(req)
}

func (t *unixAwareTransport) GetUnderlyingTransport() *http.Transport {
	return getHttpTransport(t.next)
}

var _ HttpTransportExposer = &unixAwareTransport{}

func encodeUnixPathToHost(socketPath string) string {
	return fmt.Sprintf("%s%s", UnixTransportHostPrefix, encoding.EncodeToString([]byte(socketPath)))
}

func decodeUnixPathFromHost(host string) (string, error) {
	if strings.HasPrefix(host, UnixTransportHostPrefix) {
		if res, err := encoding.DecodeString(host[len(UnixTransportHostPrefix):]); err == nil {
			return string(res), nil
		}
	}

	return "", errNotSocketHost
}
