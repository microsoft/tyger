// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package tygerproxy

import (
	"bufio"
	"context"
	"crypto/tls"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strconv"
	"sync"
	"sync/atomic"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/hashicorp/go-cleanhttp"
	pool "github.com/libp2p/go-buffer-pool"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type ProxyOptions struct {
	controlplane.LoginConfig
}

type ProxyServiceMetadata struct {
	model.ServiceMetadata
	ServerUrl string `json:"serverUrl"`
	LogPath   string `json:"logPath,omitempty"`
}

var (
	ErrProxyAlreadyRunning            = errors.New("the proxy is already running")
	ErrProxyAlreadyRunningWrongTarget = errors.New("the proxy is already running on the requested port, but targets a different server")
	ErrProxyNotRunning                = errors.New("the proxy is not running")
)

type CloseProxyFunc func() error

func RunProxy(ctx context.Context, tygerClient *client.TygerClient, options *ProxyOptions, logger zerolog.Logger) (CloseProxyFunc, error) {
	controlPlaneTargetUrl := tygerClient.ControlPlaneUrl
	handler := proxyHandler{
		tygerClient:           tygerClient,
		targetControlPlaneUrl: controlPlaneTargetUrl,
		options:               options,
		nextProxyFunc:         tygerClient.DataPlaneClient.Proxy,
	}

	r := chi.NewRouter()

	if len(options.AllowedClientCIDRs) > 0 {
		r.Use(createIpFilteringMidleware(options))
	}

	r.Use(createRequestLoggerMiddleware())

	// tyger API group
	r.Group(func(r chi.Router) {
		r.Route("/", func(r chi.Router) {
			r.Route("/runs/{runId}", func(r chi.Router) {
				r.Get("/", handler.forwardControlPlaneRequest)
				r.Get("/logs", handler.forwardControlPlaneRequest)
			})
			r.Post("/buffers/{id}/access", handler.forwardControlPlaneRequest)
			r.Get("/metadata", handler.handleMetadataRequest)
		})
	})

	// data plane tunneling
	r.Connect("/", handler.handleTunnelRequest)

	r.NotFound(handler.handleUnsupportedRequest)
	r.MethodNotAllowed(handler.handleUnsupportedRequest)

	connectionIdCounter := atomic.Uint64{}

	server := &http.Server{
		Handler: r,
		BaseContext: func(l net.Listener) context.Context {
			return logger.WithContext(ctx)
		},
		// Disable HTTP/2.
		TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
		ConnContext: func(ctx context.Context, conn net.Conn) context.Context {
			return log.Ctx(ctx).With().Uint64("connectionId", connectionIdCounter.Add(1)).Logger().WithContext(ctx)
		},
	}

	l, err := net.Listen("tcp", fmt.Sprintf(":%d", options.Port))
	if err != nil {
		if opErr, ok := err.(*net.OpError); ok && opErr.Op == "listen" {
			// The port is already in use. Let's see if it's a proxy server.
			_, existingProxyErr := CheckProxyAlreadyRunning(options)
			if existingProxyErr == nil {
				return nil, ErrProxyAlreadyRunning
			}
			if existingProxyErr == ErrProxyAlreadyRunningWrongTarget {
				return nil, ErrProxyAlreadyRunningWrongTarget
			}
		}
		return nil, err
	}

	_, port, _ := net.SplitHostPort(l.Addr().String())
	options.Port, _ = strconv.Atoi(port)

	go func() {
		err := server.Serve(l)
		if err != nil && err != http.ErrServerClosed {
			logger.Fatal().Err(err).Msg("proxy failed")
		}
	}()

	return func() error { return server.Close() }, nil
}

func CheckProxyAlreadyRunning(options *ProxyOptions) (*ProxyServiceMetadata, error) {
	if options.ServerUrl == "" {
		panic("ServerUrL must be set")
	}

	existingProxy := GetExistingProxyMetadata(options)
	if existingProxy == nil {
		return existingProxy, ErrProxyNotRunning
	}

	if existingProxy.ServerUrl != options.ServerUrl {
		return existingProxy, ErrProxyAlreadyRunningWrongTarget
	}

	return existingProxy, nil
}

func GetExistingProxyMetadata(options *ProxyOptions) *ProxyServiceMetadata {
	// note: not using retryablehttp here because we are hitting localhost
	// and we want to fail quickly
	resp, err := cleanhttp.DefaultClient().Get(fmt.Sprintf("http://localhost:%d/metadata", options.Port))
	if err == nil && resp.StatusCode == http.StatusOK {
		metadata := ProxyServiceMetadata{}
		err = json.NewDecoder(resp.Body).Decode(&metadata)
		if err == nil && metadata.DataPlaneProxy != "" && metadata.Audience == "" && metadata.Authority == "" {
			return &metadata
		}
	}

	return nil
}

type proxyHandler struct {
	tygerClient           *client.TygerClient
	targetControlPlaneUrl *url.URL
	options               *ProxyOptions
	nextProxyFunc         func(*http.Request) (*url.URL, error)
}

func (h *proxyHandler) handleMetadataRequest(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusOK)
	dataPlaneProxyUrl := url.URL{Host: r.Host}

	if r.TLS == nil {
		dataPlaneProxyUrl.Scheme = "http"
	} else {
		dataPlaneProxyUrl.Scheme = "https"
	}

	metadata := ProxyServiceMetadata{
		ServiceMetadata: model.ServiceMetadata{
			DataPlaneProxy: dataPlaneProxyUrl.String(),
		},
		ServerUrl: h.targetControlPlaneUrl.String(),
		LogPath:   h.options.LogPath,
	}
	if err := json.NewEncoder(w).Encode(metadata); err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("unable to write metadata response")
	}
}

