// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package client

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"testing"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

func TestGetProxyFuncAutomatic(t *testing.T) {
	testCases := []string{
		"", "auto", "automatic",
	}
	for _, tC := range testCases {
		t.Run(tC, func(t *testing.T) {
			t.Setenv("HTTPS_PROXY", "http://123.456.789.012:8080")
			t.Setenv("HTTP_PROXY", "http://123.456.789.012:8888")

			targetUrl, err := url.Parse("https://example.com")
			require.NoError(t, err)

			req := &http.Request{
				URL: targetUrl,
			}

			proxyFunc, err := ParseProxy(tC)
			require.NoError(t, err)

			proxyURL, err := proxyFunc(req)
			require.NoError(t, err)
			require.NotNil(t, proxyURL)
			require.Equal(t, "http://123.456.789.012:8080", proxyURL.String())

			// now use HTTP, which should not use a proxy
			targetUrl, err = url.Parse("http://example.com")
			require.NoError(t, err)

			// Test with http scheme
			req = &http.Request{
				URL: targetUrl,
			}
			proxyURL, err = proxyFunc(req)
			require.NoError(t, err)
			require.Nil(t, proxyURL)
		})
	}
}

func TestGetProxyFuncNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://123.456.789.012:8080")
	t.Setenv("HTTP_PROXY", "http://123.456.789.012:8888")

	proxyFunc, err := ParseProxy("none")
	require.NoError(t, err)

	targetUrl, err := url.Parse("https://example.com")
	require.NoError(t, err)

	req := &http.Request{
		URL: targetUrl,
	}
	proxyURL, err := proxyFunc(req)
	require.NoError(t, err)
	require.Nil(t, proxyURL)
}

func TestGetProxyFuncExplicitProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://123.456.789.012:8080")
	t.Setenv("HTTP_PROXY", "http://123.456.789.012:8888")

	proxy := "http://999.888.777.666:5555"
	proxyFunc, err := ParseProxy(proxy)
	require.NoError(t, err)

	targetUrl, err := url.Parse("https://example.com")
	require.NoError(t, err)

	req := &http.Request{
		URL: targetUrl,
	}
	proxyURL, err := proxyFunc(req)
	require.NoError(t, err)
	require.NotNil(t, proxyURL)
	require.Equal(t, proxy, proxyURL.String())
}

func TestGetProxyFuncExplicitProxyWithoutScheme(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://123.456.789.012:8080")
	t.Setenv("HTTP_PROXY", "http://123.456.789.012:8888")

	proxyFunc, err := ParseProxy("squid:1234")
	require.NoError(t, err)

	targetUrl, err := url.Parse("https://example.com")
	require.NoError(t, err)

	req := &http.Request{
		URL: targetUrl,
	}
	proxyURL, err := proxyFunc(req)
	require.NoError(t, err)
	require.NotNil(t, proxyURL)
	require.Equal(t, "http://squid:1234", proxyURL.String())
}

func TestGetProxyFuncWithDataPlaneProxy(t *testing.T) {
	dpProxy := "http://111.222.333.444:5555"
	dpClient, err := NewClient(&ClientOptions{ProxyString: dpProxy})
	require.NoError(t, err)

	dataPlaneUrl, err := url.Parse("https://dataplane.example.com")
	require.NoError(t, err)

	req := &http.Request{
		URL: dataPlaneUrl,
	}

	proxyURL, err := dpClient.Proxy(req)
	require.NoError(t, err)
	require.NotNil(t, proxyURL)
	require.Equal(t, dpProxy, proxyURL.String())
}

func TestClockSkewWarning(t *testing.T) {
	testCases := []struct {
		desc            string
		date            time.Time
		expectedWarning bool
	}{
		{
			desc:            "Clock skew warning expected",
			date:            time.Now().UTC().Add(time.Hour),
			expectedWarning: true,
		},
		{
			desc:            "No clock skew warning expected",
			date:            time.Now().UTC(),
			expectedWarning: false,
		},
	}

	for _, tC := range testCases {
		t.Run(tC.desc, func(t *testing.T) {
			c, err := NewClient(&ClientOptions{
				CreateTransport: func(next http.RoundTripper) http.RoundTripper {
					return &fixedTimeTransport{date: tC.date}
				},
			})

			require.NoError(t, err)

			errorBuf := bytes.Buffer{}
			ctx := zerolog.New(&errorBuf).WithContext(context.Background())
			req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodGet, "https://example.com", nil)
			require.NoError(t, err)
			resp, err := c.Do(req)
			require.NoError(t, err)
			require.Equal(t, http.StatusOK, resp.StatusCode)
			if tC.expectedWarning {
				require.Contains(t, errorBuf.String(), clockSkewWarning)
			} else {
				require.NotContains(t, errorBuf.String(), clockSkewWarning)
			}
		})
	}
}

type fixedTimeTransport struct {
	date time.Time
}

func (t *fixedTimeTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	resp := http.Response{
		Request:    req,
		StatusCode: http.StatusOK,
		Header: http.Header{
			"Date": []string{t.date.Format(http.TimeFormat)},
		},
	}
	return &resp, nil
}

func (t *fixedTimeTransport) GetUnderlyingTransport() *http.Transport {
	return http.DefaultTransport.(*http.Transport)
}
