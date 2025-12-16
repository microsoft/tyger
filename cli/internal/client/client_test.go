// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package client

import (
	"bytes"
	"context"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

// Sample PEM certificate for testing (self-signed)
const testCertPEM = `-----BEGIN CERTIFICATE-----
MIIFBTCCAu2gAwIBAgIUQpZ8mfHKY4VpAoNzLgm/O/u8Hb0wDQYJKoZIhvcNAQEL
BQAwEjEQMA4GA1UEAwwHdGVzdGluZzAeFw0yNTEyMTYxNTIzNTBaFw0yNjEyMTYx
NTIzNTBaMBIxEDAOBgNVBAMMB3Rlc3RpbmcwggIiMA0GCSqGSIb3DQEBAQUAA4IC
DwAwggIKAoICAQCijGKAuxmzw+1xn6QoTRsHRuce3m6ZYcEkGao4OYgz0t0aGXpS
Ks+wB+kvOLU4bDJ/UnUa8m78lo3XWAYZkU8Sj9Y+9gaoPzBIYpMvKLWsELakFSqy
O3PQ/l4JOskD2To4hJXsZ9uxmrVqLGEBcI7zrk9TtIGS0cjoW+lMpNTMEdJV6Y+r
eJjHDxuf05JGcQEfjbCICgKNLoIQQkuFRojS4CMmeNVxl4FOwckWDcTnGRel0jaP
Ie8FFx1TQEPdx5hhTJjFuHDQWOvQMfZETHRm9bidK627zRtuV0Fz3+IUidPWBLa1
UAlq2eaH0gmI5EeojRkveFOuAM3v9cd7trNqv0UuVB5QMAMuKYj54YVYmvzqzzlv
2oJcZbhaxDTqAlvfBPcqQRdLghEBM/zfvfc2eYXFZMI86CoFjCxSmlHW0NToMKPI
dz4EsCW8cAZz0aoladjx05olQ1tL8i0OmsZuSmQ0EPAGaNUBBfhfP/NIM+0wgF40
LecSO3umbgL6GQ+CelEJo5K94KfmQBwAQwQfgaflhX20+8o0H+Se6XyKveYUt6Xv
V+KU/zqhDq2YCluFk1m2wFg3+iCW1jakRUXxzxwkP8rS73omZnxmkC0U+2PwF3sB
ekbQmUGyDRp0tmGKCqw9lxs6OUycYhwezlZfKv992lhBx+VPNz6iIrQ7YwIDAQAB
o1MwUTAdBgNVHQ4EFgQUPSwP8mHigUWWIWITPSJXN0QwgBowHwYDVR0jBBgwFoAU
PSwP8mHigUWWIWITPSJXN0QwgBowDwYDVR0TAQH/BAUwAwEB/zANBgkqhkiG9w0B
AQsFAAOCAgEAa3Io2CAYhOpJ5AjTFkDJnxfmdrOxdOb8wQ+EbBr3UBLlm4lDkJQP
4bSW1zQ+2lZPYeqbx+xU75izRyex9bakb2h8NO4Ct6GxDN9TajAw6qbPN1Y44Y+E
xr61hXWbnFYxd/j8AhxJA2lCRPeQ+bVIXfi03d/IDstIxJQxKxNAM45/5VZg1cIp
EiHTrCVXySSGB88CGcyZd9Qyc1KZXSaJojNSPTudT+hJnIUnzppfg6z9Cg4iiO9j
E7zjXHK2oTBiW9T7F2adhBWzWYXYBiFaBdwkDZc8yUuxBcdUCdvqKLkjgShvmnKp
I+VYZSMpPxrbbmNyX54/MCdN4JG7NYxwkVMZtyL7uRbUamLz56QhmsNkJsHo9II6
eVG2CVTbcWEVNCjFkEr9DwIgIyGXapBPkdMaUCaT8L1p4jdaDg+RcHTpTdVlJFcq
QlsgQ/UK8RPOEeRHNaSY4ffdiywuxNcRcAI3vnIhcdYVJ7/hTDWdJJGdGd2qIzMe
F/heBl0/YYQCPHzpAFdxUZ0JjxO0Rrd7SCQF/mWflkNv7bl7d9gguXWMhTEIynDO
5Nvfoz2a7b2yHph3oj1ExkjzzaDFWs+zXWqC9gYj2lW5UTJc66sS/A90aMykZPQ4
HhLayo5LKGZDeVBSGA8x541VeAWr4d13pHi/9k7GLtWnRdaWhmW9Txc=
-----END CERTIFICATE-----`

func TestGetCaCertPoolEmptySource(t *testing.T) {
	// Clear the cache to ensure clean test
	certPoolCache = sync.Map{}

	pool, err := getCaCertPool("")
	require.NoError(t, err)
	require.Nil(t, pool)
}

func TestGetCaCertPoolOSSource(t *testing.T) {
	// Clear the cache to ensure clean test
	certPoolCache = sync.Map{}

	pool, err := getCaCertPool(TlsCaCertificateSourceOperatingSystem)
	require.NoError(t, err)
	require.Nil(t, pool)
}

func TestGetCaCertPoolEmbeddedSource(t *testing.T) {
	// Clear the cache to ensure clean test
	certPoolCache = sync.Map{}

	pool, err := getCaCertPool(TlsCaCertificateSourceEmbedded)
	require.NoError(t, err)
	require.NotNil(t, pool)

	// Call again to verify caching works
	pool2, err := getCaCertPool(TlsCaCertificateSourceEmbedded)
	require.NoError(t, err)
	require.Equal(t, pool, pool2)
}

func TestGetCaCertPoolInlinePEM(t *testing.T) {
	// Clear the cache to ensure clean test
	certPoolCache = sync.Map{}

	pool, err := getCaCertPool(TlsCaCertificateSource(testCertPEM))
	require.NoError(t, err)
	require.NotNil(t, pool)

	// Verify the pool is cached
	cached, ok := certPoolCache.Load(TlsCaCertificateSource(testCertPEM))
	require.True(t, ok)
	require.Equal(t, pool, cached)
}

func TestGetCaCertPoolFromFile(t *testing.T) {
	// Clear the cache to ensure clean test
	certPoolCache = sync.Map{}

	// Create a temporary file with PEM content
	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "test-cert.pem")
	err := os.WriteFile(certFile, []byte(testCertPEM), 0644)
	require.NoError(t, err)

	pool, err := getCaCertPool(TlsCaCertificateSource(certFile))
	require.NoError(t, err)
	require.NotNil(t, pool)

	// Verify the pool is cached
	cached, ok := certPoolCache.Load(TlsCaCertificateSource(certFile))
	require.True(t, ok)
	require.Equal(t, pool, cached)
}

