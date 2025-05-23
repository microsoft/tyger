// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"runtime"
	"strings"

	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/spf13/cobra"
	"github.com/spf13/pflag"
	"sigs.k8s.io/yaml"
)

func NewLoginCommand() *cobra.Command {
	optionsFilePath := ""
	options := controlplane.LoginConfig{
		Persisted: true,
	}

	local := false

	loginCmd := &cobra.Command{
		Use:   "login { SERVER_URL [--service-principal APPID --certificate CERTPATH] [--use-device-code] [--proxy PROXY] } | --file LOGIN_FILE.yaml | --local",
		Short: "Login to a server",
		Long: `Login to the Tyger server at the given URL.
Subsequent commands will be performed against this server.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if local {
				var err error
				cmd.LocalFlags().VisitAll(func(f *pflag.Flag) {
					if f.Changed && err == nil {
						switch f.Name {
						case "local", "proxy":
						default:
							err = fmt.Errorf("the options --local and --%s cannot be used together", f.Name)
						}
					}
				})
				if err != nil {
					return err
				}

				if len(args) > 0 {
					return errors.New("--local cannot be specified with a server address")
				}

				options.ServerUrl = controlplane.LocalUrlSentinel
				_, err = controlplane.Login(cmd.Context(), options)
				return err
			}

			switch len(args) {
			case 0:
				if optionsFilePath == "" {
					return errors.New("either a server URL, --file, or --local must be specified")
				}

				optionsFilePath, err := filepath.Abs(optionsFilePath)
				if err != nil {
					return fmt.Errorf("failed to resolve login options file path: %v", err)
				}

				bytes, err := os.ReadFile(optionsFilePath)
				if err != nil {
					return fmt.Errorf("failed to read login options file: %v", err)
				}

				if err := yaml.UnmarshalStrict(bytes, &options); err != nil {
					return fmt.Errorf("failed to parse login options file: %v", err)
				}

				if options.ServerUrl == "" {
					return errors.New("serverUrl must be specified")
				}

				if options.CertificateThumbprint != "" && runtime.GOOS != "windows" {
					return errors.New("certificateThumbprint is not supported on this platform")
				}

				if options.ServicePrincipal != "" {
					if runtime.GOOS == "windows" {
						if options.CertificatePath == "" && options.CertificateThumbprint == "" {
							return errors.New("certificatePath or certificateThumbprint must be specified with when servicePrincipal is specified")
						}

						if options.CertificatePath != "" && options.CertificateThumbprint != "" {
							return errors.New("certificatePath and certificateThumbprint cannot both be specified")
						}

					} else if options.CertificatePath == "" {
						return errors.New("certificateThumbprint must be specified with when servicePrincipal is specified")
					}
				} else {
					if options.CertificatePath != "" {
						return errors.New("certificatePath can only be used when servicePrincipal is specified")
					}
					if options.CertificateThumbprint != "" {
						return errors.New("certificateThumbprint can only be used when servicePrincipal is specified")
					}
				}

				if options.CertificatePath != "" && !filepath.IsAbs(options.CertificatePath) {
					// The certificate path is relative to the login options file.
					options.CertificatePath = filepath.Clean(filepath.Join(filepath.Dir(optionsFilePath), options.CertificatePath))
				}

				_, err = controlplane.Login(cmd.Context(), options)
				return err
			case 1:
				if options.ServicePrincipal != "" {
					if runtime.GOOS == "windows" {
						if options.CertificatePath == "" && options.CertificateThumbprint == "" {
							return errors.New("--cert-file or --cert-thumbprint must be specified with --service-principal")
						}
					} else if options.CertificatePath == "" {
						return errors.New("--cert-file must be specified with --service-principal")
					}

					if options.UseDeviceCode {
						return errors.New("--use-device-code cannot be used with --service-principal")
					}
				} else {
					if options.CertificatePath != "" {
						return errors.New("--cert-file can only be used with --service-principal")
					}
					if options.CertificateThumbprint != "" {
						return errors.New("--cert-thumbprint can only be used with --service-principal")
					}
				}

				options.ServerUrl = args[0]
				_, err := controlplane.Login(cmd.Context(), options)
				return err
			default:
				return errors.New("too many arguments")
			}
		},
	}

	loginCmd.AddCommand(newLoginStatusCommand())

	loginCmd.Flags().StringVarP(&optionsFilePath, "file", "f", "", `The path to a file containing login options. It should be a YAML file with the following structure:

# The Tyger server URL
serverUrl: https://example.com

# The service principal ID
servicePrincipal: api://my-client

# The path to a file with the service principal certificate
certificatePath: /a/path/to/a/file.pem

# The thumbprint of a certificate in a Windows certificate store to use for service principal authentication (Windows only)
certificateThumbprint: 92829BFAEB67C738DECE0B255C221CF9E1A46285

# The HTTP proxy to use. Can be 'auto[matic]', 'none', or a URL. The default is 'auto'.
proxy: auto
	`)

	loginCmd.Flags().StringVarP(&options.ServicePrincipal, "service-principal", "s", "", "The service principal app ID or identifier URL")
	loginCmd.Flags().StringVarP(&options.CertificatePath, "cert-file", "c", "", "The path to the certificate in PEM format to use for service principal authentication")

	if runtime.GOOS == "windows" {
		loginCmd.Flags().StringVarP(&options.CertificateThumbprint, "cert-thumbprint", "t", "", "The thumbprint of a certificate in a Windows certificate store to use for service principal authentication")
		loginCmd.MarkFlagsMutuallyExclusive("cert-file", "cert-thumbprint")
	}

	loginCmd.Flags().BoolVarP(&options.UseDeviceCode, "use-device-code", "d", false, "Whether to use the device code flow for user logins. Use this mode when the app can't launch a browser on your behalf.")

	loginCmd.Flags().StringVar(&options.Proxy, "proxy", "auto", "The HTTP proxy to use. Can be 'auto[matic]', 'none', or a URL.")

	loginCmd.Flags().BoolVar(&options.DisableTlsCertificateValidation, "disable-tls-certificate-validation", false, "Disable TLS certificate validation.")
	loginCmd.Flags().MarkHidden("disable-tls-certificate-validation")

	loginCmd.Flags().BoolVar(&local, "local", false, "Login to  local Tyger server. Cannot be used with other flags.")

	return loginCmd
}

func newLoginStatusCommand() *cobra.Command {
	outputFormat := OutputFormatUnspecified

	stausCmd := &cobra.Command{
		Use:                   "status",
		Short:                 "Get the login status",
		Long:                  `Get the login status.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			tygerClient, err := controlplane.GetClientFromCache()

			if err != nil || tygerClient.ControlPlaneUrl == nil {
				return errors.New("run 'tyger login' to connect to a Tyger server")
			}

			_, err = tygerClient.GetAccessToken(cmd.Context())
			if err != nil {
				return fmt.Errorf("run `tyger login` to login to a server: %v", err)
			}

			service := *tygerClient.RawControlPlaneUrl

			if service.Scheme == "http+unix" {
				service.Scheme = "unix"
				service.Path = strings.TrimSuffix(service.Path, ":")
			}

			switch outputFormat {
			case OutputFormatUnspecified, OutputFormatText:
				if tygerClient.Principal == "" {
					fmt.Printf("You are logged in to %s", service.String())
				} else {
					fmt.Printf("You are logged in to %s as %s", service.String(), tygerClient.Principal)
				}

				if tygerClient.RawProxy != nil {
					fmt.Printf(" using proxy server %s", tygerClient.RawProxy.String())
				}
				fmt.Println()

				return nil
			case OutputFormatJson:
				result := loginStatusResult{
					ServerUrl: service.String(),
					Principal: tygerClient.Principal,
				}
				if tygerClient.RawProxy != nil {
					result.Proxy = tygerClient.RawProxy.String()
				}
				if _, err := controlplane.InvokeRequest(cmd.Context(), http.MethodGet, "/metadata", nil, nil, &result.Metadata); err != nil {
					return fmt.Errorf("failed to get service metadata: %v", err)
				}

				enc := json.NewEncoder(os.Stdout)
				enc.SetIndent("", "  ")
				return enc.Encode(result)
			default:
				panic(fmt.Sprintf("unexpected output format: %v", outputFormat))
			}
		},
	}

	addOutputFormatFlag(stausCmd, &outputFormat)
	return stausCmd
}

type loginStatusResult struct {
	ServerUrl string                `json:"serverUrl"`
	Principal string                `json:"principal,omitempty"`
	Proxy     string                `json:"proxy,omitempty"`
	Metadata  model.ServiceMetadata `json:"metadata"`
}

func NewLogoutCommand() *cobra.Command {
	return &cobra.Command{
		Use:                   "logout",
		Short:                 "Logout from a server",
		Long:                  `Logout from a server.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			return controlplane.Logout()
		},
	}
}
