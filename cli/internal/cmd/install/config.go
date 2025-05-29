// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"html/template"
	"io"
	"math"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/a8m/envsubst"
	"github.com/eiannone/keyboard"
	"github.com/golang-jwt/jwt/v5"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
	"github.com/microsoft/tyger/cli/internal/install/dockerinstall"
	"github.com/rs/zerolog/log"
	yaml "sigs.k8s.io/yaml/goyaml.v3"

	"github.com/spf13/cobra"

	"github.com/erikgeiser/promptkit"
	"github.com/erikgeiser/promptkit/confirmation"
	"github.com/erikgeiser/promptkit/selection"
	"github.com/erikgeiser/promptkit/textinput"
)

func NewConfigCommand(parentCommand *cobra.Command) *cobra.Command {
	installCmd := &cobra.Command{
		Use:                   "config",
		Short:                 "Manage the Tyger configuration file",
		Long:                  "Manage the Tyger configuration file",
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			return errors.New("a command is required")
		},
	}

	installCmd.AddCommand(newConfigCreateCommand())
	installCmd.AddCommand(newConfigValidateCommand())
	installCmd.AddCommand(newConfigPrettyPrintCommand())
	installCmd.AddCommand(newConfigConvertCommand())

	return installCmd
}

func newConfigValidateCommand() *cobra.Command {
	inputPath := ""
	cmd := &cobra.Command{
		Use:                   "validate -f FILE.yml",
		Short:                 "Validate a config file",
		Long:                  "Validate a config file",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			yamlBytes, err := os.ReadFile(inputPath)
			if err != nil {
				log.Fatal().AnErr("error", err).Msg("Failed to read config file")
			}

			c, err := ParseConfig(yamlBytes)
			if err == nil {
				err = c.QuickValidateConfig(cmd.Context())
			}

			if err != nil {
				if !errors.Is(err, install.ErrAlreadyLoggedError) {
					log.Fatal().Err(err).Send()
				}
				os.Exit(1)
			}
		},
	}

	cmd.Flags().StringVarP(&inputPath, "file", "f", "", "The path to the config file to validate")
	cmd.MarkFlagRequired("file")

	return cmd
}

func newConfigPrettyPrintCommand() *cobra.Command {
	inputPath := ""
	outputPath := ""
	singleFilePath := ""
	templatePath := ""

	cmd := &cobra.Command{
		Use:                   "pretty-print -f FILE.yml | { -i INPUT.yml [-o OUTPUT.yml] }",
		Short:                 "Add documentation to a config file.",
		Long:                  "Add documentation to a config file.",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			if singleFilePath != "" {
				if inputPath != "" {
					log.Fatal().Msg("Cannot specify both --input and --file flags")
				}
				if outputPath != "" {
					log.Fatal().Msg("Cannot specify both --output and --file flags")
				}
				outputPath = singleFilePath
				inputPath = singleFilePath
			} else if inputPath == "" {
				log.Fatal().Msg("Either --file or --input must be specified")
			}

			inputFileBytes, err := os.ReadFile(inputPath)
			if err != nil {
				log.Fatal().AnErr("error", err).Msg("Failed to read config file")
			}

			outputBuffer := bytes.Buffer{}
			if err := prettyPrintConfig(cmd.Context(), bytes.NewBuffer(inputFileBytes), &outputBuffer, true); err != nil {
				if !errors.Is(err, install.ErrAlreadyLoggedError) {
					log.Fatal().AnErr("error", err).Msg("Failed to pretty print config file")
				}
				os.Exit(1)
			}

			if templatePath != "" {
				// Restore with unexpanded environment variables from the template file.
				templateBytes, err := os.ReadFile(templatePath)
				if err != nil {
					log.Fatal().AnErr("error", err).Msg("Failed to read template file")
				}

				templateNode, err := ParseConfigToAst(templateBytes)
				if err != nil {
					log.Fatal().AnErr("error", err).Msg("Failed to parse template file")
				}

				prettyPrintedAst, err := ParseConfigToAst(outputBuffer.Bytes())
				if err != nil {
					log.Fatal().AnErr("error", err).Msg("Failed to parse pretty printed config file")
				}

				mergeAst(prettyPrintedAst, templateNode)

				outputBuffer.Reset()
				outputBuffer.WriteString(prettyPrintedAst.String() + "\n")
			}

			writeToOutputPathOrStdoutFatalOnError(outputPath, outputBuffer.Bytes())
		},
	}

	cmd.Flags().StringVarP(&inputPath, "input", "i", "", "The path to the config file to read")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "The path to the config file to create")
	cmd.Flags().StringVarP(&singleFilePath, "file", "f", "", "The path to the configuration file to update in-place. Equivalent to --input and --output with the same value.")

	cmd.Flags().StringVar(&templatePath, "template", "", "Internal. The original template with unexpanded environment variables")
	cmd.Flags().MarkHidden("template")

	return cmd
}

