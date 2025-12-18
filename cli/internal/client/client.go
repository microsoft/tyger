// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package client

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	_ "embed"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/golang-jwt/jwt/v5"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/mattn/go-ieproxy"
	"github.com/pkg/errors"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	DefaultControlPlaneSocketPathEnvVar = "TYGER_SOCKET_PATH"
	defaultControlPlaneUnixSocketPath   = "/opt/tyger/api.sock"

	clockSkewWarningThreshold = 15 * time.Minute
	clockSkewWarning          = "Detected significant clock skew. The system time may be wrong."
)

var (
	DefaultClient          *Client
	DefaultRetryableClient *retryablehttp.Client

	//go:embed ca-certificates.pem
	CaCertificates []byte
)

func GetDefaultSocketUrl() string {
	path := os.Getenv(DefaultControlPlaneSocketPathEnvVar)
	if path == "" {
		path = defaultControlPlaneUnixSocketPath
	}

	return "http+unix://" + path + ":"
}

type MakeRoundTripper func(next http.RoundTripper) http.RoundTripper

type MakeDialer func(next dialContextFunc) dialContextFunc

type dialContextFunc func(ctx context.Context, network, address string) (net.Conn, error)

type TlsCaCertificateSource string

const (
	TlsCaCertificateSourceOperatingSystem TlsCaCertificateSource = "os"
	TlsCaCertificateSourceEmbedded        TlsCaCertificateSource = "embedded"
)

var certPoolCache sync.Map // map[TlsCaCertificateSource]*x509.CertPool

func GetCaCertPemBytes(source TlsCaCertificateSource) ([]byte, error) {
	if source == "" || source == TlsCaCertificateSourceOperatingSystem {
		return nil, nil
	}

	certPool := x509.NewCertPool()

	switch source {
	case TlsCaCertificateSourceEmbedded:
		return CaCertificates, nil
	default:
		// check if this is an inline PEM
		byteSource := []byte(source)
		if certPool.AppendCertsFromPEM(byteSource) {
			return byteSource, nil
		}

		var certData []byte
		certData, err := os.ReadFile(string(source))
		if err != nil {
			return nil, errors.Wrap(err, "failed to read CA certificate file")
		}
		return certData, nil
	}
}

func getCaCertPool(source TlsCaCertificateSource) (*x509.CertPool, error) {
	if source == "" || source == TlsCaCertificateSourceOperatingSystem {
		return nil, nil
	}

	if cached, ok := certPoolCache.Load(source); ok {
		return cached.(*x509.CertPool), nil
	}

	certPool := x509.NewCertPool()

	certData, err := GetCaCertPemBytes(source)
	if err != nil {
		return nil, err
	}

	if ok := certPool.AppendCertsFromPEM(certData); !ok {
		return nil, errors.New("invalid CA certificate data")
	}

	certPoolCache.Store(source, certPool)
	return certPool, nil
}

type Client struct {
	*retryablehttp.Client
	transport               http.RoundTripper
	underlyingHttpTransport *http.Transport
}

func (c *Client) GetUnderlyingTransport() *http.Transport {
	return c.underlyingHttpTransport
}

func (c *Client) Proxy(req *http.Request) (*url.URL, error) {
	return c.underlyingHttpTransport.Proxy(req)
}

type ClientOptions struct {
	ProxyString         string
	CreateTransport     MakeRoundTripper
	CreateDialer        MakeDialer
	DisableRetries      bool
	CaCertificateSource TlsCaCertificateSource
}

func NewClient(opts *ClientOptions) (*Client, error) {
	if opts == nil {
		opts = &ClientOptions{}
	}

	if opts.CreateDialer == nil {
		opts.CreateDialer = makeUnixDialer
	}

	if opts.CreateTransport == nil {
		opts.CreateTransport = makeUnixAwareTransport
	}

	proxyFunc, err := ParseProxy(opts.ProxyString)
	if err != nil {
		return nil, err
	}

	caCertPool, err := getCaCertPool(opts.CaCertificateSource)
	if err != nil {
		return nil, err
	}

	transport := &http.Transport{
		MaxIdleConnsPerHost:   1000,
		ResponseHeaderTimeout: 60 * time.Second,
		Proxy:                 proxyFunc,
		DialContext:           opts.CreateDialer((&net.Dialer{}).DialContext),
		TLSClientConfig: &tls.Config{
			RootCAs: caCertPool,
		},
	}

	var roundTripper http.RoundTripper = transport

	roundTripper = opts.CreateTransport(roundTripper)

	roundTripper = &clockSkewCheckingRoundTripper{
		RoundTripper: roundTripper,
	}

	if log.Logger.GetLevel() <= zerolog.DebugLevel {
		roundTripper = &loggingTransport{RoundTripper: roundTripper}
	}

	retryableClient := retryablehttp.NewClient()
	if opts.DisableRetries {
		retryableClient.RetryMax = 0
	} else {
		retryableClient.RetryMax = 6
	}

	retryableClient.Logger = nil
	retryableClient.ErrorHandler = func(resp *http.Response, err error, numTries int) (*http.Response, error) {
		return resp, err
	}
	retryableClient.CheckRetry = createCheckRetryFunc(retryableClient)

	retryableClient.HTTPClient = &http.Client{
		Transport: roundTripper,
	}

	return &Client{
		transport:               roundTripper,
		underlyingHttpTransport: transport,
		Client:                  retryableClient,
	}, nil
}

