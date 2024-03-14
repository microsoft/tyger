package client

import (
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/mattn/go-ieproxy"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	DefaultControlPlaneUnixSocketPath = "/opt/tyger/control-plane/tyger.sock"
	DefaultControlPlaneUnixSocketUrl  = "http+unix://" + DefaultControlPlaneUnixSocketPath + ":"

	DefaultDockerGatewayUrl = "http://localhost:6777"
)

var (
	underlyingHttpTransport = http.DefaultTransport.(*http.Transport)
	defaultRoundTripper     = http.DefaultTransport
	defaultRetryableClient  *retryablehttp.Client
)

func init() {
	registerHttpUnixProtocolHandler(underlyingHttpTransport)
}

type TygerClient struct {
	ControlPlaneUrl    *url.URL
	ControlPlaneClient *retryablehttp.Client
	GetAccessToken     AccessTokenFunc
	DataPlaneClient    *retryablehttp.Client
	Principal          string
}

type AccessTokenFunc func(ctx context.Context) (string, error)

func NewTygerClient(controlPlaneUrl *url.URL, getAccessToken AccessTokenFunc, dataPlaneProxy *url.URL, principal string) *TygerClient {
	controlPlaneClient := DefaultRetryableClient()
	dataPlaneClient := NewRetryableClient()
	dataPlaneClient.HTTPClient = &http.Client{
		Timeout:   100 * time.Second,
		Transport: dataPlaneClient.HTTPClient.Transport,
	}

	if dataPlaneProxy != nil {
		dataPlaneRoundtripper := dataPlaneClient.HTTPClient.Transport
		var dataPlaneHttpTransport *http.Transport
		switch t := dataPlaneRoundtripper.(type) {
		case nil:
			dataPlaneRoundtripper = underlyingHttpTransport.Clone()
			dataPlaneHttpTransport = dataPlaneRoundtripper.(*http.Transport)
		case *http.Transport:
			dataPlaneRoundtripper = t.Clone()
			dataPlaneHttpTransport = dataPlaneRoundtripper.(*http.Transport)
		case *loggingTransport:
			dataPlaneRoundtripper = t.Clone()
			dataPlaneHttpTransport = dataPlaneRoundtripper.(*loggingTransport).transport
		default:
			panic(fmt.Sprintf("unexpected roundtripper type %T", t))
		}

		// Alt protocol registrations are not cloned in Clone() so we need to re-register them
		registerHttpUnixProtocolHandler(dataPlaneHttpTransport)
		dataPlaneHttpTransport.Proxy = httpCheckProxyFunc(http.ProxyURL(dataPlaneProxy))
		dataPlaneClient.HTTPClient.Transport = dataPlaneRoundtripper
	}

	return &TygerClient{
		ControlPlaneUrl:    controlPlaneUrl,
		ControlPlaneClient: controlPlaneClient,
		DataPlaneClient:    dataPlaneClient,
		GetAccessToken:     getAccessToken,
		Principal:          principal,
	}
}

func DefaultRetryableClient() *retryablehttp.Client {
	if defaultRetryableClient == nil {
		defaultRetryableClient = NewRetryableClient()
	}

	return defaultRetryableClient
}

func NewRetryableClient() *retryablehttp.Client {
	client := retryablehttp.NewClient()
	client.RetryMax = 6

	client.Logger = nil
	client.ErrorHandler = func(resp *http.Response, err error, numTries int) (*http.Response, error) {
		return resp, err
	}
	client.CheckRetry = createCheckRetryFunc(client)

	client.HTTPClient = &http.Client{
		Transport: defaultRoundTripper,
	}

	return client
}

func PrepareDefaultHttpTransport(proxyString string) error {
	proxyFunc, err := ParseProxy(proxyString)
	if err != nil {
		return err
	}

	underlyingHttpTransport.Proxy = proxyFunc
	if strings.HasPrefix(proxyString, "ssh://") {
		underlyingHttpTransport.MaxConnsPerHost = 16
	}

	underlyingHttpTransport.MaxIdleConnsPerHost = 1000
	underlyingHttpTransport.ResponseHeaderTimeout = 60 * time.Second

	if log.Logger.GetLevel() <= zerolog.DebugLevel {
		defaultRoundTripper = &loggingTransport{transport: underlyingHttpTransport}
		http.DefaultClient.Transport = defaultRoundTripper
	}

	return nil
}

func DisableTlsCertificateValidation() {
	underlyingHttpTransport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
}

func GetHttpTransport(client *http.Client) *http.Transport {
	switch t := client.Transport.(type) {
	case nil:
		return underlyingHttpTransport
	case *http.Transport:
		return t
	case *loggingTransport:
		return t.transport
	default:
		panic(fmt.Sprintf("unexpected transport type %T", t))
	}
}

type loggingTransport struct {
	transport *http.Transport
}

func (t *loggingTransport) Clone() *loggingTransport {
	return &loggingTransport{
		transport: t.transport.Clone(),
	}
}

func (t *loggingTransport) RoundTrip(req *http.Request) (*http.Response, error) {
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

	resp, err := t.transport.RoundTrip(req)

	if err != nil {
		logger.Trace().Err(err).Msg("Error sending request")

		return nil, err
	}

	logger.Trace().Int("status", resp.StatusCode).Msg("Received response")

	return resp, err
}

func ParseProxy(proxyString string) (func(r *http.Request) (*url.URL, error), error) {
	switch proxyString {
	case "none":
		return func(r *http.Request) (*url.URL, error) { return nil, nil }, nil
	case "auto", "automatic", "":
		return httpCheckProxyFunc(ieproxy.GetProxyFunc()), nil
	default:
		parsedProxy, err := url.Parse(proxyString)
		if err != nil || parsedProxy.Host == "" {
			// It may be that the URI was given in the form "host:1234", and the scheme ends up being "host"
			parsedProxy, err = url.Parse("http://" + proxyString)
			if err != nil {
				return nil, errors.New("proxy must be 'auto', 'automatic', '' (same as 'auto/automatic'), 'none', or a valid URI")
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