func prettyPrintConfig(ctx context.Context, input io.Reader, output io.Writer, validate bool) error {
	inputBytes, err := io.ReadAll(input)
	if err != nil {
		return err
	}

	c, err := ParseConfig(inputBytes)
	if err != nil {
		return err
	}

	if validate {
		if err := c.QuickValidateConfig(ctx); err != nil {
			return err
		}
	}

	// parse the config again to clear the fields that are set during validation
	c, err = ParseConfig(inputBytes)
	if err != nil {
		return err
	}

	var outputContents string

	switch config := c.(type) {
	case *dockerinstall.DockerEnvironmentConfig:
		buf := &strings.Builder{}
		if err := dockerinstall.PrettyPrintConfig(config, buf); err != nil {
			return fmt.Errorf("failed to pretty print config: %w", err)
		}
		outputContents = buf.String()
	case *cloudinstall.CloudEnvironmentConfig:
		config = c.(*cloudinstall.CloudEnvironmentConfig)

		buf := &strings.Builder{}
		if err := cloudinstall.PrettyPrintConfig(config, buf); err != nil {
			return fmt.Errorf("failed to pretty print config: %w", err)
		}
		outputContents = buf.String()
	default:
		panic(fmt.Sprintf("unexpected config type: %T", config))
	}

	if _, err := output.Write([]byte(outputContents)); err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	parsedPrettyConfig, err := ParseConfig([]byte(outputContents))
	if err != nil {
		return fmt.Errorf("failed to parse config: %w", err)
	}

	if validate {
		if err := parsedPrettyConfig.QuickValidateConfig(ctx); err != nil {
			return fmt.Errorf("validation failed on pretty printed config: %w", err)
		}
	}

	return nil
}

func newConfigConvertCommand() *cobra.Command {
	inputPath := ""
	outputPath := ""

	cmd := &cobra.Command{
		Use:                   "convert -i FILE.yml -o FILE.yml",
		Long:                  "Convert a config file in a older format.",
		Short:                 "Convert a config file in a older format.",
		DisableFlagsInUseLine: true,
		Run: func(cmd *cobra.Command, args []string) {
			err := convert(cmd.Context(), inputPath, outputPath)
			if err != nil {
				if !errors.Is(err, install.ErrAlreadyLoggedError) {
					log.Fatal().AnErr("error", err).Send()
				}

				os.Exit(1)
			}
		},
	}

	cmd.Flags().StringVarP(&inputPath, "input", "i", "", "The path to the config file to read")
	cmd.MarkFlagRequired("input")
	cmd.Flags().StringVarP(&outputPath, "output", "o", "", "The path to the config file to create")
	cmd.MarkFlagRequired("output")

	return cmd
}

