// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package client

import (
	"context"
	"net/http"
	"net/url"
	"testing"

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

	controlPlaneUrl, err := url.Parse("https://example.com")
	require.NoError(t, err)
	dataPlaneProxy, err := url.Parse("http://111.222.333.444:5555")
	require.NoError(t, err)

	tygerClient := NewTygerClient(controlPlaneUrl, func(ctx context.Context) (string, error) { return "", nil }, dataPlaneProxy, "me")

	dataPlaneUrl, err := url.Parse("https://dataplane.example.com")
	require.NoError(t, err)

	req := &http.Request{
		URL: dataPlaneUrl,
	}

	proxyURL, err := GetHttpTransport(tygerClient.DataPlaneClient.HTTPClient).Proxy(req)
	require.NoError(t, err)
	require.NotNil(t, proxyURL)
	require.Equal(t, dataPlaneProxy.String(), proxyURL.String())

}
