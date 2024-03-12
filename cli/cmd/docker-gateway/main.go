package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"os"

	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/cmd"
	"github.com/microsoft/tyger/cli/internal/proxy"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

var (
	// set during build
	version = ""
)

func main() {
	err := newRootCommand().Execute()
	if err != nil {
		os.Exit(1)
	}
}

func newRootCommand() *cobra.Command {
	rootCommand := cmd.NewCommonRootCommand(version)
	rootCommand.Use = "docker-gateway"
	rootCommand.Short = "Opens an HTTP listener that proxies requests to listeners on Unix domain sockets."

	rootCommand.Run = func(cmd *cobra.Command, args []string) {
		server := &http.Server{
			Handler: http.HandlerFunc(handleRequest),
			// Disable HTTP/2.
			TLSNextProto: make(map[string]func(*http.Server, *tls.Conn, http.Handler)),
		}

		l, err := net.Listen("tcp", "localhost:6777")
		if err != nil {
			log.Fatal().Err(err).Msg("failed to listen on port")
		}

		log.Info().Msgf("Listening on %s", l.Addr())

		if err := server.Serve(l); err != nil {
			log.Fatal().Err(err).Msg("failed to start server")
		}
	}

	return rootCommand
}

func handleRequest(w http.ResponseWriter, r *http.Request) {
	proxyReq := r.Clone(r.Context())
	proxyReq.RequestURI = "" // need to clear this since the instance will be used for a new request

	decodedSocketPath, err := client.DecodeUnixPathFromHost(proxyReq.Host)

	log.Info().Str("socket", decodedSocketPath).Str("method", proxyReq.Method).Str("path", proxyReq.URL.Path).Msg("Handling request")
	if err != nil {
		log.Ctx(r.Context()).Error().Err(err).Msg("Failed to decode socket path")
		http.Error(w, "Bad request", http.StatusBadRequest)
		return
	}

	proxyReq.URL.Scheme = "http+unix"
	proxyReq.URL.Host = ""
	proxyReq.Host = ""
	proxyReq.URL.Path = string(decodedSocketPath) + ":" + proxyReq.URL.Path

	resp, err := client.DefaultRetryableClient().HTTPClient.Transport.RoundTrip(proxyReq)
	if err != nil {
		// TODO: handle socket write error!
		log.Ctx(r.Context()).Error().Err(err).Msg("Failed to forward request")
		http.Error(w, "Bad Gateway", http.StatusBadGateway)
		return
	}

	copyHeaders(w.Header(), resp.Header)
	w.WriteHeader(resp.StatusCode)

	if err := proxy.CopyResponse(w, resp); err != nil {
		log.Ctx(r.Context()).Warn().Err(err).Msg("Failure while copying response")
	}
}

func copyHeaders(dst, src http.Header) {
	for k, vv := range src {
		dst[k] = vv
	}
}
