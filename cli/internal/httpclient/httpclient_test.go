package httpclient

// import (
// 	"net/http"
// 	"net/url"
// 	"testing"

// 	"github.com/stretchr/testify/require"
// )

// func TestGetProxyFuncWithHttp(t *testing.T) {
// 	proxyFunc := GetProxyFunc()

// 	targetUrl, err := url.Parse("http://example.com")
// 	require.NoError(t, err)

// 	// Test with http scheme
// 	req := &http.Request{
// 		URL: targetUrl,
// 	}
// 	proxyURL, err := proxyFunc(req)
// 	require.NoError(t, err)
// 	require.Nil(t, proxyURL)
// }

// func TestGetProxyFunc(t *testing.T) {
// 	t.Setenv("HTTPS_PROXY", "http://123.456.789.012:8080")
// 	t.Setenv("HTTP_PROXY", "http://123.456.789.012:8888")
// 	proxyFunc := GetProxyFunc()

// 	targetUrl, err := url.Parse("https://example.com")
// 	require.NoError(t, err)

// 	req := &http.Request{
// 		URL: targetUrl,
// 	}
// 	proxyURL, err := proxyFunc(req)
// 	require.NoError(t, err)
// 	require.NotNil(t, proxyURL)
// 	require.Equal(t, "http://123.456.789.012:8080", proxyURL.String())

// 	// now use HTTP, which should not use a proxy
// 	targetUrl, err = url.Parse("http://example.com")
// 	require.NoError(t, err)

// 	// Test with http scheme
// 	req = &http.Request{
// 		URL: targetUrl,
// 	}
// 	proxyURL, err = proxyFunc(req)
// 	require.NoError(t, err)
// 	require.Nil(t, proxyURL)

// }
