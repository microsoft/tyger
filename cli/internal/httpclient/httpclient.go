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
