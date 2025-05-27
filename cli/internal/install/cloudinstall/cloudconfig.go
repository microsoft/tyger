// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"io"
	"reflect"
	"strings"
	"text/template"
	"unicode"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Masterminds/sprig/v3"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
	"sigs.k8s.io/yaml"
)

//go:embed config-pretty.tpl
var prettyPrintConfigTemplate string

const (
	ConfigKindCloud         = "azureCloud"
	ConfigKindAccessControl = "accessControl"
)

type CloudEnvironmentConfig struct {
	install.ConfigFileCommon `json:",inline"`
	EnvironmentName          string       `json:"environmentName"`
	Cloud                    *CloudConfig `json:"cloud"`

	Organizations []*OrganizationConfig `json:"organizations"`
}

func (c *CloudEnvironmentConfig) GetSingleOrg() *OrganizationConfig {
	if len(c.Organizations) != 1 {
		panic("GetSingleOrganization called on environment with multiple organizations")
	}
	return c.Organizations[0]
}

func (c *CloudEnvironmentConfig) ForEachOrgInParallel(ctx context.Context, action func(context.Context, *OrganizationConfig) error) error {
	pg := &install.PromiseGroup{}
	for _, org := range c.Organizations {
		org := org
		ctx := log.Ctx(ctx).With().Str("organization", org.Name).Logger().WithContext(ctx)
		install.NewPromise(ctx, pg, func(ctx context.Context) (any, error) {
			err := action(ctx, org)
			return nil, err
		})
	}

	// wait for the tasks to complete
	for _, p := range *pg {
		if err := p.AwaitErr(); err != nil && err != install.ErrDependencyFailed {
			return err
		}
	}

	return nil
}

type CloudConfig struct {
	TenantID              string                `json:"tenantId"`
	SubscriptionID        string                `json:"subscriptionId"`
	DefaultLocation       string                `json:"defaultLocation"`
	ResourceGroup         string                `json:"resourceGroup"`
	Compute               *ComputeConfig        `json:"compute"`
	Database              *DatabaseServerConfig `json:"database"`
	LogAnalyticsWorkspace *NamedAzureResource   `json:"logAnalyticsWorkspace"`
	DnsZone               *NamedAzureResource   `json:"dnsZone"`
	TlsCertificate        *TlsCertificate       `json:"tlsCertificate"`

	// Internal support for associating resources with a network security perimeter profile
	NetworkSecurityPerimeter *NetworkSecurityPerimeterConfig `json:"networkSecurityPerimeter"`
}

type ComputeConfig struct {
	Clusters                   []*ClusterConfig  `json:"clusters"`
	ManagementPrincipals       []Principal       `json:"managementPrincipals"`
	LocalDevelopmentIdentityId string            `json:"localDevelopmentIdentityId"` // undocumented - for local development only
	PrivateContainerRegistries []string          `json:"privateContainerRegistries"`
	DnsLabel                   string            `json:"dnsLabel"`
	Helm                       *SharedHelmConfig `json:"helm"`
}

func (c *ComputeConfig) GetManagementPrincipalIds() []string {
	var ids []string
	for _, p := range c.ManagementPrincipals {
		ids = append(ids, p.ObjectId)
	}
	return ids
}

type TlsCertificate struct {
	KeyVault        *NamedAzureResource `json:"keyVault"`
	CertificateName string              `json:"certificateName"`
}

type NamedAzureResource struct {
	ResourceGroup string `json:"resourceGroup"`
	Name          string `json:"name"`
}

type PrincipalKind string

const (
	PrincipalKindUser             PrincipalKind = "User"
	PrincipalKindGroup            PrincipalKind = "Group"
	PrincipalKindServicePrincipal PrincipalKind = "ServicePrincipal"
)

type Principal struct {
	Kind              PrincipalKind `json:"kind" yaml:"kind"`
	ObjectId          string        `json:"objectId,omitempty" yaml:"objectId,omitempty"`
	UserPrincipalName string        `json:"userPrincipalName,omitempty" yaml:"userPrincipalName,omitempty"`
	DisplayName       string        `json:"displayName,omitempty" yaml:"displayName,omitempty"`
}