func convert(ctx context.Context, inputPath string, outputPath string) error {
	yamlBytes, err := os.ReadFile(inputPath)
	if err != nil {
		return fmt.Errorf("failed to read config file: %w", err)
	}

	document, err := ParseConfigToMap(yamlBytes)
	if err != nil {
		return err
	}

	if kind, _ := document["kind"].(string); kind != "docker" {
		if document["organizations"] == nil {
			identities := safeGetAndRemove(document, "cloud.compute.identities")
			storage := safeGetAndRemove(document, "cloud.storage")
			api := safeGetAndRemove(document, "api")

			if storage == nil || api == nil {
				return fmt.Errorf("the given config file appears not to be have been valid")
			}

			if apiMap, ok := api.(map[string]any); ok {
				apiMap["tlsCertificateProvider"] = string(cloudinstall.TlsCertificateProviderLetsEncrypt)
				apiMap["accessControl"] = safeGetAndRemove(apiMap, "auth")
			}

			org := map[string]any{
				"name":                                "default",
				"singleOrganizationCompatibilityMode": true,
				"cloud": map[string]any{
					"storage":    storage,
					"identities": identities,
				},
				"api": api,
			}

			document["organizations"] = []any{org}
		}
	}

	if orgs, ok := document["organizations"].([]any); ok {
		for _, org := range orgs {
			if orgMap, ok := org.(map[string]any); ok {
				if apiMap, ok := orgMap["api"].(map[string]any); ok {
					auth := safeGetAndRemove(apiMap, "auth")
					if auth != nil {
						apiMap["accessControl"] = auth
					}
				}
			}
		}
	}

	buffer := bytes.Buffer{}
	if err := yaml.NewEncoder(&buffer).Encode(document); err != nil {
		return fmt.Errorf("failed to serialize config file: %w", err)
	}

	if err := os.MkdirAll(path.Dir(outputPath), 0775); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	outputFile, err := os.OpenFile(outputPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open config file for writing: %w", err)
	}

	defer outputFile.Close()

	if err := prettyPrintConfig(ctx, &buffer, outputFile, false); err != nil {
		return fmt.Errorf("failed to pretty print config file: %w", err)
	}

	return nil
}

func safeGetAndRemove(m map[string]any, path string) any {
	segments := strings.Split(path, ".")
	for i, segment := range segments {
		if i == len(segments)-1 {
			if val, ok := m[segment]; ok {
				delete(m, segment)
				return val
			}
			return nil
		}
		if val, ok := m[segment]; ok {
			if subMap, ok := val.(map[string]any); ok {
				m = subMap
			} else {
				return nil
			}
		} else {
			return nil
		}
	}

	return nil
}

func newConfigCreateCommand() *cobra.Command {
	configPath := ""
	cmd := &cobra.Command{
		Use:                   "create -f FILE.yml",
		Short:                 "Create a new config file",
		Long:                  "Create a new config file",
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if _, err := os.Stat(configPath); err == nil {
				input := confirmation.New(fmt.Sprintf("A config file already exists at %s and will be overwritten. Continue?", configPath), confirmation.Yes)
				input.WrapMode = promptkit.WordWrap
				ready, err := input.RunPrompt()
				if err != nil {
					return err
				}
				if !ready {
					os.Exit(1)
				}

				fmt.Println()
			}

			if err := os.MkdirAll(path.Dir(configPath), 0775); err != nil {
				return fmt.Errorf("failed to create config directory: %w", err)
			}

			f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
			if err != nil {
				return fmt.Errorf("failed to open config file for writing: %w", err)
			}
			defer f.Close()

			options := []IdAndName{
				{cloudinstall.ConfigKindCloud, "Azure cloud"},
				{dockerinstall.ConfigKindDocker, "Docker"},
			}

			s := selection.New(
				"Do you want to create a Tyger environment in the Azure cloud or on your local system using Docker?",
				options)
			s.Filter = nil
			s.WrapMode = promptkit.WordWrap

			res, err := s.RunPrompt()
			if err != nil {
				return err
			}

			fmt.Println()

			switch res.id {
			case cloudinstall.ConfigKindCloud:
				if err := generateCloudConfig(cmd.Context(), f); err != nil {
					return err
				}
			case dockerinstall.ConfigKindDocker:
				if err := generateDockerConfig(f); err != nil {
					return err
				}
			default:
				panic(fmt.Sprintf("unexpected environment kind: %s", res.id))
			}

			fmt.Println("Config file written to", configPath)
			return nil
		},
	}

	cmd.Flags().StringVarP(&configPath, "file", "f", "", "The path to the config file to create")
	cmd.MarkFlagRequired("file")

	return cmd
}

