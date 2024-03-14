package main

import (
	"crypto/tls"
	"net"
	"net/http"
	"os"

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
			Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				proxy.HandleUDSProxyRequest(r.Context(), w, r)
			}),
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
