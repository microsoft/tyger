// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/http"
	"net/url"
	"os"
	"regexp"
	"strconv"
	"strings"

	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/yaml"
)

func NewCodespecCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "codespec",
		Aliases:               []string{"codespecs"},
		Short:                 "Manage codespecs",
		Long:                  `Manage codespecs`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	cmd.AddCommand(newCodespecCreateCommand())
	cmd.AddCommand(newCodespecShowCommand())
	cmd.AddCommand(codespecListCommand())

	return cmd
}

func newCodespecCreateCommand() *cobra.Command {
	type overcommittableResourceStrings struct {
		cpu    string
		memory string
	}
	var flags struct {
		specFile      string
		image         string
		kind          string
		inputBuffers  []string
		outputBuffers []string
		env           map[string]string
		command       bool
		identity      string
		requests      overcommittableResourceStrings
		limits        overcommittableResourceStrings
		gpu           string
		maxReplicas   string
		endpoints     map[string]int
	}

	var cmd = &cobra.Command{
		Use:                   `create NAME [--file YAML_SPEC] [--image IMAGE] [--kind job|worker] [--max-replicas REPLICAS] [[--input BUFFER_NAME] ...] [[--output BUFFER_NAME] ...] [[--env \"KEY=VALUE\"] ...] [--identity IDENTITY] [[ --endpoint SERVICE=PORT ]] [--gpu QUANTITY] [--cpu-request QUANTITY] [--memory-request QUANTITY] [--cpu-limit QUANTITY] [--memory-limit QUANTITY] [--command] -- [COMMAND] [args...]`,
		Short:                 "Create or update a codespec",
		Long:                  `Create or update a codespec. Outputs the version of the codespec that was created.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			newCodespec := model.Codespec{}

			if flags.specFile != "" {
				bytes, err := os.ReadFile(flags.specFile)
				if err != nil {
					return fmt.Errorf("failed to read file %s: %w", flags.specFile, err)
				}

				err = yaml.UnmarshalStrict(bytes, &newCodespec)
				if err != nil {
					return fmt.Errorf("failed to parse file %s: %w", flags.specFile, err)
				}

				if len(args) > 0 && cmd.ArgsLenAtDash() != 0 {
					newCodespec.Name = &args[0]
				} else if newCodespec.Name == nil || *newCodespec.Name == "" {
					return errors.New("a name for the codespec must be required")
				}
			} else {
				if len(args) == 0 || cmd.ArgsLenAtDash() == 0 {
					return errors.New("if -f|--file is not provided, a name for the codespec is required")
				}

				newCodespec.Name = &args[0]
			}

			if len(args) > 1 && cmd.ArgsLenAtDash() == -1 {
				return fmt.Errorf("unexpected positional args %v. Container arguments must be preceded by -- and placed at the end of the command-line", args[1:])
			}
			if len(args) > 1 && cmd.ArgsLenAtDash() > 1 {
				unexpectedArgs := args[1:cmd.ArgsLenAtDash()]
				return fmt.Errorf("unexpected positional args before container args: %v", unexpectedArgs)
			}

			var containerArgs []string
			if cmd.ArgsLenAtDash() > -1 {
				containerArgs = args[cmd.ArgsLenAtDash():]
			}

			var isValidName = regexp.MustCompile(`^[a-z0-9\-._]*$`).MatchString
			if newCodespec.Name == nil || !isValidName(*newCodespec.Name) {
				return errors.New("codespec names must contain only lower case letters (a-z), numbers (0-9), dashes (-), underscores (_), and dots (.)")
			}

			if hasFlagChanged(cmd, "image") {
				newCodespec.Image = flags.image
			}

			if hasFlagChanged(cmd, "input") {
				if newCodespec.Buffers == nil {
					newCodespec.Buffers = &model.BufferParameters{}
				}
				newCodespec.Buffers.Inputs = flags.inputBuffers
			}

			if hasFlagChanged(cmd, "output") {
				if newCodespec.Buffers == nil {
					newCodespec.Buffers = &model.BufferParameters{}
				}
				newCodespec.Buffers.Outputs = flags.outputBuffers
			}

			if hasFlagChanged(cmd, "env") {
				newCodespec.Env = flags.env
			}

			if hasFlagChanged(cmd, "identity") {
				newCodespec.Identity = flags.identity
			}

			if hasFlagChanged(cmd, "endpoint") {
				newCodespec.Endpoints = flags.endpoints
			}

			if flags.maxReplicas != "" {
				mr, err := strconv.Atoi(flags.maxReplicas)
				if err != nil {
					return fmt.Errorf("max-replicas must be an integer, got %s", flags.maxReplicas)
				}
				newCodespec.MaxReplicas = &mr
			}

			if newCodespec.Kind == "" {
				newCodespec.Kind = strings.ToLower(flags.kind)
			}

			switch newCodespec.Kind {
			case "job":
				if len(newCodespec.Endpoints) != 0 {
					return errors.New("job codespecs cannot have endpoints")
				}
				newCodespec.Endpoints = nil
			case "worker":
				if newCodespec.Buffers != nil && len(newCodespec.Buffers.Inputs)+len(newCodespec.Buffers.Outputs) != 0 {
					return errors.New("worker codespecs cannot have use buffers")
				}
				newCodespec.Buffers = nil
			default:
				return errors.New("codespec kind must be either 'job', worker' or empty (defaults to 'job')")
			}

			if cmd.ArgsLenAtDash() > -1 {
				if flags.command {
					newCodespec.Command = containerArgs
				} else {
					newCodespec.Args = containerArgs
				}
			}

			parseOvercommittableResources := func(resourceStrings overcommittableResourceStrings, resourceType string) (*model.OvercommittableResources, error) {
				resources := &model.OvercommittableResources{}
				cpuFlagName := fmt.Sprintf("cpu-%s", resourceType)
				if cmd.Flags().Lookup(cpuFlagName) == nil {
					panic(fmt.Sprintf("flag not found: %s", cpuFlagName))
				}
				if hasFlagChanged(cmd, cpuFlagName) {
					q, err := resource.ParseQuantity(resourceStrings.cpu)
					if err != nil {
						return nil, fmt.Errorf("cpu %s value is invalid: %v", resourceType, err)
					}
					resources.Cpu = &q
				}

				memoryFlagName := fmt.Sprintf("memory-%s", resourceType)
				if cmd.Flags().Lookup(memoryFlagName) == nil {
					panic(fmt.Sprintf("flag not found: %s", memoryFlagName))
				}

				if hasFlagChanged(cmd, memoryFlagName) {
					q, err := resource.ParseQuantity(resourceStrings.memory)
					if err != nil {
						return nil, fmt.Errorf("memory %s value is invalid: %v", resourceType, err)
					}
					resources.Memory = &q
				}

				return resources, nil
			}

			if res, err := parseOvercommittableResources(flags.requests, "request"); err != nil {
				return err
			} else {
				if res.Cpu != nil || res.Memory != nil {
					if newCodespec.Resources == nil {
						newCodespec.Resources = &model.CodespecResources{}
					}
					if newCodespec.Resources.Requests == nil {
						newCodespec.Resources.Requests = &model.OvercommittableResources{}
					}
					if res.Cpu != nil {
						newCodespec.Resources.Requests.Cpu = res.Cpu
					}
					if res.Memory != nil {
						newCodespec.Resources.Requests.Memory = res.Memory
					}
				}
			}

			if res, err := parseOvercommittableResources(flags.limits, "limit"); err != nil {
				return err
			} else {
				if res.Cpu != nil || res.Memory != nil {
					if newCodespec.Resources == nil {
						newCodespec.Resources = &model.CodespecResources{}
					}
					if newCodespec.Resources.Limits == nil {
						newCodespec.Resources.Limits = &model.OvercommittableResources{}
					}
					if res.Cpu != nil {
						newCodespec.Resources.Limits.Cpu = res.Cpu
					}
					if res.Memory != nil {
						newCodespec.Resources.Limits.Memory = res.Memory
					}
				}
			}

			if hasFlagChanged(cmd, "gpu") {
				q, err := resource.ParseQuantity(flags.gpu)
				if err != nil {
					return fmt.Errorf("gpu value is invalid: %v", err)
				}
				if newCodespec.Resources == nil {
					newCodespec.Resources = &model.CodespecResources{}
				}
				newCodespec.Resources.Gpu = &q
			}

			resp, err := controlplane.InvokeRequest(cmd.Context(), http.MethodPut, fmt.Sprintf("v1/codespecs/%s", *newCodespec.Name), nil, newCodespec, &newCodespec)
			if err != nil {
				return err
			}

			version, err := getCodespecVersionFromResponse(resp)
			if err != nil {
				return fmt.Errorf("unable to get codespec version: %v", err)
			}
			fmt.Println(version)

			return nil
		},
	}

	cmd.Flags().StringVar(&flags.image, "image", "", "The container image (required)")
	cmd.Flags().StringVarP(&flags.specFile, "file", "f", "", "A YAML file with the run specification. All other flags override the values in the file.")
	cmd.Flags().StringVarP(&flags.kind, "kind", "k", "job", "The codespec kind. Either 'job' (the default) or 'worker'.")
	cmd.Flags().StringVarP(&flags.maxReplicas, "max-replicas", "r", "", "The maximum number of replicas this codespec supports.")
	cmd.Flags().StringSliceVarP(&flags.inputBuffers, "input", "i", nil, "Input buffer parameter names")
	cmd.Flags().StringSliceVarP(&flags.outputBuffers, "output", "o", nil, "Output buffer parameter names")
	cmd.Flags().StringToStringVarP(&flags.env, "env", "e", nil, "Environment variables to set in the container in the form KEY=value")
	cmd.Flags().StringToIntVar(&flags.endpoints, "endpoint", nil, "TCP endpoints in the form NAME=PORT. Only valid for worker codespecs.")
	cmd.Flags().BoolVar(&flags.command, "command", false, "If true and extra arguments are present, use them as the 'command' field in the container, rather than the 'args' field which is the default.")
	cmd.Flags().StringVar(&flags.identity, "identity", "", "The workload identity to use for this codespec.")
	cmd.Flags().StringVar(&flags.requests.cpu, "cpu-request", "", "CPU cores requested")
	cmd.Flags().StringVar(&flags.requests.memory, "memory-request", "", "memory bytes requested")
	cmd.Flags().StringVar(&flags.limits.cpu, "cpu-limit", "", "CPU cores limit")
	cmd.Flags().StringVar(&flags.limits.memory, "memory-limit", "", "memory bytes limit")
	cmd.Flags().StringVar(&flags.gpu, "gpu", "", "GPUs needed")

	return cmd
}

func newCodespecShowCommand() *cobra.Command {
	var flags struct {
		version int
	}

	var cmd = &cobra.Command{
		Use:                   "show NAME [--version VERSION]",
		Aliases:               []string{"get"},
		Short:                 "Show the details of a codespec",
		Long:                  `Show the details of a codespec.`,
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
			_, err := controlplane.InvokeRequest(cmd.Context(), http.MethodGet, relativeUri, nil, nil, &codespec)
			if err != nil {
				return err
			}

			formattedRun, err := json.MarshalIndent(codespec, "  ", "  ")
			if err != nil {
				return err
			}

			fmt.Println(string(formattedRun))
			return nil
		},
	}

	cmd.Flags().IntVar(&flags.version, "version", -1, "the version of the codespec to get")

	return cmd
}

func getCodespecVersionFromResponse(resp *http.Response) (int, error) {
	location := resp.Header.Get("Location")
	return strconv.Atoi(location[strings.LastIndex(location, "/")+1:])
}

func codespecListCommand() *cobra.Command {
	var flags struct {
		limit  int
		prefix string
	}

	cmd := &cobra.Command{
		Use:                   "list [--prefix STRING] [--limit COUNT]",
		Short:                 "List codespecs",
		Long:                  `List codespecs. Latest version of codespecs are sorted alphabetically.`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, args []string) error {
			queryOptions := url.Values{}
			if flags.limit > 0 {
				queryOptions.Add("limit", strconv.Itoa(flags.limit))
			} else {
				flags.limit = math.MaxInt
			}
			if flags.prefix != "" {
				queryOptions.Add("prefix", flags.prefix)
			}

			return controlplane.InvokePageRequests[model.Codespec](cmd.Context(), "v1/codespecs", queryOptions, flags.limit, !cmd.Flags().Lookup("limit").Changed)
		},
	}

	cmd.Flags().StringVarP(&flags.prefix, "prefix", "p", "", "Show only codespecs that start with this prefix")
	cmd.Flags().IntVarP(&flags.limit, "limit", "l", 1000, "The maximum number of codespecs to list. Default 1000")

	return cmd
}