func generateDockerConfig(configFile *os.File) error {
	templateValues := dockerinstall.ConfigTemplateValues{}

	c := confirmation.New("Tyger requires a cryptographic key pair to secure the data plane API. Do you want to generate a new key pair?", confirmation.Yes)
	c.WrapMode = promptkit.WordWrap
	shouldGenerateKey, err := c.RunPrompt()
	if err != nil {
		return err
	}

	fmt.Println()

	privateKeyPath := "${HOME}/tyger-signing.pem"
	var expandedPrivateKeyPath string
PromptPrivateKey:
	privateKeyPath, err = prompt("Enter the path of the private key file. This must not be in a source code repository. Environment variables (${VAR}) will be expanded when reading the file.", privateKeyPath, "", nil)
	if err != nil {
		return err
	}

	expandedPrivateKeyPath, err = envsubst.String(privateKeyPath)
	if err != nil {
		return fmt.Errorf("failed to expand environment variables: %w", err)
	}

	if _, err := os.Stat(expandedPrivateKeyPath); err != nil {
		if !shouldGenerateKey {
			fmt.Printf("Failed to stat file at '%s'. Please enter a valid path.\n\n", expandedPrivateKeyPath)
			goto PromptPrivateKey
		}
	} else {
		if shouldGenerateKey {
			c := confirmation.New("Do you want to overwrite the existing file?", confirmation.Yes)
			c.WrapMode = promptkit.WordWrap
			overwrite, err := c.RunPrompt()
			if err != nil {
				return err
			}

			fmt.Println()

			if !overwrite {
				goto PromptPrivateKey
			}
		}
	}

	baseName := strings.TrimSuffix(privateKeyPath, filepath.Ext(privateKeyPath))
	publicKeyPath := fmt.Sprintf("%s-public%s", baseName, filepath.Ext(privateKeyPath))
	var expandedPublicKeyPath string
PromptPublicKey:

	publicKeyPath, err = prompt("Enter the path of the public key file. This must not be in a source code repository. Environment variables (${VAR}) will be expanded when reading the file.", publicKeyPath, "", nil)
	if err != nil {
		return err
	}

	expandedPublicKeyPath, err = envsubst.StringRestricted(publicKeyPath, true, false)
	if err != nil {
		return fmt.Errorf("failed to expand environment variables: %w", err)
	}

	if _, err := os.Stat(expandedPublicKeyPath); err != nil {
		if !shouldGenerateKey {
			fmt.Printf("Failed to stat file at '%s'. Please enter a valid path.\n\n", expandedPublicKeyPath)
			goto PromptPublicKey
		}
	} else {
		if shouldGenerateKey {
			c := confirmation.New("Do you want to overwrite the existing file?", confirmation.Yes)
			c.WrapMode = promptkit.WordWrap
			overwrite, err := c.RunPrompt()
			if err != nil {
				return err
			}

			fmt.Println()

			if !overwrite {
				goto PromptPublicKey
			}
		}
	}

	templateValues.PrivateSigningKeyPath = privateKeyPath
	templateValues.PublicSigningKeyPath = publicKeyPath

	if shouldGenerateKey {
		if err := dockerinstall.GenerateSigningKeyPair(expandedPublicKeyPath, expandedPrivateKeyPath); err != nil {
			return fmt.Errorf("failed to generate key pair: %w", err)
		}
	}

	portString := ""
	port, err := dataplane.GetFreePort()
	if err == nil {
		portString = strconv.Itoa(port)
	}

	portString, err = prompt("Enter the port on which the data plane API will listen:", portString, "", regexp.MustCompile(`^\d+$`))
	if err != nil {
		return err
	}

	templateValues.DataPlanePort, err = strconv.Atoi(portString)
	if err != nil {
		return err
	}

	return dockerinstall.RenderConfig(templateValues, configFile)
}

