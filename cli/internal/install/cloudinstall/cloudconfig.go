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
	"strings"
	"text/template"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/microsoft/tyger/cli/internal/templatefunctions"
	"github.com/rs/zerolog/log"
)

//go:embed cloud-config-pretty.tpl
var prettyPrintConfigTemplate string

const (
	ConfigKindCloud         = "azureCloud"
	ConfigKindAccessControl = "accessControl"
)

type CloudEnvironmentConfig struct {
	install.ConfigFileCommon `yaml:",inline"`
	EnvironmentName          string       `yaml:"environmentName"`
	Cloud                    *CloudConfig `yaml:"cloud"`

	Organizations []*OrganizationConfig `yaml:"organizations"`
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
	TenantID              string                `yaml:"tenantId"`
	SubscriptionID        string                `yaml:"subscriptionId"`
	DefaultLocation       string                `yaml:"defaultLocation"`
	ResourceGroup         string                `yaml:"resourceGroup"`
	Compute               *ComputeConfig        `yaml:"compute"`
	Database              *DatabaseServerConfig `yaml:"database"`
	LogAnalyticsWorkspace *NamedAzureResource   `yaml:"logAnalyticsWorkspace"`
	DnsZone               *NamedAzureResource   `yaml:"dnsZone"`
	TlsCertificate        *TlsCertificate       `yaml:"tlsCertificate"`

	// Internal support for associating resources with a network security perimeter profile
	NetworkSecurityPerimeter *NetworkSecurityPerimeterConfig `yaml:"networkSecurityPerimeter"`
}

type ComputeConfig struct {
	Clusters                   []*ClusterConfig  `yaml:"clusters"`
	ManagementPrincipals       []Principal       `yaml:"managementPrincipals"`
	LocalDevelopmentIdentityId string            `yaml:"localDevelopmentIdentityId"` // undocumented - for local development only
	PrivateContainerRegistries []string          `yaml:"privateContainerRegistries"`
	ContainerRegistryProxy     string            `yaml:"containerRegistryProxy"` // undocumented and for internal use only
	DnsLabel                   string            `yaml:"dnsLabel"`
	Helm                       *SharedHelmConfig `yaml:"helm"`
}

func (c *ComputeConfig) GetManagementPrincipalIds() []string {
	var ids []string
	for _, p := range c.ManagementPrincipals {
		ids = append(ids, p.ObjectId)
	}
	return ids
}

type TlsCertificate struct {
	KeyVault        *NamedAzureResource `yaml:"keyVault"`
	CertificateName string              `yaml:"certificateName"`
}

type NamedAzureResource struct {
	ResourceGroup string `yaml:"resourceGroup"`
	Name          string `yaml:"name"`
}

type PrincipalKind string

const (
	PrincipalKindUser             PrincipalKind = "User"
	PrincipalKindGroup            PrincipalKind = "Group"
	PrincipalKindServicePrincipal PrincipalKind = "ServicePrincipal"
)

type Principal struct {
	Kind              PrincipalKind `yaml:"kind"`
	ObjectId          string        `yaml:"objectId,omitempty"`
	UserPrincipalName string        `yaml:"userPrincipalName,omitempty"`
	DisplayName       string        `yaml:"displayName,omitempty"`
}

type TygerRbacRoleAssignment struct {
	Principal `yaml:",inline"`
	Details   *aadAppRoleAssignment `yaml:"-"`
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
	Owner       []TygerRbacRoleAssignment `yaml:"owner"`
	Contributor []TygerRbacRoleAssignment `yaml:"contributor"`
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
	Name              string                                    `yaml:"name"`
	ApiHost           bool                                      `yaml:"apiHost"`
	Location          string                                    `yaml:"location"`
	Sku               armcontainerservice.ManagedClusterSKUTier `yaml:"sku"`
	KubernetesVersion string                                    `yaml:"kubernetesVersion,omitempty"`
	SystemNodePool    *NodePoolConfig                           `yaml:"systemNodePool"`
	UserNodePools     []*NodePoolConfig                         `yaml:"userNodePools"`
}

type NodePoolConfig struct {
	Name     string `yaml:"name"`
	VMSize   string `yaml:"vmSize"`
	OsSku    string `yaml:"osSku"`
	MinCount int32  `yaml:"minCount"`
	MaxCount int32  `yaml:"maxCount"`
}

type StorageConfig struct {
	Buffers []*StorageAccountConfig `yaml:"buffers"`
	Logs    *StorageAccountConfig   `yaml:"logs"`
}

type NetworkSecurityPerimeterConfig struct {
	NspResourceGroup string                                 `yaml:"nspResourceGroup"`
	NspName          string                                 `yaml:"nspName"`
	StorageProfile   *NetworkSecurityPerimeterProfileConfig `yaml:"storageProfile"`
}

type NetworkSecurityPerimeterProfileConfig struct {
	Name string `yaml:"name"`
	Mode string `yaml:"mode"`
}

type StorageAccountConfig struct {
	Name            string `yaml:"name"`
	Location        string `yaml:"location"`
	Sku             string `yaml:"sku"`
	DnsEndpointType string `yaml:"dnsEndpointType"`
}

type DatabaseServerConfig struct {
	ServerName           string          `yaml:"serverName"`
	Location             string          `yaml:"location"`
	ComputeTier          string          `yaml:"computeTier"`
	VMSize               string          `yaml:"vmSize"`
	FirewallRules        []*FirewallRule `yaml:"firewallRules,omitempty"`
	PostgresMajorVersion *int            `yaml:"postgresMajorVersion"`
	StorageSizeGB        *int            `yaml:"storageSizeGB"`
	BackupRetentionDays  *int            `yaml:"backupRetentionDays"`
	BackupGeoRedundancy  bool            `yaml:"backupGeoRedundancy"`
}

