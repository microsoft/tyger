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
		Use:   "login { SERVER_URL [--service-principal APPID --certificate CERTPATH] [--use-device-code] [--identity [--identity-client-id [CLIENT_ID]] --federated-identity CLIENT_ID] [--proxy PROXY] } | --file LOGIN_FILE.yaml | --local",
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
							return errors.New("when servicePrincipal is set on Windows, either certificatePath or certificateThumbprint must be specified")
						}

						if options.CertificatePath != "" && options.CertificateThumbprint != "" {
							return errors.New("certificatePath and certificateThumbprint cannot both be specified")
						}
					} else if options.CertificatePath == "" {
						return errors.New("when servicePrincipal is set, certificatePath must be specified on non-Windows platforms")
					}

					if options.UseDeviceCode {
						return errors.New("useDeviceCode cannot be used when servicePrincipal is set")
					}

					if options.ManagedIdentity {
						return errors.New("managedIdentity cannot be used when servicePrincipal is set")
					}

					if options.ManagedIdentityClientId != "" {
						return errors.New("managedIdentityClientId can only be set when managedIdentity is used")
					}

					if options.TargetFederatedIdentity != "" {
						return errors.New("targetFederatedIdentity can only be set when managedIdentity or github is used")
					}
				} else if options.ManagedIdentity {
					if options.UseDeviceCode {
						return errors.New("useDeviceCode cannot be used when managedIdentity is set")
					}

					if options.CertificatePath != "" {
						return errors.New("certificatePath cannot be set when managedIdentity is used")
					}

					if options.CertificateThumbprint != "" {
						return errors.New("certificateThumbprint cannot be set when managedIdentity is used")
					}
				} else if options.GitHub {
					if options.TargetFederatedIdentity == "" {
						return errors.New("targetFederatedIdentity must be specified when github is used")
					}

					if options.UseDeviceCode {
						return errors.New("useDeviceCode cannot be used when github is set")
					}

					if options.CertificatePath != "" {
						return errors.New("certificatePath cannot be set when github is used")
					}

					if options.CertificateThumbprint != "" {
						return errors.New("certificateThumbprint cannot be set when github is used")
					}
				} else {
					if options.CertificatePath != "" {
						return errors.New("certificatePath can only be set when servicePrincipal is used")
					}

					if options.CertificateThumbprint != "" {
						return errors.New("certificateThumbprint can only be set when servicePrincipal is used")
					}

					if options.ManagedIdentityClientId != "" {
						return errors.New("managedIdentityClientId can only be set when managedIdentity is used")
					}

					if options.TargetFederatedIdentity != "" {
						return errors.New("targetFederatedIdentity can only be set when managedIdentity or github is used")
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
						if options.CertificatePath != "" && options.CertificateThumbprint != "" {
							return errors.New("--cert-file and --cert-thumbprint cannot both be specified")
						}
					} else if options.CertificatePath == "" {
						return errors.New("--cert-file must be specified with --service-principal")
					}

					if options.UseDeviceCode {
						return errors.New("--use-device-code cannot be used with --service-principal")
					}
					if options.ManagedIdentity {
						return errors.New("--identity cannot be used with --service-principal")
					}
					if options.ManagedIdentityClientId != "" {
						return errors.New("--identity-client-id can only be used with --identity")
					}
					if options.TargetFederatedIdentity != "" {
						return errors.New("--federated-identity can only be used with --identity or --github")
					}
				} else if options.ManagedIdentity {
					if options.UseDeviceCode {
						return errors.New("--use-device-code cannot be used with --identity")
					}
					if options.CertificatePath != "" || options.CertificateThumbprint != "" {
						return errors.New("--cert-file and --cert-thumbprint cannot be used with --identity")
					}
					if options.CertificatePath != "" {
						return errors.New("--cert-file can only be used with --service-principal")
					}
					if options.CertificateThumbprint != "" {
						return errors.New("--cert-thumbprint can only be used with --service-principal")
					}
				} else if options.GitHub {
					if options.TargetFederatedIdentity == "" {
						return errors.New("--federated-identity must be specified with --github")
					}
					if options.UseDeviceCode {
						return errors.New("--use-device-code cannot be used with --github")
					}
					if options.CertificatePath != "" || options.CertificateThumbprint != "" {
						return errors.New("--cert-file and --cert-thumbprint cannot be used with --github")
					}
					if options.CertificatePath != "" {
						return errors.New("--cert-file can only be used with --service-principal")
					}
					if options.CertificateThumbprint != "" {
						return errors.New("--cert-thumbprint can only be used with --service-principal")
					}
				} else {
					if options.CertificatePath != "" {
						return errors.New("--cert-file can only be used with --service-principal")
					}
					if options.CertificateThumbprint != "" {
						return errors.New("--cert-thumbprint can only be used with --service-principal")
					}
					if options.ManagedIdentityClientId != "" {
						return errors.New("--identity-client-id can only be used with --identity")
					}
					if options.TargetFederatedIdentity != "" {
						return errors.New("--federated-identity can only be used with --identity or --github")
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

# The service principal ID.
servicePrincipal: api://my-client

# The path to a file with the service principal certificate.
# Can only be specified if servicePrincipal is set.
certificatePath: /a/path/to/a/file.pem

# The thumbprint of a certificate in a Windows certificate store to use for service principal authentication (Windows only)
# Can only be specified if servicePrincipal is set.
certificateThumbprint: 92829BFAEB67C738DECE0B255C221CF9E1A46285

# Whether to use Azure managed identity for authentication.
managedIdentity: false
managedIdentityClientId: # Optionally specify the client ID of the managed identity to use.

# Whether to use GitHub Actions tokens with federated identity for authentication.
github: false

# If using managed identity or GitHub Actions, specify the client ID of the federated identity to authenticate as.
targetFederatedIdentity: # Optionally specify a federated identity to authenticate as using the managed identity.

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

	loginCmd.Flags().BoolVar(&options.ManagedIdentity, "identity", false, "Use Azure Managed Identity for authentication. This is only supported in Azure environments.")
	loginCmd.Flags().StringVar(&options.ManagedIdentityClientId, "identity-client-id", "", "The client ID of the managed identity to use. If not specified, the system-assigned identity is used, or if not present, the single user-assigned identity.")

	loginCmd.Flags().BoolVar(&options.GitHub, "github", false, "Use GitHub Actions tokens with federated identity for authentication. Requires --federated-identity to be set.")

	loginCmd.Flags().StringVar(&options.TargetFederatedIdentity, "federated-identity", "", "Use federation to authenticate as this identity. Can only be used with --managed-identity or --github.")

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

			roles, _ := tygerClient.GetRoleAssignments(cmd.Context())

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
					fmt.Printf("You are logged in to %s as '%s'", service.String(), tygerClient.Principal)
				}

				if len(roles) > 0 {
					for i, role := range roles {
						roles[i] = fmt.Sprintf("'%s'", role)
					}

					switch len(roles) {
					case 1:
						fmt.Printf(" with role %s", roles[0])
					case 2:
						fmt.Printf(" with roles %s and %s", roles[0], roles[1])
					default:
						fmt.Printf(" with roles %s, and %s", strings.Join(roles[:len(roles)-1], ", "), roles[len(roles)-1])
					}
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
					Roles:     roles,
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
	Roles     []string              `json:"roles,omitempty"`
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