func generateCloudConfig(ctx context.Context, configFile *os.File) error {
	if _, err := exec.LookPath("az"); err != nil {
		return errors.New("please install the Azure CLI (az) first")
	}

	templateValues := cloudinstall.ConfigTemplateValues{
		KubernetesVersion:    cloudinstall.DefaultKubernetesVersion,
		PostgresMajorVersion: cloudinstall.DefaultPostgresMajorVersion,
	}

	fmt.Printf("\nLet's collect settings for the Azure subscription to use. This is where cloud resources will be deployed.\n\n")

	var err error
	var cred azcore.TokenCredential

	for {
		var principal ExtendedPrincipal
		for {

			cred, err = cloudinstall.NewMiAwareAzureCLICredential(nil)
			if err == nil {
				principal, err = getCurrentPrincipal(ctx, cred)
			}
			if err != nil {
				if err == cloudinstall.ErrNotLoggedIn {
					fmt.Printf("You are not logged in to Azure. Please run `az login` in another terminal window.\nPress any key to continue when ready...\n\n")
					getSingleKey()
					continue
				}
				return err
			}
			break
		}

		input := confirmation.New(fmt.Sprintf("You are logged in as %s. Is that the right account?", principal.String()), confirmation.Yes)
		input.WrapMode = promptkit.WordWrap
		ready, err := input.RunPrompt()
		if err != nil {
			return err
		}
		fmt.Println()
		if ready {
			break
		}

		fmt.Printf("Please run `az login` in another terminal window.\nPress any key to continue when ready...\n\n")
		getSingleKey()
	}

	templateValues.TenantId, err = chooseTenant(cred, "Select the tenant associated with the subscription:", false)
	if err != nil {
		return err
	}

	tenantCred, err := cloudinstall.NewMiAwareAzureCLICredential(
		&azidentity.AzureCLICredentialOptions{
			TenantID: templateValues.TenantId,
		})
	if err != nil {
		return err
	}

	principal, err := getCurrentPrincipal(ctx, tenantCred)
	if err != nil {
		return err
	}

	templateValues.ManagementPrincipal = principal.Principal

	for {
		templateValues.SubscriptionId, err = chooseSubscription(tenantCred)
		if err != nil {
			if strings.Contains(err.Error(), "AADSTS50076") {

				fmt.Printf("Run 'az login --tenant %s' in another terminal window to explicitly login to this tenant.\nPress any key when ready...\n\n", templateValues.TenantId)
				getSingleKey()
				continue
			}
			return err
		}
		break
	}

	templateValues.EnvironmentName, err = prompt("Give this environment a name:", "", "", cloudinstall.ResourceNameRegex)
	if err != nil {
		return err
	}

	templateValues.ResourceGroup, err = prompt("Enter a name for a resource group:", templateValues.EnvironmentName, "", cloudinstall.ResourceNameRegex)
	if err != nil {
		return err
	}

	templateValues.DefaultLocation, err = chooseLocation(tenantCred, templateValues.SubscriptionId)
	if err != nil {
		return err
	}

	templateValues.DatabaseServerName, err = prompt("Give the database server a name:", fmt.Sprintf("%s-tyger", templateValues.EnvironmentName), "", cloudinstall.DatabaseServerNameRegex)
	if err != nil {
		return err
	}

	templateValues.BufferStorageAccountName, err = prompt("Give the buffer storage account a name:", fmt.Sprintf("%s%sbuf", templateValues.EnvironmentName, templateValues.DefaultLocation), "", cloudinstall.StorageAccountNameRegex)
	if err != nil {
		return err
	}

	templateValues.LogsStorageAccountName, err = prompt("Give the logs storage account a name:", fmt.Sprintf("%stygerlogs", templateValues.EnvironmentName), "", cloudinstall.StorageAccountNameRegex)
	if err != nil {
		return err
	}

	positiveIntegerRegex := regexp.MustCompile(`^\d+$`)
	if numString, err := prompt("Enter the minimum node count for the CPU node pool:", "1", "", positiveIntegerRegex); err != nil {
		return err
	} else {
		i, err := strconv.Atoi(numString)
		if err == nil && i >= 0 && i <= math.MaxInt32 {
			templateValues.CpuNodePoolMinCount = int32(i)
		} else {
			return fmt.Errorf("invalid value for CPU node pool min count: %s", numString)
		}
	}

	if numString, err := prompt("Enter the minimum node count for the GPU node pool:", "0", "", positiveIntegerRegex); err != nil {
		return err
	} else {
		i, err := strconv.Atoi(numString)
		if err == nil && i >= 0 && i <= math.MaxInt32 {
			templateValues.GpuNodePoolMinCount = int32(i)
		} else {
			return fmt.Errorf("invalid value for GPU node pool min count: %s", numString)
		}
	}

	suggestedDomainName := fmt.Sprintf("%s-tyger", templateValues.EnvironmentName)
	domainSuffix := cloudinstall.GetDomainNameSuffix(templateValues.DefaultLocation)
	domainLabel, err := prompt("Choose a domain name for the Tyger service:", suggestedDomainName, domainSuffix, cloudinstall.SubdomainRegex)
	if err != nil {
		return err
	}
	templateValues.DomainName = fmt.Sprintf("%s%s", domainLabel, domainSuffix)

	fmt.Printf("Now for the tenant associated with the Tyger service.\n\n")
	input := confirmation.New("Do you want to use the same tenant for the Tyger service?", confirmation.Yes)
	input.WrapMode = promptkit.WordWrap
	sameTenant, err := input.RunPrompt()
	if err != nil {
		return err
	}

	if sameTenant {
		templateValues.ApiTenantId = templateValues.TenantId
	} else {
		for {
			res, err := chooseTenant(cred, "Choose a tenant for the Tyger service:", true)
			if err != nil {
				return err
			}

			if res == "other" {
				fmt.Printf("Run 'az login --allow-no-subscriptions' in another terminal window.\nPress any key when ready...\n\n")
				getSingleKey()
				continue
			} else {
				templateValues.ApiTenantId = res
				break
			}
		}
	}

	principal, err = getCurrentPrincipal(ctx, cred)
	if err != nil {
		return err
	}

	templateValues.TygerPrincipal = principal.Principal

	err = cloudinstall.RenderConfig(templateValues, configFile)
	if err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	return nil
}