func TestGetCaCertPoolFileNotFound(t *testing.T) {
	// Clear the cache to ensure clean test
	certPoolCache = sync.Map{}

	pool, err := getCaCertPool(TlsCaCertificateSource("/nonexistent/path/to/cert.pem"))
	require.Error(t, err)
	require.Nil(t, pool)
	require.Contains(t, err.Error(), "failed to read CA certificate file")
}

func TestGetCaCertPoolInvalidPEMInFile(t *testing.T) {
	// Clear the cache to ensure clean test
	certPoolCache = sync.Map{}

	// Create a temporary file with invalid PEM content
	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "invalid-cert.pem")
	err := os.WriteFile(certFile, []byte("not a valid PEM"), 0644)
	require.NoError(t, err)

	pool, err := getCaCertPool(TlsCaCertificateSource(certFile))
	require.Error(t, err)
	require.Nil(t, pool)
	require.Contains(t, err.Error(), "invalid CA certificate data")
}

func TestGetCaCertPoolCaching(t *testing.T) {
	// Clear the cache to ensure clean test
	certPoolCache = sync.Map{}

	// Create a temporary file with PEM content
	tmpDir := t.TempDir()
	certFile := filepath.Join(tmpDir, "cached-cert.pem")
	err := os.WriteFile(certFile, []byte(testCertPEM), 0644)
	require.NoError(t, err)

	source := TlsCaCertificateSource(certFile)

	// First call should load from file
	pool1, err := getCaCertPool(source)
	require.NoError(t, err)
	require.NotNil(t, pool1)

	// Delete the file
	err = os.Remove(certFile)
	require.NoError(t, err)

	// Second call should return cached value even though file is gone
	pool2, err := getCaCertPool(source)
	require.NoError(t, err)
	require.NotNil(t, pool2)
	require.Equal(t, pool1, pool2)
}

func TestClientCert

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
