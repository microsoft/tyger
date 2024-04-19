// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"
	"errors"
	"fmt"
	"html/template"
	"os"
	"os/exec"
	"path"
	"regexp"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/eiannone/keyboard"
	"github.com/golang-jwt/jwt/v5"
	"github.com/ipinfo/go/v2/ipinfo"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
	"github.com/microsoft/tyger/cli/internal/install/dockerinstall"
	"github.com/thediveo/enumflag"

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

	return installCmd
}

func newConfigCreateCommand() *cobra.Command {
	configPath := ""
	type kindEnum int
	const (
		azureCloudKind kindEnum = iota
		dockerKind
	)

	var kinds = map[kindEnum][]string{
		azureCloudKind: {cloudinstall.EnvironmentKindCloud},
		dockerKind:     {dockerinstall.EnvironmentKindDocker},
	}

	var kind kindEnum

	cmd := &cobra.Command{
		Use:                   "create -f FILE.yml",
		Short:                 "Create a new config file",
		Long:                  "Create a new config file",
		DisableFlagsInUseLine: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			return generateCloudConfig(cmd.Context(), configPath)
		},
	}

	cmd.Flags().VarP(enumflag.New(&kind, "kind", kinds, enumflag.EnumCaseInsensitive), "kind", "k", "The kind of environment to create a config for. Can be 'azureCloud' or 'docker'.")
	cmd.Flags().StringVarP(&configPath, "file", "f", "config.yml", "The path to the config file to create")
	cmd.MarkFlagRequired("file")

	return cmd
}

func generateCloudConfig(ctx context.Context, configPath string) error {
	if _, err := exec.LookPath("az"); err != nil {
		return errors.New("please install the Azure CLI (az) first")
	}

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
	}

	ipInfoClient := ipinfo.NewClient(nil, nil, "")
	ip, err := ipInfoClient.GetIPInfo(nil)
	if err != nil {
		return fmt.Errorf("failed to get current external IP address: %w", err)
	}

	templateValues := cloudinstall.ConfigTemplateValues{
		KubernetesVersion:    cloudinstall.DefaultKubernetesVersion,
		PostgresMajorVersion: cloudinstall.DefaultPostgresMajorVersion,
		CurrentIpAddress:     ip.IP.String(),
	}

	fmt.Printf("\nFirst, let's collect settings for the Azure subscription to use. This is where cloud resources will be deployed.\n\n")

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

	switch principal.Kind {
	case cloudinstall.PrincipalKindUser:
		templateValues.PrincipalKind = principal.Kind
		templateValues.PrincipalDisplay = principal.Upn

		if principal.IsFromCurrentTenant {
			templateValues.PrincipalId = principal.Upn
		} else {
			templateValues.PrincipalId = principal.ObjectId
		}
	case cloudinstall.PrincipalKindServicePrincipal:
		templateValues.PrincipalKind = principal.Kind
		templateValues.PrincipalId = principal.ObjectId
		templateValues.PrincipalDisplay = principal.Display
	default:
		panic(fmt.Sprintf("unexpected principal kind: %s", principal.Kind))
	}

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
		templateValues.CpuNodePoolMinCount, _ = strconv.Atoi(numString)
	}

	if numString, err := prompt("Enter the minimum node count for the GPU node pool:", "0", "", positiveIntegerRegex); err != nil {
		return err
	} else {
		templateValues.GpuNodePoolMinCount, _ = strconv.Atoi(numString)
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
				fmt.Printf("Run 'az login' in another terminal window.\nPress any key when ready...\n\n")
				getSingleKey()
				continue
			} else {
				templateValues.ApiTenantId = res
				break
			}
		}
	}

	if err := os.MkdirAll(path.Dir(configPath), 0775); err != nil {
		return fmt.Errorf("failed to create config directory: %w", err)
	}

	f, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, 0644)
	if err != nil {
		return fmt.Errorf("failed to open config file for writing: %w", err)
	}
	defer f.Close()
	err = cloudinstall.RenderConfig(templateValues, f)
	if err != nil {
		return fmt.Errorf("failed to write config file: %w", err)
	}

	fmt.Println("Config file written to", configPath)
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
		principal.Upn, err = cloudinstall.GetUserPrincipalName(ctx, cred, principals[0].ObjectId)
		if err != nil {
			return principal, err
		}

		if idpClaim, hasIdpClaim := claims["idp"]; hasIdpClaim && idpClaim.(string) != claims["iss"].(string) {
			principal.IsFromCurrentTenant = false
		} else {
			principal.IsFromCurrentTenant = true
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
	input.Validate = func(s string) error {
		if validationRegex.MatchString(s) {
			return nil
		}

		return fmt.Errorf("must match the regex %s", validationRegex.String())
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
	Upn                 string
	Display             string
	IsFromCurrentTenant bool
}

func (p ExtendedPrincipal) String() string {
	if p.Upn != "" {
		return p.Upn
	}

	if p.Display != "" {
		return p.Display
	}

	return p.ObjectId
}
