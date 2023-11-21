package controlplane

import (
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

			si := &serviceInfo{}

			targetUrl, err := url.Parse("https://example.com")
			require.NoError(t, err)

			req := &http.Request{
				URL: targetUrl,
			}
			proxyURL, err := si.GetProxyFunc()(req)
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
			proxyURL, err = si.GetProxyFunc()(req)
			require.NoError(t, err)
			require.Nil(t, proxyURL)
		})
	}
}

func TestGetProxyFuncNoProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://123.456.789.012:8080")
	t.Setenv("HTTP_PROXY", "http://123.456.789.012:8888")

	si := &serviceInfo{
		Proxy: "none",
	}

	targetUrl, err := url.Parse("https://example.com")
	require.NoError(t, err)

	req := &http.Request{
		URL: targetUrl,
	}
	proxyURL, err := si.GetProxyFunc()(req)
	require.NoError(t, err)
	require.Nil(t, proxyURL)
}

func TestGetProxyFuncExplicitProxy(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://123.456.789.012:8080")
	t.Setenv("HTTP_PROXY", "http://123.456.789.012:8888")

	si := &serviceInfo{
		Proxy: "http://999.888.777.666:5555",
	}

	require.Nil(t, validateServiceInfo(si))

	targetUrl, err := url.Parse("https://example.com")
	require.NoError(t, err)

	req := &http.Request{
		URL: targetUrl,
	}
	proxyURL, err := si.GetProxyFunc()(req)
	require.NoError(t, err)
	require.NotNil(t, proxyURL)
	require.Equal(t, si.Proxy, proxyURL.String())
}

func TestGetProxyFuncExplicitProxyWithoutScheme(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://123.456.789.012:8080")
	t.Setenv("HTTP_PROXY", "http://123.456.789.012:8888")

	si := &serviceInfo{
		Proxy: "squid:1234",
	}

	require.Nil(t, validateServiceInfo(si))

	targetUrl, err := url.Parse("https://example.com")
	require.NoError(t, err)

	req := &http.Request{
		URL: targetUrl,
	}
	proxyURL, err := si.GetProxyFunc()(req)
	require.NoError(t, err)
	require.NotNil(t, proxyURL)
	require.Equal(t, "http://squid:1234", proxyURL.String())
}

func TestGetProxyFuncExplicitProxyWithDataPlaneProxy(t *testing.T) {
	si := &serviceInfo{
		ServerUri:      "https://example.com",
		Proxy:          "http://999.888.777.666:5555",
		DataPlaneProxy: "http://111.222.333.444:5555",
	}

	require.Nil(t, validateServiceInfo(si))

	controlPlaneUrl, err := url.Parse("https://example.com")
	require.NoError(t, err)

	req := &http.Request{
		URL: controlPlaneUrl,
	}
	proxyURL, err := si.GetProxyFunc()(req)
	require.NoError(t, err)
	require.NotNil(t, proxyURL)
	require.Equal(t, si.Proxy, proxyURL.String())

	dataPlaneUrl, err := url.Parse("https://dataplane.example.com")
	require.NoError(t, err)
	req.URL = dataPlaneUrl
	proxyURL, err = si.GetProxyFunc()(req)
	require.NoError(t, err)
	require.NotNil(t, proxyURL)
	require.Equal(t, si.DataPlaneProxy, proxyURL.String())

}
