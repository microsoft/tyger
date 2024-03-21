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

	"github.com/docker/cli/cli/connhelper/commandconn"
)

// Inspired by github.com/peterbourgon/unixtransport

var (
	errNotSocketHost = errors.New("not a socket path host")
	errNotSshHost    = errors.New("not an SSH host")
	encoding         = base32.StdEncoding.WithPadding(base32.NoPadding)
)

func registerHttpUnixProtocolHandler(t *http.Transport) {
	switch {
	case t.DialContext == nil && t.DialTLSContext == nil:
		t.DialContext = dialContextAdapter(defaultDialContextFunc)

	case t.DialContext == nil && t.DialTLSContext != nil:
		t.DialContext = dialContextAdapter(defaultDialContextFunc)
		t.DialTLSContext = dialContextAdapter(t.DialTLSContext)

	case t.DialContext != nil && t.DialTLSContext == nil:
		t.DialContext = dialContextAdapter(t.DialContext)

	case t.DialContext != nil && t.DialTLSContext != nil:
		t.DialContext = dialContextAdapter(t.DialContext)
		t.DialTLSContext = dialContextAdapter(t.DialTLSContext)
	}

	tt := roundTripAdapter(t)

	t.RegisterProtocol("http+unix", tt)
	t.RegisterProtocol("https+unix", tt)
}

func dialContextAdapter(next dialContextFunc) dialContextFunc {
	return func(ctx context.Context, network, address string) (net.Conn, error) {

		host, _, err := net.SplitHostPort(address)
		if err != nil {
			host = address
		}

		if socketPath, err := DecodeUnixPathFromHost(host); err == nil {
			network, address = "unix", socketPath
		} else if sshParams, err := DecodeSshUrlFromHost(host); err == nil {
			return commandconn.New(ctx, "ssh", sshParams.FormatArgs()...)
		}

		return next(ctx, network, address)
	}
}

func roundTripAdapter(next http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL == nil {
			return nil, fmt.Errorf("unix transport: no request URL")
		}

		var scheme string
		switch req.URL.Scheme {
		case "http+unix":
			scheme = "http"
		case "https+unix":
			scheme = "https"
		default:
			return nil, fmt.Errorf("unix transport: missing '+unix' suffix in scheme %s", req.URL.Scheme)
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
		req.URL.Host = EncodeUnixPathToHost(socketPath)
		req.URL.Path = requestPath

		return next.RoundTrip(req)
	})
}

func EncodeUnixPathToHost(socketPath string) string {
	return fmt.Sprintf("!unix!%s", encoding.EncodeToString([]byte(socketPath)))
}

func DecodeUnixPathFromHost(host string) (string, error) {
	if strings.HasPrefix(host, "!unix!") {
		if res, err := encoding.DecodeString(host[6:]); err == nil {
			return string(res), nil
		}
	}

	return "", errNotSocketHost
}

func EncodeSshUrlToHost(sp *SshParams) string {
	u := sp.URL()
	u.Path = ""
	return fmt.Sprintf("!ssh!%s", encoding.EncodeToString([]byte(u.String())))
}

func DecodeSshUrlFromHost(host string) (*SshParams, error) {
	if strings.HasPrefix(host, "!ssh!") {
		if res, err := encoding.DecodeString(host[5:]); err == nil {
			return ParseSshUrl(string(res))
		}
	}

	return nil, errNotSshHost
}

type dialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

var defaultDialContextFunc = (&net.Dialer{}).DialContext

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
