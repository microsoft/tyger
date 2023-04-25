package controlplane

import (
	"net/http"

	"github.com/hashicorp/go-retryablehttp"
)

func NewRetryableClient() *http.Client {
	client := retryablehttp.NewClient()
	client.Logger = nil
	client.RetryMax = 6
	client.ErrorHandler = func(resp *http.Response, err error, numTries int) (*http.Response, error) {
		return resp, err
	}
	return client.StandardClient()
}