type TygerRbacRoleAssignment struct {
	Principal `json:",inline"`
	Details   *aadAppRoleAssignment `json:"-"`
}

func (a *TygerRbacRoleAssignment) UnmarshalJSON(data []byte) error {
	return json.Unmarshal(data, &a.Principal)
}

func (a *TygerRbacRoleAssignment) String() string {
	switch a.Principal.Kind {
	case PrincipalKindUser:
		return fmt.Sprintf("user '%s'", a.Principal.UserPrincipalName)
	case PrincipalKindGroup:
		return fmt.Sprintf("group '%s'", a.Principal.DisplayName)
	case PrincipalKindServicePrincipal:
		return fmt.Sprintf("service principal '%s'", a.Principal.DisplayName)
	default:
		panic(fmt.Sprintf("unknown principal kind '%s'", a.Principal.Kind))
	}
}

type TygerRbacRoleAssignments struct {
	Owner       []TygerRbacRoleAssignment `json:"owner" yaml:"owner"`
	Contributor []TygerRbacRoleAssignment `json:"contributor" yaml:"contributor"`
}

func (c *ComputeConfig) GetApiHostCluster() *ClusterConfig {
	for _, c := range c.Clusters {
		if c.ApiHost {
			return c
		}
	}

	panic("API host cluster not found - this should have been caught during validation")
}

type ClusterConfig struct {
	Name              string                                    `json:"name"`
	ApiHost           bool                                      `json:"apiHost"`
	Location          string                                    `json:"location"`
	Sku               armcontainerservice.ManagedClusterSKUTier `json:"sku"`
	KubernetesVersion string                                    `json:"kubernetesVersion,omitempty"`
	SystemNodePool    *NodePoolConfig                           `json:"systemNodePool"`
	UserNodePools     []*NodePoolConfig                         `json:"userNodePools"`
}

type NodePoolConfig struct {
	Name     string `json:"name"`
	VMSize   string `json:"vmSize"`
	OsSku    string `json:"osSku"`
	MinCount int32  `json:"minCount"`
	MaxCount int32  `json:"maxCount"`
}

type StorageConfig struct {
	Buffers []*StorageAccountConfig `json:"buffers"`
	Logs    *StorageAccountConfig   `json:"logs"`
}

type NetworkSecurityPerimeterConfig struct {
	NspResourceGroup string                                 `json:"nspResourceGroup"`
	NspName          string                                 `json:"nspName"`
	StorageProfile   *NetworkSecurityPerimeterProfileConfig `json:"storageProfile"`
}

type NetworkSecurityPerimeterProfileConfig struct {
	Name string `json:"name"`
	Mode string `json:"mode"`
}

type StorageAccountConfig struct {
	Name            string `json:"name"`
	Location        string `json:"location"`
	Sku             string `json:"sku"`
	DnsEndpointType string `json:"dnsEndpointType"`
}

type DatabaseServerConfig struct {
	ServerName           string          `json:"serverName"`
	Location             string          `json:"location"`
	ComputeTier          string          `json:"computeTier"`
	VMSize               string          `json:"vmSize"`
	FirewallRules        []*FirewallRule `json:"firewallRules,omitempty"`
	PostgresMajorVersion *int            `json:"postgresMajorVersion"`
	StorageSizeGB        *int            `json:"storageSizeGB"`
	BackupRetentionDays  *int            `json:"backupRetentionDays"`
	BackupGeoRedundancy  bool            `json:"backupGeoRedundancy"`
}

type FirewallRule struct {
	Name           string `json:"name"`
	StartIpAddress string `json:"startIpAddress"`
	EndIpAddress   string `json:"endIpAddress"`
}

type OrganizationConfig struct {
	Name                                string                   `json:"name"`
	SingleOrganizationCompatibilityMode bool                     `json:"singleOrganizationCompatibilityMode"`
	Cloud                               *OrganizationCloudConfig `json:"cloud"`
	Api                                 *OrganizationApiConfig   `json:"api"`
}

