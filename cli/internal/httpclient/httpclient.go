// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package httpclient

import (
	"context"
	"crypto/tls"
	"fmt"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/settings"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

var DefaultRetryableClient = NewRetryableClient()

func NewRetryableClient() *retryablehttp.Client {
	client := retryablehttp.NewClient()
	client.RetryMax = 6

	client.Logger = nil
	client.ErrorHandler = func(resp *http.Response, err error, numTries int) (*http.Response, error) {
		return resp, err
	}
	client.CheckRetry = createCheckRetryFunc(client)

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

	if log.Logger.GetLevel() > zerolog.TraceLevel {
		return m.transport.RoundTrip(req)
	}

	// Trace logging
	proxy, err := m.transport.Proxy(req)
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

	resp, err := m.transport.RoundTrip(req)

	if err != nil {
		logger.Trace().Err(err).Msg("Error sending request")
		return nil, err
	}

	logger.Trace().Int("status", resp.StatusCode).Msg("Received response")

	return resp, err
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