type FirewallRule struct {
	Name           string `yaml:"name"`
	StartIpAddress string `yaml:"startIpAddress"`
	EndIpAddress   string `yaml:"endIpAddress"`
}

type OrganizationConfig struct {
	Name                                string                   `yaml:"name"`
	SingleOrganizationCompatibilityMode bool                     `yaml:"singleOrganizationCompatibilityMode"`
	Cloud                               *OrganizationCloudConfig `yaml:"cloud"`
	Api                                 *OrganizationApiConfig   `yaml:"api"`
}

type OrganizationCloudConfig struct {
	ResourceGroup       string                     `yaml:"-"`
	DatabaseName        string                     `yaml:"databaseName"`
	KubernetesNamespace string                     `yaml:"-"`
	Storage             *OrganizationStorageConfig `yaml:"storage"`
	Identities          []string                   `yaml:"identities"`
}

type OrganizationStorageConfig struct {
	Buffers []*StorageAccountConfig `yaml:"buffers"`
	Logs    *StorageAccountConfig   `yaml:"logs"`
}

type TlsCertificateProvider string

const (
	TlsCertificateProviderLetsEncrypt TlsCertificateProvider = "LetsEncrypt"
	TlsCertificateProviderKeyVault    TlsCertificateProvider = "KeyVault"
)

type OrganizationApiConfig struct {
	DomainName             string                  `yaml:"domainName"`
	TlsCertificateProvider TlsCertificateProvider  `yaml:"tlsCertificateProvider"`
	AccessControl          *AccessControlConfig    `yaml:"accessControl"`
	Buffers                *BuffersConfig          `yaml:"buffers"`
	Helm                   *OrganizationHelmConfig `yaml:"helm"`
}

type StandaloneAccessControlConfig struct {
	install.ConfigFileCommon `yaml:",inline"`
	*AccessControlConfig     `yaml:",inline"`
}

type AccessControlConfig struct {
	TenantID                   string                    `yaml:"tenantId"`
	ApiAppUri                  string                    `yaml:"apiAppUri"`
	ApiAppId                   string                    `yaml:"apiAppId"`
	CliAppUri                  string                    `yaml:"cliAppUri"`
	CliAppId                   string                    `yaml:"cliAppId"`
	ServiceManagementReference string                    `yaml:"serviceManagementReference,omitempty"`
	RoleAssignments            *TygerRbacRoleAssignments `yaml:"roleAssignments"`
}

type OrganizationHelmConfig struct {
	Tyger *HelmChartConfig `yaml:"tyger"`
}

type SharedHelmConfig struct {
	Traefik            *HelmChartConfig `yaml:"traefik"`
	CertManager        *HelmChartConfig `yaml:"certManager"`
	NvidiaDevicePlugin *HelmChartConfig `yaml:"nvidiaDevicePlugin"`
}

type HelmChartConfig struct {
	Namespace   string         `yaml:"namespace"`
	ReleaseName string         `yaml:"releaseName"`
	RepoName    string         `yaml:"repoName"`
	RepoUrl     string         `yaml:"repoUrl"`
	Version     string         `yaml:"version"`
	ChartRef    string         `yaml:"chartRef"`
	Values      map[string]any `yaml:"values"`
}

type BuffersConfig struct {
	ActiveLifetime      string `yaml:"activeLifetime"`
	SoftDeletedLifetime string `yaml:"softDeletedLifetime"`
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
	OrganizationTenantId     string
	OrganizationName         string
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
				Name: templateValues.OrganizationName,
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
						TenantID:  templateValues.OrganizationTenantId,
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
	f := templatefunctions.GetFuncMap()
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

func renderHelm(config *HelmChartConfig) string {
	w := &strings.Builder{}
	if config == nil {
		config = &HelmChartConfig{}
	}

	fmt.Fprintf(w, "%s\n", templatefunctions.OptionalField("repoName", config.RepoName, ""))
	fmt.Fprintf(w, "%s\n", templatefunctions.OptionalField("repoUrl", config.RepoUrl, "not set if using `chartRef`"))
	fmt.Fprintf(w, "%s\n", templatefunctions.OptionalField("chartRef", config.ChartRef, "e.g. oci://..."))
	fmt.Fprintf(w, "%s\n", templatefunctions.OptionalField("version", config.Version, ""))
	if len(config.Values) > 0 {
		w.WriteString("values:\n")
		w.WriteString(templatefunctions.Indent(2, templatefunctions.ToYaml(config.Values)))
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

	w.WriteString(templatefunctions.Indent(4, renderHelm(config.Traefik)) + "\n")

	if config.CertManager == nil {
		w.WriteString("  # certManager:\n")
	} else {
		w.WriteString("  certManager:\n")
		w.WriteString(templatefunctions.Indent(4, renderHelm(config.CertManager)) + "\n")
	}

	if config.NvidiaDevicePlugin == nil {
		w.WriteString("  # nvidiaDevicePlugin:")
	} else {
		w.WriteString("  nvidiaDevicePlugin:\n")
		w.WriteString(templatefunctions.Indent(4, renderHelm(config.NvidiaDevicePlugin)))
	}

	return templatefunctions.AlignConsecutiveCommentLinesByColumn(w.String())
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

	w.WriteString(templatefunctions.Indent(4, renderHelm(config.Tyger)))
	return templatefunctions.AlignConsecutiveCommentLinesByColumn(w.String())
}