type OrganizationCloudConfig struct {
	ResourceGroup       string                     `json:"-"`
	DatabaseName        string                     `json:"databaseName"`
	KubernetesNamespace string                     `json:"-"`
	Storage             *OrganizationStorageConfig `json:"storage"`
	Identities          []string                   `json:"identities"`
}

type OrganizationStorageConfig struct {
	Buffers []*StorageAccountConfig `json:"buffers"`
	Logs    *StorageAccountConfig   `json:"logs"`
}

type TlsCertificateProvider string

const (
	TlsCertificateProviderLetsEncrypt TlsCertificateProvider = "LetsEncrypt"
	TlsCertificateProviderKeyVault    TlsCertificateProvider = "KeyVault"
)

type OrganizationApiConfig struct {
	DomainName             string                  `json:"domainName"`
	TlsCertificateProvider TlsCertificateProvider  `json:"tlsCertificateProvider"`
	AccessControl          *AccessControlConfig    `json:"accessControl"`
	Buffers                *BuffersConfig          `json:"buffers"`
	Helm                   *OrganizationHelmConfig `json:"helm"`
}

type StandaloneAccessControlConfig struct {
	install.ConfigFileCommon `json:",inline"`
	*AccessControlConfig     `json:",inline"`
}

type AccessControlConfig struct {
	TenantID                   string                    `json:"tenantId"`
	ApiAppUri                  string                    `json:"apiAppUri"`
	ApiAppId                   string                    `json:"apiAppId"`
	CliAppUri                  string                    `json:"cliAppUri"`
	CliAppId                   string                    `json:"cliAppId"`
	ServiceManagementReference string                    `json:"serviceManagementReference,omitempty"`
	RoleAssignments            *TygerRbacRoleAssignments `json:"roleAssignments"`
}

type OrganizationHelmConfig struct {
	Tyger *HelmChartConfig `json:"tyger"`
}

type SharedHelmConfig struct {
	Traefik            *HelmChartConfig `json:"traefik"`
	CertManager        *HelmChartConfig `json:"certManager"`
	NvidiaDevicePlugin *HelmChartConfig `json:"nvidiaDevicePlugin"`
}

type HelmChartConfig struct {
	Namespace   string         `json:"namespace"`
	ReleaseName string         `json:"releaseName"`
	RepoName    string         `json:"repoName"`
	RepoUrl     string         `json:"repoUrl"`
	Version     string         `json:"version"`
	ChartRef    string         `json:"chartRef"`
	Values      map[string]any `json:"values"`
}

type BuffersConfig struct {
	ActiveLifetime      string `json:"activeLifetime"`
	SoftDeletedLifetime string `json:"softDeletedLifetime"`
}

type ConfigTemplateValues struct {
	EnvironmentName          string
	ResourceGroup            string
	TenantId                 string
	SubscriptionId           string
	DefaultLocation          string
	KubernetesVersion        string
	ManagementPrincipal      Principal
	TygerPrincipal           Principal
	DatabaseServerName       string
	PostgresMajorVersion     int
	BufferStorageAccountName string
	LogsStorageAccountName   string
	DomainName               string
	ApiTenantId              string
	CpuNodePoolMinCount      int32
	GpuNodePoolMinCount      int32
}

