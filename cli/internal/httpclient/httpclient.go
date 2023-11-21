package httpclient

import (
	"context"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/mattn/go-ieproxy"
	"github.com/microsoft/tyger/cli/internal/settings"
	"github.com/rs/zerolog/log"
)

type proxyFuncContextKeyType string

var (
	proxyFuncContextKey    proxyFuncContextKeyType = "proxyFunc"
	DefaultRetryableClient                         = NewRetryableClient()
)

func SetProxyFunc(ctx context.Context, proxyFunc func(*http.Request) (*url.URL, error)) context.Context {
	return context.WithValue(ctx, proxyFuncContextKey, proxyFunc)
}

func GetProxyFuncFromContext(ctx context.Context) func(*http.Request) (*url.URL, error) {
	if proxyFunc, ok := ctx.Value(proxyFuncContextKey).(func(*http.Request) (*url.URL, error)); ok {
		return proxyFunc
	}
	return nil
}

func GetProxyFunc() func(*http.Request) (*url.URL, error) {
	innerFunc := ieproxy.GetProxyFunc()
	return func(req *http.Request) (*url.URL, error) {
		if req.URL.Scheme == "http" {
			// We will not use an HTTP proxy when when not using TLS.
			// The only supported scenario for using http and not https is
			// when using using tyger to call tyger-proxy. In that case, we
			// want to connect to tyger-proxy directly, and not through a proxy.
			return nil, nil
		}

		serviceInfo, _ := settings.GetServiceInfoFromContext(req.Context())
		if serviceInfo == nil {
			return innerFunc(req)
		}

		dataPlaneProxy := serviceInfo.GetDataPlaneProxy()
		controlPlaneUrl := serviceInfo.GetServerUri()

		if dataPlaneProxy == nil ||
			(req.URL.Scheme == controlPlaneUrl.Scheme &&
				req.URL.Host == controlPlaneUrl.Host &&
				strings.HasPrefix(req.URL.Path, controlPlaneUrl.Path)) {

			if serviceInfo.GetIgnoreSystemProxySettings() {
				return nil, nil
			}

			return innerFunc(req)
		}

		return dataPlaneProxy, nil

	}
}

func NewRetryableClient() *retryablehttp.Client {
	client := retryablehttp.NewClient()
	client.RetryMax = 6
	client.HTTPClient.Timeout = 100 * time.Second

	client.Logger = nil
	client.ErrorHandler = func(resp *http.Response, err error, numTries int) (*http.Response, error) {
		return resp, err
	}
	client.CheckRetry = checkRetry

	transport := client.HTTPClient.Transport.(*http.Transport)
	transport.MaxIdleConnsPerHost = 1000
	transport.ResponseHeaderTimeout = 20 * time.Second

	transport.Proxy = GetProxyFunc()

	return client
}

func checkRetry(ctx context.Context, resp *http.Response, err error) (bool, error) {
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

// If the error is a *url.Error, redact the query string values
func RedactHttpError(err error) error {
	if httpErr, ok := err.(*url.Error); ok {
		if httpErr.URL != "" {
			if index := strings.IndexByte(httpErr.URL, '?'); index != -1 {
				if u, err := url.Parse(httpErr.URL); err == nil {
					q := u.Query()
					for _, v := range q {
						for i := range v {
							v[i] = "REDACTED"
						}

					}

					u.RawQuery = q.Encode()
					httpErr.URL = u.String()
				}
			}
		}

		httpErr.Err = RedactHttpError(httpErr.Err)
	}
	return err
}

// Makes http.DefaultClient and http.DefaultTransport panic
// when used. We want to avoid these because they will not have
// been properly set up to use any required proxy.
func DisableDefaultTransport() {
	http.DefaultClient.Transport = &disabledTransport{}
	http.DefaultTransport = &disabledTransport{}
}

type disabledTransport struct {
}

func (t *disabledTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	panic("Default transport has been disabled")
}
