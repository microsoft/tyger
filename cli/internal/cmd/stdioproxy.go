package cmd

import (
	"errors"
	"fmt"
	"net/http"
	"os"

	"github.com/loft-sh/devpod/pkg/stdio"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/proxy"
	"github.com/spf13/cobra"
)

func NewStdioProxyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:    "stdio-proxy",
		Short:  "Start a proxy to the stdin/stdout of a container",
		Hidden: true,
		Args:   cobra.ExactArgs(0),
		Run: func(cmd *cobra.Command, args []string) {
			server := http.Server{
				Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					// The HTTP server implementation will cancel r.Context() as the input reaches EOF.
					// Normally, this means the network connection has been closed, but in this case it just
					// means that it's the last HTTP request coming over stdin.
					// There may be a better way to handle this, but will just call handle with a new context
					proxy.HandleUDSProxyRequest(cmd.Context(), w, r)
				}),
			}

			listener := stdio.NewStdioListener(os.Stdin, os.Stdout, true)
			server.Serve(listener)
		},
	}

	cmd.AddCommand(newStdioProxyLoginCommand())
	return cmd
}

func newStdioProxyLoginCommand() *cobra.Command {
	serverUrl := client.DefaultControlPlaneUnixSocketUrl
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

			resp, err := client.NewRetryableClient().Get(parsedServerUrl.JoinPath("v1/metadata").String())
			if err != nil {
				return err
			}

			if resp.StatusCode != 200 {
				return errors.New("unexpected status code")
			}

			fmt.Println(parsedServerUrl.String())

			return nil
		},
	}

	cmd.Flags().StringVar(&serverUrl, "server-url", serverUrl, "The URL of the Tyger server to connect to")
	cmd.Flags().BoolVar(&preflight, "preflight", preflight, "Do nothing")
	return cmd
}
