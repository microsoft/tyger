package httpclient

import (
	"net/http"
	"net/url"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/mattn/go-ieproxy"
	"github.com/rs/zerolog/log"
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

	innerProxyFunc := ieproxy.GetProxyFunc()
	client.HTTPClient.Transport.(*http.Transport).Proxy = func(r *http.Request) (*url.URL, error) {
		url, err := innerProxyFunc(r)
		if err != nil {
			log.Error().Str("url", r.URL.String()).Err(err).Msg("Failed to retrieve proxy settings")
		}
		if url != nil {
			log.Trace().Str("url", r.URL.String()).Msgf("Using proxy %s", url.String())
		} else {
			log.Trace().Str("url", r.URL.String()).Msg("Not using any proxy")
		}
		return url, err
	}

	return client.StandardClient()
}
