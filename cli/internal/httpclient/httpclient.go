package httpclient

import (
	"net/http"
	"net/url"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/mattn/go-ieproxy"
)

var (
	DefaultRetryableClient = NewRetryableClient()
)

func GetProxyFunc() func(*http.Request) (*url.URL, error) {
	return ieproxy.GetProxyFunc()
}

func NewRetryableClient() *http.Client {
	client := retryablehttp.NewClient()
	client.Logger = nil
	client.RetryMax = 6
	client.ErrorHandler = func(resp *http.Response, err error, numTries int) (*http.Response, error) {
		return resp, err
	}
	client.HTTPClient.Transport.(*http.Transport).Proxy = GetProxyFunc()

	return client.StandardClient()
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
