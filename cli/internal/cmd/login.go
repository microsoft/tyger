package cmd

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/controlplane"
	"github.com/spf13/cobra"
	"gopkg.in/yaml.v3"
)

func NewLoginCommand() *cobra.Command {
	options := controlplane.LoginOptions{}
	optionsFilePath := ""

	loginCmd := &cobra.Command{
		Use:   "login { SERVER_URL [--service pricipal APPID --certificate CERTPATH] [--use-device-code] url } | --file LOGIN_FILE.yaml",
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

				if err := yaml.Unmarshal(bytes, &options); err != nil {
					return fmt.Errorf("failed to parse login options file: %v", err)
				}

				if options.ServerUri == "" {
					return errors.New("serverUri must be specified")
				}

				if (options.ServicePrincipal == "") != (options.CertificatePath == "") {
					return errors.New("servicePrincipal and certificatePath must be specified together")
				}

				if options.CertificatePath != "" && !filepath.IsAbs(options.CertificatePath) {
					// The certificate path is relative to the login options file.
					options.CertificatePath = filepath.Clean(filepath.Join(filepath.Dir(optionsFilePath), options.CertificatePath))
				}

				return controlplane.Login(options)
			case 1:
				if (options.ServicePrincipal == "") != (options.CertificatePath == "") {
					return errors.New("--service-principal and --cert must be specified together")
				}

				if options.ServicePrincipal != "" && options.UseDeviceCode {
					return errors.New("--use-device-code cannot be used with --service-principal")
				}

				options.ServerUri = args[0]
				return controlplane.Login(options)
			default:
				return errors.New("too many arguments")
			}
		},
	}

	loginCmd.AddCommand(newLoginStatusCommand())

	loginCmd.Flags().StringVarP(&optionsFilePath, "file", "f", "", "The path to a file containing login options")
	loginCmd.Flags().StringVarP(&options.ServicePrincipal, "service-principal", "s", "", "The service principal app ID or identifier URI")
	loginCmd.Flags().StringVarP(&options.CertificatePath, "cert", "c", "", "The path to the certificate in PEM format to use for service principal authentication")
	loginCmd.Flags().BoolVarP(&options.UseDeviceCode, "use-device-code", "d", false, "Whether to use the device code flow for user logins. Use this mode when the app can't launch a browser on your behalf.")

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
			context, err := controlplane.GetCliContext()
			if err == nil {
				err = context.Validate()
				if err == nil {
					fmt.Printf("You are logged into %s as %s\n", context.GetServerUri(), context.GetPrincipal())
					return nil
				}
			}

			return fmt.Errorf("you are not currently logged in to any Tyger server: %v", err)
		},
	}
}