func NewControlPlaneClient(opts *ClientOptions) (*Client, error) {
	return NewClient(opts)
}

func NewDataPlaneClient(opts *ClientOptions) (*Client, error) {
	c, err := NewClient(opts)
	if err != nil {
		return nil, err
	}

	c.Client.HTTPClient.Timeout = 100 * time.Second
	return c, nil
}

func SetDefaultNetworkClientSettings(opts *ClientOptions) error {
	client, err := NewClient(opts)
	if err != nil {
		return err
	}

	DefaultClient = client
	DefaultRetryableClient = client.Client
	http.DefaultClient = client.Client.HTTPClient
	http.DefaultClient.Transport = client.transport
	http.DefaultTransport = client.transport
	return nil
}

type AccessTokenFunc func(ctx context.Context) (string, error)

type TygerConnectionType int

const (
	TygerConnectionTypeTcp TygerConnectionType = iota
	TygerConnectionTypeUnix
	TygerConnectionTypeSsh
	TygerConnectionTypeDocker
)

type TygerClient struct {
	ControlPlaneUrl    *url.URL
	ControlPlaneClient *Client
	GetAccessToken     AccessTokenFunc
	DataPlaneClient    *Client
	Principal          string
	RawControlPlaneUrl *url.URL
	RawProxy           *url.URL
}

func (c *TygerClient) ConnectionType() TygerConnectionType {
	switch c.RawControlPlaneUrl.Scheme {
	case "docker":
		return TygerConnectionTypeDocker
	case "ssh":
		return TygerConnectionTypeSsh
	case "http+unix":
		return TygerConnectionTypeUnix
	default:
		return TygerConnectionTypeTcp
	}
}

func (c *TygerClient) GetRoleAssignments(ctx context.Context) ([]string, error) {
	tok, err := c.GetAccessToken(ctx)
	if err != nil {
		return nil, err
	}

	claims := jwt.MapClaims{}
	if _, _, err := jwt.NewParser().ParseUnverified(tok, claims); err == nil {
		roles, _ := claims["roles"].([]any)
		if roles != nil {
			roleStrings := []string{}
			for _, role := range roles {
				if roleString, ok := role.(string); ok {
					roleStrings = append(roleStrings, roleString)
				}
			}
			return roleStrings, nil
		}
	}

	return []string{}, nil
}

type HttpTransportOption func(*http.Transport)

func getHttpTransport(roudtripper http.RoundTripper) *http.Transport {
	if transport, ok := roudtripper.(*http.Transport); ok {
		return transport
	}

	if exposer, ok := roudtripper.(HttpTransportExposer); ok {
		return exposer.GetUnderlyingTransport()
	}

	panic(fmt.Sprintf("could not get *http.Transport from %T", roudtripper))
}

type HttpTransportExposer interface {
	GetUnderlyingTransport() *http.Transport
}

type loggingTransport struct {
	http.RoundTripper
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	underlyingHttpTransport := getHttpTransport(t.RoundTripper)
	proxy, err := underlyingHttpTransport.Proxy(req)
	if err != nil {
		return nil, fmt.Errorf("error getting proxy: %w", err)
	}

	var proxyString string
	if proxy != nil {
		proxyString = proxy.String()
	}

	logger := log.Ctx(req.Context()).With().
		Str("proxy", proxyString).
		Str("method", req.Method).
		Str("url", RedactUrl(req.URL).String()).
		Logger()

	logger.Trace().Msg("Sending request")

	resp, err := t.RoundTripper.RoundTrip(req)

	if err != nil {
		logger.Trace().Err(err).Msg("Error sending request")

		return nil, err
	}

	logger.Trace().Int("status", resp.StatusCode).Msg("Received response")

	return resp, err
}

func (t *loggingTransport) GetUnderlyingTransport() *http.Transport {
	return getHttpTransport(t.RoundTripper)
}

var _ HttpTransportExposer = &loggingTransport{}

// A global variable to ensure we only incur the cost of checking clock skew once and only log one warning.
var clockSkewChecked atomic.Bool

type clockSkewCheckingRoundTripper struct {
	http.RoundTripper
}

