package cmd

import (
	"encoding/json"
	"fmt"
	"net/http"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/spf13/cobra"
)

func newGetCodespecCommand(rootFlags *rootPersistentFlags) *cobra.Command {
	var flags struct {
		version int
	}

	var cmd = &cobra.Command{
		Use:                   "codespec NAME [--version VERSION]",
		Short:                 "Get a codespec",
		Long:                  `Get a codespec.`,
		DisableFlagsInUseLine: true,
		Args:                  exactlyOneArg("codespec name"),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := args[0]

			relativeUri := fmt.Sprintf("v1/codespecs/%s", name)
			var version *int
			if cmd.Flag("version").Changed {
				version = &flags.version
				relativeUri = fmt.Sprintf("%s/versions/%d", relativeUri, *version)
			}

			codespec := model.Codespec{}
			resp, err := InvokeRequest(http.MethodGet, relativeUri, nil, &codespec, rootFlags.verbose)
			if err != nil {
				return err
			}

			if version == nil {
				latestVersion, err := getCodespecVersionFromResponse(resp)
				if err != nil {
					return fmt.Errorf("unable to get codespec version: %v", err)
				}
				version = &latestVersion
			}

			type namedCodespec struct {
				Name     string         `json:"name"`
				Version  int            `json:"version"`
				Codespec model.Codespec `json:"codespec"`
			}

			nc := namedCodespec{Name: name, Version: *version, Codespec: codespec}
			formatted, err := json.MarshalIndent(nc, "", "  ")
			if err != nil {
				return err
			}

			fmt.Println(string(formatted))
			return nil
		},
	}

	cmd.Flags().IntVar(&flags.version, "version", -1, "the version of the codespec to get")

	return cmd
}
