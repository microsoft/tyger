// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package proxy

import (
	"context"
	"io"
	"net/http"

	pool "github.com/libp2p/go-buffer-pool"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/rs/zerolog/log"
)

func HandleUDSProxyRequest(ctxOverride context.Context, w http.ResponseWriter, r *http.Request) {
	if ctxOverride == nil {
		ctxOverride = r.Context()
	}

	proxyReq := r.Clone(ctxOverride)
	proxyReq.RequestURI = "" // need to clear this since the instance will be used for a new request

	decodedSocketPath, err := client.DecodeUnixPathFromHost(proxyReq.Host)
	if err == nil {
		proxyReq.URL.Scheme = "http+unix"
		proxyReq.URL.Host = ""
		proxyReq.Host = ""
		proxyReq.URL.Path = string(decodedSocketPath) + ":" + proxyReq.URL.Path
	}

	log.Info().Str("url", client.RedactUrl(proxyReq.URL).String()).Msg("Proxying request")

	resp, err := client.DefaultRetryableClient().HTTPClient.Transport.RoundTrip(proxyReq)
	if err != nil {
		// TODO: handle socket write error!
		log.Ctx(r.Context()).Error().Err(err).Msg("Failed to forward request")
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if err := CopyResponse(w, resp); err != nil {
		log.Ctx(r.Context()).Warn().Err(err).Msg("Failure while copying response")
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		dst[k] = vv
	}
}

func CopyResponse(w http.ResponseWriter, resp *http.Response) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		// The ResponseWriter doesn't support flushing, fallback to simple copy
		_, err := io.Copy(w, resp.Body)
		return err
	}

	// Copy with flushing whenever there is data so that a trickle of data does not get buffered
	// and result in high latency

	buf := pool.Get(32 * 1024)
	defer func() {
		pool.Put(buf)
	}()

	for {
		n, err := resp.Body.Read(buf)
		if n > 0 {
			w.Write(buf[:n])
			flusher.Flush()
		}
		if err != nil {
			if err != io.EOF {
				return err
			}
			return nil
		}
	}
}
