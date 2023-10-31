package httpclient

import (
	"net/http"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/mattn/go-ieproxy"
)

var (
	DefaultRetryableClient = NewRetryableClient()
)

func NewRetryableClient() *http.Client {
	client := retryablehttp.NewClient()
	client.Logger = nil
	client.RetryMax = 6
	client.ErrorHandler = func(resp *http.Response, err error, numTries int) (*http.Response, error) {
		return resp, err
	}
	client.HTTPClient.Transport.(*http.Transport).Proxy = ieproxy.GetProxyFunc()

	return client.StandardClient()
}
