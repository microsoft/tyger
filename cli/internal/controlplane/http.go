package controlplane

import (
	"net/http"
	"net/url"

	"github.com/hashicorp/go-retryablehttp"
	"github.com/mattn/go-ieproxy"
	"github.com/rs/zerolog/log"
)

func NewRetryableClient() *http.Client {
	client := retryablehttp.NewClient()
	client.Logger = nil
	client.RetryMax = 6
	client.ErrorHandler = func(resp *http.Response, err error, numTries int) (*http.Response, error) {
		return resp, err
	}

	client.HTTPClient.Transport.(*http.Transport).Proxy = func(r *http.Request) (*url.URL, error) {
		f := ieproxy.GetProxyFunc()
		url, err := f(r)
		if err != nil {
			log.Error().Err(err).Msg("Failed to retrieve proxy settings")
		}
		if url != nil {
			log.Trace().Msgf("Using proxy %s", url.String())
		} else {
			log.Trace().Msg("Not using any proxy")
		}
		return url, err
	}

	return client.StandardClient()
}