func RenderConfig(templateValues ConfigTemplateValues, writer io.Writer) error {
	config := CloudEnvironmentConfig{
		ConfigFileCommon: install.ConfigFileCommon{
			Kind: ConfigKindCloud,
		},
		EnvironmentName: templateValues.EnvironmentName,
		Cloud: &CloudConfig{
			TenantID:        templateValues.TenantId,
			SubscriptionID:  templateValues.SubscriptionId,
			DefaultLocation: templateValues.DefaultLocation,
			ResourceGroup:   templateValues.ResourceGroup,
			Compute: &ComputeConfig{
				Clusters: []*ClusterConfig{
					{
						Name:              templateValues.EnvironmentName,
						ApiHost:           true,
						KubernetesVersion: templateValues.KubernetesVersion,
						SystemNodePool: &NodePoolConfig{
							Name:     "system",
							VMSize:   "Standard_DS2_v2",
							MinCount: 1,
							MaxCount: 3,
						},
						UserNodePools: []*NodePoolConfig{
							{
								Name:     "cpunp",
								VMSize:   "Standard_DS2_v2",
								MinCount: templateValues.CpuNodePoolMinCount,
								MaxCount: 10,
							},
							{
								Name:     "gpunp",
								VMSize:   "Standard_NC6s_v3",
								MinCount: templateValues.GpuNodePoolMinCount,
								MaxCount: 10,
							}},
					},
				},
				ManagementPrincipals: []Principal{templateValues.ManagementPrincipal},
			},
			Database: &DatabaseServerConfig{
				ServerName:           templateValues.DatabaseServerName,
				PostgresMajorVersion: &templateValues.PostgresMajorVersion,
			},
		},
		Organizations: []*OrganizationConfig{
			{
				Name: "default",
				Cloud: &OrganizationCloudConfig{
					Storage: &OrganizationStorageConfig{
						Buffers: []*StorageAccountConfig{
							{
								Name: templateValues.BufferStorageAccountName,
							},
						},
						Logs: &StorageAccountConfig{
							Name: templateValues.LogsStorageAccountName,
						},
					},
				},
				Api: &OrganizationApiConfig{
					DomainName:             templateValues.DomainName,
					TlsCertificateProvider: TlsCertificateProviderLetsEncrypt,
					AccessControl: &AccessControlConfig{
						TenantID:  templateValues.ApiTenantId,
						ApiAppUri: "api://tyger-server",
						CliAppUri: "api://tyger-cli",
						RoleAssignments: &TygerRbacRoleAssignments{
							Owner: []TygerRbacRoleAssignment{
								{
									Principal: templateValues.TygerPrincipal,
								},
							},
						},
					},
				},
			},
		},
	}

	return PrettyPrintConfig(&config, writer)
}

func PrettyPrintConfig(config *CloudEnvironmentConfig, writer io.Writer) error {
	t := template.Must(template.New("config").Funcs(funcMap()).Parse(prettyPrintConfigTemplate))
	return t.Execute(writer, config)
}

func funcMap() template.FuncMap {
	f := sprig.FuncMap()
	f["indent"] = indent
	f["nindent"] = nindent
	f["toYAML"] = toYAML
	f["optionalField"] = optionalField
	f["renderSharedHelm"] = renderSharedHelm
	f["renderOrgHelm"] = renderOrgHelm
	f["renderAccessControlConfig"] = func(config *AccessControlConfig) string {
		buf := bytes.Buffer{}
		err := PrettyPrintAccessControlConfig(config, &buf)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to pretty print access control config")
		}

		return buf.String()
	}

	return f
}

func optionalField(name string, value any, comment string) string {
	// treat “empty” the same way Go’s templates do
	isEmpty := func(x any) bool {
		return x == nil ||
			x == "" ||
			x == false ||
			(reflect.ValueOf(x).Kind() == reflect.Slice &&
				reflect.ValueOf(x).Len() == 0)
	}

	if isEmpty(value) {
		if comment != "" {
			comment = " " + comment
		}

		return fmt.Sprintf("# %s:%s", name, comment)
	}

	if ptrValue := reflect.ValueOf(value); ptrValue.Kind() == reflect.Ptr && !ptrValue.IsNil() {
		value = ptrValue.Elem().Interface()
	}

	return fmt.Sprintf("%s: %v", name, value)
}

func toYAML(v any) string {
	data, err := yaml.Marshal(v)
	if err != nil {
		// Swallow errors inside of a template.
		return ""
	}
	return strings.TrimSuffix(string(data), "\n")
}

func renderHelm(config *HelmChartConfig) string {
	w := &strings.Builder{}
	if config == nil {
		config = &HelmChartConfig{}
	}

	fmt.Fprintf(w, "%s\n", optionalField("repoName", config.RepoName, ""))
	fmt.Fprintf(w, "%s\n", optionalField("repoUrl", config.RepoUrl, "not set if using `chartRef`"))
	fmt.Fprintf(w, "%s\n", optionalField("chartRef", config.ChartRef, "e.g. oci://..."))
	fmt.Fprintf(w, "%s\n", optionalField("version", config.Version, ""))
	if len(config.Values) > 0 {
		w.WriteString("values:\n")
		w.WriteString(indent(2, toYAML(config.Values)))
	} else {
		fmt.Fprintf(w, "# values:")
	}

	return w.String()
}

