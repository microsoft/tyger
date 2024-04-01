// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	_ "embed"
	"io"
	"strings"
	"text/template"
)

//go:embed config.tpl
var configTemplate string

type EnvironmentKind string

const (
	EnvironmentKindCloud  EnvironmentKind = "azureCloud"
	EnvironmentKindDocker EnvironmentKind = "docker"
)

type EnvironmentConfigCommon struct {
	Kind EnvironmentKind `json:"kind"`
}

type EnvironmentConfig interface {
	_environmentConfig()
}

type CloudEnvironmentConfig struct {
	EnvironmentConfigCommon
	EnvironmentName string       `json:"environmentName"`
	Cloud           *CloudConfig `json:"cloud"`
	Api             *ApiConfig   `json:"api"`
}

func (c *CloudEnvironmentConfig) _environmentConfig() {}

var _ EnvironmentConfig = &CloudEnvironmentConfig{}

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
	ManagementPrincipals       []AksPrincipal   `json:"managementPrincipals"`
	PrivateContainerRegistries []string         `json:"privateContainerRegistries"`
}

type NamedAzureResource struct {
	ResourceGroup string `json:"resourceGroup"`
	Name          string `json:"name"`
}

type AksPrincipal struct {
	Kind PrincipalKind `json:"kind"`
	Id   string        `json:"id"`
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
	Name                       string            `json:"name"`
	ApiHost                    bool              `json:"apiHost"`
	Location                   string            `json:"location"`
	KubernetesVersion          string            `json:"kubernetesVersion,omitempty"`
	UserNodePools              []*NodePoolConfig `json:"userNodePools"`
	LocalDevelopmentIdentityId string            `json:"localDevelopmentIdentityId"` // undocumented - for local development only
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

type DockerEnvironmentConfig struct {
	EnvironmentConfigCommon

	EnvironmentName string `json:"environmentName"`

	PostgresImage      string `json:"postgresImage"`
	ControlPlaneImage  string `json:"controlPlaneImage"`
	DataPlaneImage     string `json:"dataPlaneImage"`
	BufferSidecarImage string `json:"bufferSidecarImage"`
	GatewayImage       string `json:"gatewayImage"`

	UseGateway bool `json:"useGateway"`
}

func (*DockerEnvironmentConfig) _environmentConfig() {
}

type ConfigTemplateValues struct {
	EnvironmentName          string
	ResourceGroup            string
	TenantId                 string
	SubscriptionId           string
	DefaultLocation          string
	KubernetesVersion        string
	PrincipalId              string
	PrincipalDisplay         string
	PrincipalKind            PrincipalKind
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
