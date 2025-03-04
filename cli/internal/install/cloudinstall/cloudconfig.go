// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	_ "embed"
	"io"
	"strings"
	"text/template"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
)

//go:embed config.tpl
var configTemplate string

const (
	EnvironmentKindCloud = "azureCloud"
)

type CloudEnvironmentConfig struct {
	Kind            string         `json:"kind"`
	EnvironmentName string         `json:"environmentName"`
	Cloud           *CloudConfig   `json:"cloud"`
	Api             *ApiConfig     `json:"api"`
	Buffers         *BuffersConfig `json:"buffers"`
}

type CloudConfig struct {
	TenantID              string              `json:"tenantId"`
	SubscriptionID        string              `json:"subscriptionId"`
	DefaultLocation       string              `json:"defaultLocation"`
	ResourceGroup         string              `json:"resourceGroup"`
	Compute               *ComputeConfig      `json:"compute"`
	Storage               *StorageConfig      `json:"storage"`
	DatabaseConfig        *DatabaseConfig     `json:"database"`
	LogAnalyticsWorkspace *NamedAzureResource `json:"logAnalyticsWorkspace"`
}

type ComputeConfig struct {
	Clusters                   []*ClusterConfig `json:"clusters"`
	ManagementPrincipals       []Principal      `json:"managementPrincipals"`
	LocalDevelopmentIdentityId string           `json:"localDevelopmentIdentityId"` // undocumented - for local development only
	PrivateContainerRegistries []string         `json:"privateContainerRegistries"`
	Identities                 []string         `json:"identities"`
}

func (c *ComputeConfig) GetManagementPrincipalIds() []string {
	var ids []string
	for _, p := range c.ManagementPrincipals {
		ids = append(ids, p.ObjectId)
	}
	return ids
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
	ObjectId          string        `json:"objectId"`
	UserPrincipalName string        `json:"userPrincipalName"`
	Kind              PrincipalKind `json:"kind"`

	// Deprecated: Id is deprecated. Use ObjectId instead
	Id string `json:"id"`
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
	MinCount int32  `json:"minCount"`
	MaxCount int32  `json:"maxCount"`
}

type StorageConfig struct {
	Buffers []*StorageAccountConfig `json:"buffers"`
	Logs    *StorageAccountConfig   `json:"logs"`
}

type StorageAccountConfig struct {
	Name     string `json:"name"`
	Location string `json:"location"`
	Sku      string `json:"sku"`
}

type DatabaseConfig struct {
	ServerName           string          `json:"serverName"`
	Location             string          `json:"location"`
	ComputeTier          string          `json:"computeTier"`
	VMSize               string          `json:"vmSize"`
	FirewallRules        []*FirewallRule `json:"firewallRules,omitempty"`
	PostgresMajorVersion int             `json:"postgresMajorVersion"`
	StorageSizeGB        int             `json:"storageSizeGB"`
	BackupRetentionDays  int             `json:"backupRetentionDays"`
	BackupGeoRedundancy  bool            `json:"backupGeoRedundancy"`
}

type FirewallRule struct {
	Name           string `json:"name"`
	StartIpAddress string `json:"startIpAddress"`
	EndIpAddress   string `json:"endIpAddress"`
}

type ApiConfig struct {
	DomainName string      `json:"domainName"`
	Auth       *AuthConfig `json:"auth"`
	Helm       *HelmConfig `json:"helm"`
}

type AuthConfig struct {
	TenantID  string `json:"tenantId"`
	ApiAppUri string `json:"apiAppUri"`
	CliAppUri string `json:"cliAppUri"`
}

type HelmConfig struct {
	Tyger              *HelmChartConfig `json:"tyger"`
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
	CurrentIpAddress         string
	CpuNodePoolMinCount      int
	GpuNodePoolMinCount      int
}

func RenderConfig(templateValues ConfigTemplateValues, writer io.Writer) error {
	funcs := map[string]any{
		"contains": strings.Contains,
	}

	t := template.Must(template.New("config").Funcs(funcs).Parse(configTemplate))

	return t.Execute(writer, templateValues)
}