func chooseLocation(cred azcore.TokenCredential, subscriptionId string) (string, error) {
	c, err := armsubscriptions.NewClient(cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create subscriptions client: %w", err)
	}
	locations := make([]IdAndName, 0)
	pager := c.NewListLocationsPager(subscriptionId, nil)
	for pager.More() {
		page, err := pager.NextPage(context.Background())
		if err != nil {
			return "", fmt.Errorf("failed to enumerate locations: %w", err)
		}
		for _, l := range page.LocationListResult.Value {
			if *l.Metadata.RegionType == armsubscriptions.RegionTypePhysical {
				locations = append(locations, IdAndName{
					id:   *l.Name,
					name: *l.DisplayName,
				})
			}
		}
	}

	if len(locations) == 0 {
		return "", errors.New("no Azure locations are available for this subscription")
	}

	sp := selection.New("Select a default Azure location:", locations)
	sp.Filter = func(filterText string, choice *selection.Choice[IdAndName]) bool {
		return choice.Value.MatchesFilter(filterText)
	}
	res, err := sp.RunPrompt()
	if err != nil {
		return "", err
	}

	fmt.Println()
	return res.id, nil
}

func chooseSubscription(cred azcore.TokenCredential) (string, error) {
	subscriptionsClient, _ := armsubscriptions.NewClient(cred, nil)
	subscriptions := make([]IdAndName, 0)
	pager := subscriptionsClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(context.Background())
		if err != nil {
			return "", fmt.Errorf("failed to enumerate subscriptions: %w", err)
		}

		for _, sub := range page.SubscriptionListResult.Value {
			subscriptions = append(subscriptions, IdAndName{
				id:   *sub.SubscriptionID,
				name: *sub.DisplayName,
			})
		}
	}

	if len(subscriptions) == 0 {
		return "", errors.New("no subscriptions found")
	}

	sp := selection.New("Select the Azure subscription to use:", subscriptions)
	sp.Filter = func(filterText string, choice *selection.Choice[IdAndName]) bool {
		return choice.Value.MatchesFilter(filterText)
	}

	sub, err := sp.RunPrompt()
	if err != nil {
		return "", err
	}
	fmt.Println()
	return sub.id, nil
}

