package install

import (
	_ "embed"
	"io"
	"strings"
	"text/template"
)

//go:embed config.tpl
var configTemplate string

type EnvironmentConfig struct {
	EnvironmentName string       `json:"environmentName"`
	Cloud           *CloudConfig `json:"cloud"`
	Api             *ApiConfig   `json:"api"`
}

type CloudConfig struct {
	TenantID        string          `json:"tenantId"`
	SubscriptionID  string          `json:"subscriptionId"`
	DefaultLocation string          `json:"defaultLocation"`
	ResourceGroup   string          `json:"resourceGroup"`
	Compute         *ComputeConfig  `json:"compute"`
	Storage         *StorageConfig  `json:"storage"`
	DatabaseConfig  *DatabaseConfig `json:"database"`
}

type ComputeConfig struct {
	Clusters                   []*ClusterConfig    `json:"clusters"`
	LogAnalyticsWorkspace      *NamedAzureResource `json:"logAnalyticsWorkspace"`
	ManagementPrincipals       []AksPrincipal      `apjson:"managementPrincipals"`
	PrivateContainerRegistries []string            `json:"privateContainerRegistries"`
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
	ServerName            string `json:"serverName"`
	Location              string `json:"location"`
	ComputeTier           string `json:"computeTier"`
	VMSize                string `json:"vmSize"`
	PostgresMajorVersion  int    `json:"postgresMajorVersion"`
	InitialDatabaseSizeGb int    `json:"initialDatabaseSizeGb"`
	BackupRetentionDays   int    `json:"backupRetentionDays"`
	BackupGeoRedundancy   bool   `json:"backupGeoRedundancy"`
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
	RepoName  string         `json:"repoName"`
	RepoUrl   string         `json:"repoUrl"`
	Version   string         `json:"version"`
	ChartRef  string         `json:"chartRef"`
	Namespace string         `json:"namespace"`
	Values    map[string]any `json:"values"`
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
	BufferStorageAccountName string
	LogsStorageAccountName   string
	DomainName               string
	ApiTenantId              string
}

func RenderConfig(templateValues ConfigTemplateValues, writer io.Writer) error {
	funcs := map[string]any{
		"contains": strings.Contains,
	}

	t := template.Must(template.New("config").Funcs(funcs).Parse(configTemplate))

	return t.Execute(writer, templateValues)
}
