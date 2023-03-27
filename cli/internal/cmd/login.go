package cmd

import (
	"errors"
	"fmt"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/controlplane"
	"github.com/spf13/cobra"
)

func NewLoginCommand() *cobra.Command {
	flags := controlplane.LoginOptions{}

	loginCmd := &cobra.Command{
		Use:   "login SERVER_URL",
		Short: "Login to a server",
		Long: `Login to the Tyger server at the given URL.
Subsequent commands will be performed against this server.`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("server [--service pricipal APPID --certificate CERTPATH] [--use-device-code] url"),
		RunE: func(cmd *cobra.Command, args []string) error {

			if (flags.ServicePrincipal == "") != (flags.CertificatePath == "") {
				return errors.New("--service-principal and --cert must be specified together")
			}

			if flags.ServicePrincipal != "" && flags.UseDeviceCode {
				return errors.New("--use-device-code cannot be used with --service-principal")
			}

			flags.ServerUri = args[0]
			return controlplane.Login(flags)
		},
	}

	loginCmd.AddCommand(newLoginStatusCommand())

	loginCmd.Flags().StringVarP(&flags.ServicePrincipal, "service-principal", "s", "", "The service principal app ID or identifier URI")
	loginCmd.Flags().StringVarP(&flags.CertificatePath, "cert", "c", "", "The path to the certificate in PEM format to use for service principal authentication")
	loginCmd.Flags().BoolVarP(&flags.UseDeviceCode, "use-device-code", "d", false, "Whether to use the device code flow for user logins. Use this mode when the app can't launch a browser on your behalf.")

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