func getCurrentPrincipal(ctx context.Context, cred azcore.TokenCredential) (principal ExtendedPrincipal, err error) {
	tokenResponse, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{cloud.AzurePublic.Services[cloud.ResourceManager].Audience}})
	if err != nil {
		return principal, cloudinstall.ErrNotLoggedIn
	}

	claims := jwt.MapClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(tokenResponse.Token, claims)
	if err != nil {
		return principal, fmt.Errorf("failed to parse token: %w", err)
	}
	principal.ObjectId = claims["oid"].(string)

	principals, err := cloudinstall.ObjectsIdToPrincipals(ctx, cred, []string{principal.ObjectId})
	if err != nil {
		return principal, err
	}

	if len(principals) == 0 {
		return principal, errors.New("principal not found")
	}

	principal.Kind = principals[0].Kind

	switch principals[0].Kind {
	case cloudinstall.PrincipalKindUser:
		principal.UserPrincipalName, err = cloudinstall.GetUserPrincipalName(ctx, cred, principals[0].ObjectId)
		if err != nil {
			return principal, err
		}

	case cloudinstall.PrincipalKindServicePrincipal:
		principal.Display, err = cloudinstall.GetServicePrincipalDisplayName(ctx, cred, principals[0].ObjectId)
		if err != nil {
			return principal, err
		}
	default:
		panic(fmt.Sprintf("unexpected principal kind: %s", principals[0].Kind))
	}

	return principal, nil
}

func chooseTenant(cred azcore.TokenCredential, prompt string, presentOtherOption bool) (string, error) {
	tenantsClient, err := armsubscriptions.NewTenantsClient(cred, nil)
	if err != nil {
		return "", err
	}

	tenants := make([]IdAndName, 0)

	pager := tenantsClient.NewListPager(nil)
	for pager.More() {
		page, err := pager.NextPage(context.Background())
		if err != nil {
			return "", fmt.Errorf("failed to enumerate tenants: %w", err)
		}

		for _, ten := range page.TenantListResult.Value {
			tenant := IdAndName{
				id: *ten.TenantID,
			}
			if ten.DisplayName != nil {
				tenant.name = *ten.DisplayName
			} else {
				tenant.name = tenant.id
			}
			tenants = append(tenants, tenant)
		}
	}

	if len(tenants) == 0 {
		panic("no tenants found")
	}

	if presentOtherOption {
		tenants = append(tenants, IdAndName{
			id:   "other",
			name: "***OTHER***",
		})
	}

	if len(tenants) == 1 {
		return tenants[0].id, nil
	} else {
		sp := selection.New(prompt, tenants)
		sp.Filter = func(filterText string, choice *selection.Choice[IdAndName]) bool {
			return choice.Value.MatchesFilter(filterText)
		}

		sp.PageSize = 0

		t, err := sp.RunPrompt()
		if err != nil {
			return "", err
		}
		fmt.Println()
		return t.id, nil
	}
}

type IdAndName struct {
	id   string
	name string
}

func (t IdAndName) String() string {
	return t.name
}

func (t IdAndName) MatchesFilter(filterText string) bool {
	return strings.Contains(strings.ReplaceAll(strings.ToLower(t.String()), " ", ""), strings.ToLower(filterText))
}

func prompt(question, initialValue string, suffix string, validationRegex *regexp.Regexp) (string, error) {
	const tmpl = `
{{- Bold .Prompt }} {{ .Input -}} {{ Suffix }}
{{- if .ValidationError }} {{ Foreground "1" (Bold "✘") }}
{{ Faint (Bold .ValidationError.Error) }}
{{- else }} {{ Foreground "2" (Bold "✔") }}
{{- end -}}
	`

	const resultTmpl = `
	{{- print .Prompt " " (Foreground "32"  (Mask .FinalValue)) (Suffix) "\n" -}}
	`

	input := textinput.New(question)
	input.WrapMode = promptkit.WordWrap
	input.InitialValue = initialValue
	if validationRegex != nil {
		input.Validate = func(s string) error {
			if validationRegex.MatchString(s) {
				return nil
			}

			return fmt.Errorf("must match the regex %s", validationRegex.String())
		}
	}

	input.ExtendedTemplateFuncs = template.FuncMap{
		"Suffix": func() string {
			return suffix
		},
	}

	input.Template = tmpl
	input.ResultTemplate = resultTmpl

	defer fmt.Println()
	return input.RunPrompt()
}

func getSingleKey() {
	_, key, err := keyboard.GetSingleKey()
	if err != nil {
		panic(err)
	}
	if key == keyboard.KeyCtrlC {
		os.Exit(1)
	}
}

type ExtendedPrincipal struct {
	cloudinstall.Principal
	Display string
}

func (p ExtendedPrincipal) String() string {
	if p.UserPrincipalName != "" {
		return p.UserPrincipalName
	}

	if p.Display != "" {
		return p.Display
	}

	return p.ObjectId
}
