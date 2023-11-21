package httpclient

import (
	"context"
	"crypto/tls"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/settings"
	"github.com/rs/zerolog/log"
)

var DefaultRetryableClient = NewRetryableClient()

func NewRetryableClient() *retryablehttp.Client {
	client := retryablehttp.NewClient()
	client.RetryMax = 6
	client.HTTPClient.Timeout = 100 * time.Second

	client.Logger = nil
	client.ErrorHandler = func(resp *http.Response, err error, numTries int) (*http.Response, error) {
		return resp, err
	}
	client.CheckRetry = checkRetry

	client.HTTPClient.Transport = &lazyInitTransport{
		transport: client.HTTPClient.Transport.(*http.Transport),
	}

	return client
}

type lazyInitTransport struct {
	transportInit sync.Once
	transport     *http.Transport
}

func (m *lazyInitTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	m.transportInit.Do(func() {
		m.transport.MaxConnsPerHost = 1000
		m.transport.ResponseHeaderTimeout = 20 * time.Second

		if serviceInfo, err := settings.GetServiceInfoFromContext(req.Context()); err == nil {
			m.transport.Proxy = serviceInfo.GetProxyFunc()
			if serviceInfo.GetDisableTlsCertificateValidation() {
				m.transport.TLSClientConfig = &tls.Config{InsecureSkipVerify: true}
			}
		}
	})

	return m.transport.RoundTrip(req)
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