func (h *proxyHandler) forwardControlPlaneRequest(w http.ResponseWriter, r *http.Request) {
	proxyReq := r.Clone(r.Context())
	proxyReq.RequestURI = "" // need to clear this since the instance will be used for a new request
	proxyReq.URL.Scheme = h.targetControlPlaneUrl.Scheme
	proxyReq.URL.Host = h.targetControlPlaneUrl.Host
	proxyReq.Host = h.targetControlPlaneUrl.Host
	if h.targetControlPlaneUrl.Path != "" {
		p := proxyReq.URL.Path
		proxyReq.URL.Path = "/"
		proxyReq.URL = proxyReq.URL.JoinPath(h.targetControlPlaneUrl.Path, p)
	}

	token, err := h.tygerClient.GetAccessToken(r.Context())

	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Send()
		http.Error(w, "failed to get access token", http.StatusInternalServerError)
		return
	}

	proxyReq.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	resp, err := h.tygerClient.ControlPlaneClient.HTTPClient.Transport.RoundTrip(proxyReq)
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("Failed to forward request")
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if err := copyResponse(w, resp); err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("error copying response")
	}
}

func (h *proxyHandler) handleUnsupportedRequest(w http.ResponseWriter, r *http.Request) {
	w.WriteHeader(http.StatusUnauthorized)
	errorResponse := model.ErrorResponse{
		Error: model.ErrorInfo{
			Code:    "Unauthorized",
			Message: "The operation cannot be proxied.",
		},
	}

	if err := json.NewEncoder(w).Encode(errorResponse); err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("Unable to write error body")
	}
}

func createIpFilteringMidleware(options *ProxyOptions) func(http.Handler) http.Handler {
	allowedCIDRs := make([]*net.IPNet, 0, len(options.AllowedClientCIDRs))
	for _, cidr := range options.AllowedClientCIDRs {
		_, ipNet, err := net.ParseCIDR(cidr)
		if err != nil {
			log.Fatal().Err(err).Msgf("invalid CIDR %s", cidr)
		}

		allowedCIDRs = append(allowedCIDRs, ipNet)
	}

	return func(next http.Handler) http.Handler {
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			remoteIP, _, err := net.SplitHostPort(r.RemoteAddr)
			if err != nil {
				log.Error().Err(err).Msg("invalid remote address")
				http.Error(w, "Invalid remote address", http.StatusBadRequest)
				return
			}

			ip := net.ParseIP(remoteIP)
			if ip == nil {
				log.Error().Err(err).Msg("invalid remote IP address")
				http.Error(w, "Invalid remote IP address", http.StatusBadRequest)
				return
			}

			allowed := false
			for _, cidr := range allowedCIDRs {
				if cidr.Contains(ip) {
					allowed = true
					break
				}
			}

			if !allowed {
				// The metadata endpoint is allowed to be called from a loopback address
				// because `tyger-proxy start` relies on being able to call it
				if ip.IsLoopback() && r.URL.Path == "/metadata" {
					allowed = true
				}
			}

			if !allowed {
				log.Ctx(r.Context()).Error().Err(err).IPAddr("ip", ip).Msg("remote IP address not allowed")
				http.Error(w, "Forbidden", http.StatusForbidden)
				return
			}

			next.ServeHTTP(w, r)
		})
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		dst[k] = vv
	}
}

