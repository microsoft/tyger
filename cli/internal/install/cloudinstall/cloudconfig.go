// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	_ "embed"
	"fmt"
	"io"
	"reflect"
	"strings"
	"text/template"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Masterminds/sprig/v3"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
	"sigs.k8s.io/yaml"
)

//go:embed config-pretty.tpl
var prettyPrintConfigTemplate string

const (
	EnvironmentKindCloud = "azureCloud"
)

type CloudEnvironmentConfig struct {
	Kind            string       `json:"kind"`
	EnvironmentName string       `json:"environmentName"`
	Cloud           *CloudConfig `json:"cloud"`

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
	Auth                   *AuthConfig             `json:"auth"`
	Buffers                *BuffersConfig          `json:"buffers"`
	Helm                   *OrganizationHelmConfig `json:"helm"`
}

type AuthConfig struct {
	RbacEnabled *bool  `json:"rbacEnabled" yaml:"rbacEnabled"`
	TenantID    string `json:"tenantId" yaml:"tenantId"`
	ApiAppUri   string `json:"apiAppUri" yaml:"apiAppUri"`
	ApiAppId    string `json:"apiAppId" yaml:"apiAppId"`
	CliAppUri   string `json:"cliAppUri" yaml:"cliAppUri"`
	CliAppId    string `json:"cliAppId" yaml:"cliAppId"`
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
	Principal                Principal
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
		Kind:            EnvironmentKindCloud,
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
				ManagementPrincipals: []Principal{templateValues.Principal},
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
					Auth: &AuthConfig{
						TenantID:  templateValues.ApiTenantId,
						ApiAppUri: "api://tyger-server",
						CliAppUri: "api://tyger-cli",
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
	f["toYAML"] = toYAML
	f["optionalField"] = optionalField
	f["renderHelm"] = renderHelm
	f["renderSharedHelm"] = renderSharedHelm
	f["renderOrgHelm"] = renderOrgHelm
	f["deref"] = deref
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

func deref(v any) string {
	if v == nil {
		return ""
	}

	if ptrValue := reflect.ValueOf(v); ptrValue.Kind() == reflect.Ptr {
		if ptrValue.IsNil() {
			return ""
		}

		return fmt.Sprintf("%v", ptrValue.Elem().Interface())
	}

	return fmt.Sprintf("%v", v)
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

	return w.String()
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
	return w.String()
}

func indent(spaces int, v string) string {
	pad := strings.Repeat(" ", spaces)
	return pad + strings.Replace(v, "\n", "\n"+pad, -1)
}
