package cmd

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/controlplane"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/controlplane/model"
	"github.com/spf13/cobra"
	"k8s.io/apimachinery/pkg/api/resource"
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
		image         string
		kind          string
		inputBuffers  []string
		outputBuffers []string
		env           map[string]string
		command       bool
		requests      overcommittableResourceStrings
		limits        overcommittableResourceStrings
		gpu           string
		maxReplicas   string
		endpoints     map[string]int
	}

	var cmd = &cobra.Command{
		Use:                   "create NAME --image IMAGE [--kind job|worker] [--max-replicas REPLICAS] [[--input BUFFER_NAME] ...] [[--output BUFFER_NAME] ...] [[--env \"KEY=VALUE\"] ...] [[ --endpoint SERVICE=PORT ]] [resources] [--command] -- [COMMAND] [args...]",
		Short:                 "Create or update a codespec",
		Long:                  `Create or update a codespec. Outputs the version of the codespec that was created.`,
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if len(args) == 0 || cmd.ArgsLenAtDash() == 0 {
				return errors.New("a name for the codespec is required")
			}
			if len(args) > 1 && cmd.ArgsLenAtDash() == -1 {
				return fmt.Errorf("unexpected positional args %v. Container arguments must be preceded by -- and placed at the end of the command-line", args[1:])
			}
			if len(args) > 1 && cmd.ArgsLenAtDash() > 1 {
				unexpectedArgs := args[1:cmd.ArgsLenAtDash()]
				return fmt.Errorf("unexpected positional args before container args: %v", unexpectedArgs)
			}

			codespecName := args[0]
			containerArgs := args[1:]

			var isValidName = regexp.MustCompile(`^[a-z0-9\-._]*$`).MatchString
			if !isValidName(codespecName) {
				return errors.New("codespec names must contain only lower case letters (a-z), numbers (0-9), dashes (-), underscores (_), and dots (.)")
			}

			newCodespec := model.Codespec{
				Kind:  flags.kind,
				Image: flags.image,
				Buffers: &model.BufferParameters{
					Inputs:  flags.inputBuffers,
					Outputs: flags.outputBuffers,
				},
				Env: flags.env,
				Resources: &model.CodespecResources{
					Requests: &model.OvercommittableResources{},
					Limits:   &model.OvercommittableResources{},
				},
				Endpoints: flags.endpoints,
			}

			if flags.maxReplicas != "" {
				mr, err := strconv.Atoi(flags.maxReplicas)
				if err != nil {
					return fmt.Errorf("max-replicas must be an integer, got %s", flags.maxReplicas)
				}
				newCodespec.MaxReplicas = &mr
			}

			flags.kind = strings.ToLower(flags.kind)

			switch flags.kind {
			case "job":
				if len(newCodespec.Endpoints) != 0 {
					return errors.New("job codespecs cannot have endpoints")
				}
				newCodespec.Endpoints = nil
			case "worker":
				if len(newCodespec.Buffers.Inputs)+len(newCodespec.Buffers.Outputs) != 0 {
					return errors.New("worker codespecs cannot have use buffers")
				}
				newCodespec.Buffers = nil
			default:
				return errors.New("--kind must be either 'job' or worker'")
			}

			if flags.command {
				newCodespec.Command = containerArgs
			} else {
				newCodespec.Args = containerArgs
			}

			parseOvercommittableResources := func(resourceStrings overcommittableResourceStrings, resourceType string) (*model.OvercommittableResources, error) {
				resources := &model.OvercommittableResources{}
				if (resourceStrings.cpu) != "" {
					q, err := resource.ParseQuantity(resourceStrings.cpu)
					if err != nil {
						return nil, fmt.Errorf("cpu %s value is invalid: %v", resourceType, err)
					}
					resources.Cpu = &q
				}

				if (resourceStrings.memory) != "" {
					q, err := resource.ParseQuantity(resourceStrings.memory)
					if err != nil {
						return nil, fmt.Errorf("memory %s value is invalid: %v", resourceType, err)
					}
					resources.Memory = &q
				}

				return resources, nil
			}

			var err error
			newCodespec.Resources.Requests, err = parseOvercommittableResources(flags.requests, "request")
			if err != nil {
				return err
			}

			newCodespec.Resources.Limits, err = parseOvercommittableResources(flags.limits, "limit")
			if err != nil {
				return err
			}

			if (flags.gpu) != "" {
				q, err := resource.ParseQuantity(flags.gpu)
				if err != nil {
					return fmt.Errorf("gpu value is invalid: %v", err)
				}
				newCodespec.Resources.Gpu = &q
			}

			resp, err := controlplane.InvokeRequest(http.MethodPut, fmt.Sprintf("v1/codespecs/%s", codespecName), newCodespec, &newCodespec)
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
	if err := cmd.MarkFlagRequired("image"); err != nil {
		log.Panicln(err)
	}
	cmd.Flags().StringVarP(&flags.kind, "kind", "k", "job", "The codespec kind. Either 'job' (the default) or 'worker'.")
	cmd.Flags().StringVarP(&flags.maxReplicas, "max-replicas", "r", "", "The maximum number of replicas this codespec supports.")
	cmd.Flags().StringSliceVarP(&flags.inputBuffers, "input", "i", nil, "Input buffer parameter names")
	cmd.Flags().StringSliceVarP(&flags.outputBuffers, "output", "o", nil, "Output buffer parameter names")
	cmd.Flags().StringToStringVarP(&flags.env, "env", "e", nil, "Environment variables to set in the container in the form KEY=value")
	cmd.Flags().StringToIntVar(&flags.endpoints, "endpoint", nil, "TCP endpoints in the form NAME=PORT. Only valid for worker codespecs.")
	cmd.Flags().BoolVar(&flags.command, "command", false, "If true and extra arguments are present, use them as the 'command' field in the container, rather than the 'args' field which is the default.")
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
			_, err := controlplane.InvokeRequest(http.MethodGet, relativeUri, nil, &codespec)
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

			var relativeUri string = fmt.Sprintf("v1/codespecs?%s", queryOptions.Encode())
			return controlplane.InvokePageRequests[model.Codespec](relativeUri, flags.limit, !cmd.Flags().Lookup("limit").Changed)
		},
	}

	cmd.Flags().StringVarP(&flags.prefix, "prefix", "p", "", "Show only codespecs that start with this prefix")
	cmd.Flags().IntVarP(&flags.limit, "limit", "l", 1000, "The maximum number of codespecs to list. Default 1000")

	return cmd
}
