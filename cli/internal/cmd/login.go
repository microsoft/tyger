// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"

	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

func NewLoginCommand() *cobra.Command {
	optionsFilePath := ""
	options := controlplane.LoginConfig{
		Persisted: true,
	}

	loginCmd := &cobra.Command{
		Use:   "login { SERVER_URL [--service-principal APPID --certificate CERTPATH] [--use-device-code] [--proxy PROXY] } | --file LOGIN_FILE.yaml",
		Short: "Login to a server",
		Long: `Login to the Tyger server at the given URL.
Subsequent commands will be performed against this server.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {

			switch len(args) {
			case 0:
				if optionsFilePath == "" {
					return errors.New("either a server URL or --file must be specified")
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

				if options.ServerUri == "" {
					return errors.New("serverUri must be specified")
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

				options.ServerUri = args[0]
				_, err := controlplane.Login(cmd.Context(), options)
				return err
			default:
				return errors.New("too many arguments")
			}
		},
	}

	loginCmd.AddCommand(newLoginStatusCommand())

	loginCmd.Flags().StringVarP(&optionsFilePath, "file", "f", "", `The path to a file containing login options. It should be a YAML file with the following structure:

# The Tyger server URI
serverUri: https://example.com

# The service principal ID
servicePrincipal: api://my-client

# The path to a file with the service principal certificate
certificatePath: /a/path/to/a/file.pem

# The thumbprint of a certificate in a Windows certificate store to use for service principal authentication (Windows only)
certificateThumbprint: 92829BFAEB67C738DECE0B255C221CF9E1A46285

# The HTTP proxy to use. Can be 'auto[matic]', 'none', or a URI. The default is 'auto'.
proxy: auto
	`)

	loginCmd.Flags().StringVarP(&options.ServicePrincipal, "service-principal", "s", "", "The service principal app ID or identifier URI")
	loginCmd.Flags().StringVarP(&options.CertificatePath, "cert-file", "c", "", "The path to the certificate in PEM format to use for service principal authentication")

	if runtime.GOOS == "windows" {
		loginCmd.Flags().StringVarP(&options.CertificateThumbprint, "cert-thumbprint", "t", "", "The thumbprint of a certificate in a Windows certificate store to use for service principal authentication")
		loginCmd.MarkFlagsMutuallyExclusive("cert-file", "cert-thumbprint")
	}

	loginCmd.Flags().BoolVarP(&options.UseDeviceCode, "use-device-code", "d", false, "Whether to use the device code flow for user logins. Use this mode when the app can't launch a browser on your behalf.")

	loginCmd.Flags().StringVar(&options.Proxy, "proxy", "auto", "The HTTP proxy to use. Can be 'auto[matic]', 'none', or a URI.")

	loginCmd.Flags().BoolVar(&options.DisableTlsCertificateValidation, "disable-tls-certificate-validation", false, "Disable TLS certificate validation.")
	loginCmd.Flags().MarkHidden("disable-tls-certificate-validation")

	return loginCmd
}

func newLoginStatusCommand() *cobra.Command {
	return &cobra.Command{
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

			principal := tygerClient.Principal
			if principal == "" {
				fmt.Printf("You are anonymously logged in to %s\n", tygerClient.ControlPlaneUrl)
			} else {
				fmt.Printf("You are logged in to %s as %s\n", tygerClient.ControlPlaneUrl, principal)
			}
			return nil
		},
	}
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