func (t *clockSkewCheckingRoundTripper) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.RoundTripper.RoundTrip(req)
	if err != nil {
		return nil, err
	}

	if !clockSkewChecked.Swap(true) {
		// Check for clock skew by looking for a "Date" header
		if dateHeader := resp.Header.Get("Date"); dateHeader != "" {
			if date, err := http.ParseTime(dateHeader); err == nil {
				now := time.Now().UTC()
				if now.Sub(date) > clockSkewWarningThreshold || date.Sub(now) > clockSkewWarningThreshold {
					log.Ctx(req.Context()).Warn().Msg(clockSkewWarning)
				}
			}
		}
	}

	return resp, nil
}

func (t *clockSkewCheckingRoundTripper) GetUnderlyingTransport() *http.Transport {
	return getHttpTransport(t.RoundTripper)
}

var _ HttpTransportExposer = &clockSkewCheckingRoundTripper{}

func ParseProxy(proxyString string) (func(r *http.Request) (*url.URL, error), error) {
	switch proxyString {
	case "none":
		return func(r *http.Request) (*url.URL, error) { return nil, nil }, nil
	case "auto", "automatic", "":
		return httpCheckProxyFunc(ieproxy.GetProxyFunc()), nil
	default:
		parsedProxy, err := url.Parse(proxyString)
		if err != nil || parsedProxy.Host == "" {
			// It may be that the URL was given in the form "host:1234", and the scheme ends up being "host"
			parsedProxy, err = url.Parse("http://" + proxyString)
			if err != nil {
				return nil, errors.New("proxy must be 'auto', 'automatic', '' (same as 'auto/automatic'), 'none', or a valid URL")
			}
		}

		return httpCheckProxyFunc(http.ProxyURL(parsedProxy)), nil
	}
}

func httpCheckProxyFunc(baseCheckProxyFunc func(r *http.Request) (*url.URL, error)) func(r *http.Request) (*url.URL, error) {
	return func(r *http.Request) (*url.URL, error) {
		if r.URL.Scheme == "http" && !strings.HasPrefix(r.URL.Host, "!unix!") {
			// We will not use an HTTP proxy when when not using TLS,
			// unless we are using Unix domain dockets.
			// Otherwise, the only supported scenario for using http and not https is
			// when using using tyger to call tyger-proxy. In that case, we
			// want to connect to tyger-proxy directly, and not through a proxy.
			return nil, nil
		}

		return baseCheckProxyFunc(r)
	}
}

func createCheckRetryFunc(client *retryablehttp.Client) func(ctx context.Context, resp *http.Response, err error) (bool, error) {
	return func(ctx context.Context, resp *http.Response, err error) (bool, error) {
		if client.RetryMax == 0 {
			return false, err
		}

		if ctx.Err() != nil {
			return false, ctx.Err()
		}
		shouldRetry, checkErr := retryablehttp.DefaultRetryPolicy(ctx, resp, err)
		if shouldRetry {
			if err != nil {
				log.Ctx(ctx).Warn().Err(RedactHttpError(err)).Msg("Received retryable error")
			} else if resp != nil {
				log.Ctx(ctx).Warn().Int("statusCode", resp.StatusCode).Msg("Received retryable status code")
			}
		}
		return shouldRetry, checkErr
	}
}

// If the error is a *url.Error, redact the query string values in the error
func RedactHttpError(err error) error {
	if httpErr, ok := err.(*url.Error); ok {
		if httpErr.URL != "" {
			if index := strings.IndexByte(httpErr.URL, '?'); index != -1 {
				if u, err := url.Parse(httpErr.URL); err == nil {
					redacted := RedactUrl(u)
					if redacted != u {
						httpErr.URL = redacted.String()
					}
				}
			}
		}

		httpErr.Err = RedactHttpError(httpErr.Err)
	}
	return err
}

// redact query string values
func RedactUrl(u *url.URL) *url.URL {
	q := u.Query()
	if len(q) == 0 {
		return u
	}

	for _, v := range q {
		for i := range v {
			v[i] = "REDACTED"
		}
	}

	clone := *u
	clone.RawQuery = q.Encode()
	return &clone
}

func CloneRetryableClient(c *retryablehttp.Client) *retryablehttp.Client {
	innerClient := *c.HTTPClient
	return &retryablehttp.Client{
		HTTPClient:      &innerClient,
		Logger:          c.Logger,
		RetryWaitMin:    c.RetryWaitMin,
		RetryWaitMax:    c.RetryWaitMax,
		RetryMax:        c.RetryMax,
		RequestLogHook:  c.RequestLogHook,
		ResponseLogHook: c.ResponseLogHook,
		CheckRetry:      c.CheckRetry,
		Backoff:         c.Backoff,
		ErrorHandler:    c.ErrorHandler,
	}
}
