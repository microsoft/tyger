// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cmd

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
)

func NewStdioProxyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "stdio-proxy",
		Short:  "An HTTP proxy to tyger using standard IO for the HTTP request/response streams.",
		Hidden: true,
		Args:   cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			bufIn := bufio.NewReader(os.Stdin)
			req, err := http.ReadRequest(bufIn)
			if err != nil {
				log.Fatal().Err(err).Msg("Failed to read request")
			}

			if req.URL.Scheme != "http+unix" {
				log.Fatal().Msg("Unsupported URL scheme")
			}

			tokens := strings.Split(req.URL.Path, ":")
			socketPath := tokens[0]

			req.Host = ""
			req.URL.Host = ""
			req.URL.Path = tokens[1]
			req.Header.Add("Connection", "Close")

			conn, err := net.Dial("unix", socketPath)
			if err != nil {
				log.Error().Err(err).Msg("Failed to connect to server")
				writeResponseForError(err)
				return
			}

			defer conn.Close()

			if err := req.Write(conn); err != nil {
				log.Fatal().Err(err).Msg("Failed to write request")
				writeResponseForError(err)
				return
			}

			if _, err := io.Copy(os.Stdout, conn); err != nil {
				log.Fatal().Err(err).Msg("Failed to copy response")
			}
		},
	}

	cmd.AddCommand(newStdioProxyLoginCommand())
	cmd.AddCommand(newStdioProxySleepCommand())
	return cmd
}

func writeResponseForError(err error) {
	log.Error().Err(err).Send()
	resp := http.Response{
		StatusCode: http.StatusBadGateway,
		Header:     http.Header{},
	}

	errorCode := ""
	if os.IsNotExist(err) || errors.Is(err, syscall.ENOENT) {
		errorCode = "ENOENT"
	} else if errors.Is(err, syscall.ECONNREFUSED) {
		errorCode = "ECONNREFUSED"
	} else {
		errorCode = err.Error()
	}

	resp.Header.Add("x-ms-error", errorCode)

	if err := resp.Write(os.Stdout); err != nil {
		log.Fatal().Err(err).Msg("Failed to write response")
	}
}

func newStdioProxyLoginCommand() *cobra.Command {
	serverUrl := client.GetDefaultSocketUrl()
	preflight := false
	cmd := &cobra.Command{
		Use:    "login",
		Short:  "Verify connectivity over the stdio-proxy command",
		Hidden: true,
		Args:   cobra.ExactArgs(0),
		RunE: func(cmd *cobra.Command, args []string) error {
			if preflight {
				return nil
			}

			parsedServerUrl, err := controlplane.NormalizeServerUri(serverUrl)
			if err != nil {
				return err
			}

			c, err := client.NewClient(&client.ClientOptions{ProxyString: "none", DisableRetries: true})
			if err != nil {
				return err
			}

			resp, err := c.Get(parsedServerUrl.JoinPath("metadata").String())
			if err != nil {
				return err
			}

			if resp.StatusCode != 200 {
				return errors.New("unexpected status code")
			}

			fmt.Print(parsedServerUrl.String())

			return nil
		},
	}

	cmd.Flags().StringVar(&serverUrl, "server-url", serverUrl, "The URL of the Tyger server to connect to")
	cmd.Flags().BoolVar(&preflight, "preflight", preflight, "Do nothing")
	return cmd
}

func newStdioProxySleepCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "sleep",
		Short:  "Sleep forever",
		Hidden: true,
		Args:   cobra.NoArgs,
		Run: func(cmd *cobra.Command, args []string) {
			for {
				time.Sleep(time.Hour)
			}

		},
	}

	return cmd
}