func renderSharedHelm(config *SharedHelmConfig) string {
	w := &strings.Builder{}
	if config == nil {
		fmt.Fprintf(w, "# helm:\n")
		config = &SharedHelmConfig{}
	} else {
		fmt.Fprintf(w, "helm:\n")
	}

	if config.Traefik == nil {
		w.WriteString("  # traefik:\n")
	} else {
		w.WriteString("  traefik:\n")
	}

	w.WriteString(indent(4, renderHelm(config.Traefik)) + "\n")

	if config.CertManager == nil {
		w.WriteString("  # certManager:\n")
	} else {
		w.WriteString("  certManager:\n")
		w.WriteString(indent(4, renderHelm(config.CertManager)) + "\n")
	}

	if config.NvidiaDevicePlugin == nil {
		w.WriteString("  # nvidiaDevicePlugin:")
	} else {
		w.WriteString("  nvidiaDevicePlugin:\n")
		w.WriteString(indent(4, renderHelm(config.NvidiaDevicePlugin)))
	}

	return alignConsecutiveCommentLinesByColumn(w.String())
}

func renderOrgHelm(config *OrganizationHelmConfig) string {
	w := &strings.Builder{}
	if config == nil {
		fmt.Fprintf(w, "# helm:\n")
		config = &OrganizationHelmConfig{}
	} else {
		fmt.Fprintf(w, "helm:\n")
	}

	if config.Tyger == nil {
		w.WriteString("  # tyger:\n")
	} else {
		w.WriteString("  tyger:\n")
	}

	w.WriteString(indent(4, renderHelm(config.Tyger)))
	return alignConsecutiveCommentLinesByColumn(w.String())
}

// github.com/goccy/go-yaml gets confused when editing YAML files with comment blocks
// that are not aligned. So this function aligns consecutive comment lines.
// For example, this:
//
//	# helm:
//	  # traefik:
//	    # repoName:
//	    # repoUrl: not set if using `chartRef`
//
// Becomes:
//
//	# helm:
//	#   traefik:
//	#     repoName:
//	#     repoUrl: not set if using `chartRef`
func alignConsecutiveCommentLinesByColumn(yamlString string) string {
	lines := strings.Split(yamlString, "\n")
	var result []string

	var commentBlock []string
	var minHashCol int

	flushBlock := func() {
		if len(commentBlock) == 0 {
			return
		}
		// Find the minimum column where '#' appears in the block
		minHashCol = -1
		for _, line := range commentBlock {
			hashIdx := strings.Index(line, "#")
			if hashIdx >= 0 && (minHashCol == -1 || hashIdx < minHashCol) {
				minHashCol = hashIdx
			}
		}
		// Align all '#' to minHashCol
		for _, line := range commentBlock {
			hashIdx := strings.Index(line, "#")
			afterHash := line[hashIdx+1:]
			aligned := strings.Repeat(" ", minHashCol) + "#" + strings.Repeat(" ", max(0, hashIdx-minHashCol)) + afterHash
			result = append(result, aligned)
		}
		commentBlock = nil
		minHashCol = 0
	}

	for _, line := range lines {
		trimmed := strings.TrimLeft(line, " ")
		if strings.HasPrefix(trimmed, "#") {
			commentBlock = append(commentBlock, line)
		} else {
			flushBlock()
			result = append(result, line)
		}
	}
	flushBlock()
	return strings.Join(result, "\n")
}

func indent(spaces int, v string) string {
	pad := strings.Repeat(" ", spaces)
	lines := strings.Split(v, "\n")
	for i := range lines {
		line := strings.TrimRightFunc(lines[i], unicode.IsSpace)
		if len(line) > 0 {
			line = pad + line
		}

		lines[i] = line
	}

	return strings.Join(lines, "\n")
}

func nindent(spaces int, v string) string {
	return "\n" + indent(spaces, v)
}
