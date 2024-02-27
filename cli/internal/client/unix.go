package client

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"strings"
)

// Based on github.com/peterbourgon/unixtransport, but not cloning the transport
// using a host prefixing instead of bas64 encoding for communicating with the dialer

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

		if strings.HasPrefix(host, "unix:") {
			network, address = "unix", host[5:]
		}

		return next(ctx, network, address)
	}
}

func roundTripAdapter(next http.RoundTripper) http.RoundTripper {
	return roundTripFunc(func(req *http.Request) (*http.Response, error) {
		if req.URL == nil {
			return nil, fmt.Errorf("unix transport: no request URL")
		}

		scheme := strings.TrimSuffix(req.URL.Scheme, "+unix")
		if scheme == req.URL.Scheme {
			return nil, fmt.Errorf("unix transport: missing '+unix' suffix in scheme %s", req.URL.Scheme)
		}

		parts := strings.SplitN(req.URL.Path, ":", 2)

		var (
			socketPath  string
			requestPath string
		)

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

		encodedHost := fmt.Sprintf("[unix:%s]", socketPath)

		req = req.Clone(req.Context())

		req.URL.Scheme = scheme
		req.URL.Host = encodedHost
		req.URL.Path = requestPath

		return next.RoundTrip(req)
	})
}

type dialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

var defaultDialContextFunc = (&net.Dialer{}).DialContext

type roundTripFunc func(req *http.Request) (*http.Response, error)

func (f roundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) { return f(req) }