func createRequestLoggerMiddleware() func(http.Handler) http.Handler {
	return func(next http.Handler) http.Handler {
		fn := func(rw http.ResponseWriter, r *http.Request) {
			ww := middleware.NewWrapResponseWriter(rw, r.ProtoMajor)
			start := time.Now().UTC()
			defer func() {
				log.Ctx(r.Context()).Info().
					Int("status", ww.Status()).
					Str("method", r.Method).
					Str("url", r.URL.String()).
					Float32("latencyMs", float32(time.Since(start).Microseconds())/1000.0).
					Msg("Request handled")
			}()

			next.ServeHTTP(ww, r)
		}
		return http.HandlerFunc(fn)
	}
}

func (h *proxyHandler) handleTunnelRequest(w http.ResponseWriter, r *http.Request) {
	// Determine if the request is to be forwarded through another proxy

	// The get proxy func looks at the scheme, which will currently be empty, so we set it.
	r.URL.Scheme = "https"
	var err error
	nextProxyUrl, err := h.nextProxyFunc(r)
	if err != nil {
		log.Error().Err(err).Msg("Unable to resolve next proxy URL for request")
		http.Error(w, "Unable to resolve proxy", http.StatusServiceUnavailable)
		return
	}
	var destConn net.Conn
	if nextProxyUrl != nil {
		destConn, err = openTunnel(nextProxyUrl.Host, r.URL)
		if err != nil {
			log.Ctx(r.Context()).Warn().Err(err).Msg("Failed to dial proxy")
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
	} else {
		destConn, err = net.DialTimeout("tcp", r.Host, 10*time.Second)
		if err != nil {
			log.Ctx(r.Context()).Warn().Err(err).Msg("Failed to dial host")
			http.Error(w, err.Error(), http.StatusServiceUnavailable)
			return
		}
	}

	w.WriteHeader(http.StatusOK)
	hijacker, ok := w.(http.Hijacker)
	if !ok {
		log.Ctx(r.Context()).Error().Msg("Attempted to hijack connection that does not support it")
		http.Error(w, "Not supported", http.StatusInternalServerError)
		return
	}
	clientConn, _, err := hijacker.Hijack()
	if err != nil {
		_ = destConn.Close()
		log.Ctx(r.Context()).Error().Err(err).Msg("Failed to hijack connection")
		http.Error(w, err.Error(), http.StatusServiceUnavailable)
	}
	wg := sync.WaitGroup{}
	wg.Add(2)
	go transfer(destConn, clientConn, &wg)
	go transfer(clientConn, destConn, &wg)
	go func() {
		wg.Wait()
		log.Ctx(r.Context()).Info().Msg("CONNECT completed")
	}()
}

// Returns a connection that is created after successfully calling CONNECT
// on another proxy server
func openTunnel(proxyAddress string, destination *url.URL) (net.Conn, error) {
	connectReq := &http.Request{
		Method: http.MethodConnect,
		URL:    destination,
		Host:   destination.Host,
	}

	c, err := net.DialTimeout("tcp", proxyAddress, 10*time.Second)
	if err != nil {
		return nil, err
	}
	if err := connectReq.Write(c); err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("unable to send CONNECT request: %w", err)
	}
	br := bufio.NewReader(c)
	resp, err := http.ReadResponse(br, connectReq)
	if err != nil {
		_ = c.Close()
		return nil, fmt.Errorf("unable to send CONNECT request: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		_ = c.Close()
		return nil, fmt.Errorf("received unexpected status from CONNECT request: %s", resp.Status)
	}

	return c, nil
}

func transfer(destination io.WriteCloser, source io.ReadCloser, wg *sync.WaitGroup) {
	defer wg.Done()
	defer destination.Close()
	defer source.Close()
	io.Copy(destination, source)
}

func copyResponse(w http.ResponseWriter, resp *http.Response) error {
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
