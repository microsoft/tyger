package install

import (
	_ "embed"
	"io"
	"text/template"
)

//go:embed config.tmpl
var configTemplate string

type EnvironmentConfig struct {
	EnvironmentName string       `json:"environmentName"`
	Cloud           *CloudConfig `json:"cloud"`
	Api             *ApiConfig   `json:"api"`
}

type CloudConfig struct {
	TenantID        string         `json:"tenantId"`
	SubscriptionID  string         `json:"subscriptionId"`
	DefaultLocation string         `json:"defaultLocation"`
	ResourceGroup   string         `json:"resourceGroup"`
	Compute         *ComputeConfig `json:"compute"`
	Storage         *StorageConfig `json:"storage"`
}

type ComputeConfig struct {
	Clusters                   []*ClusterConfig `json:"clusters"`
	ManagementPrincipalIds     []string         `json:"managementPrincipalIds"`
	PrivateContainerRegistries []string         `json:"privateContainerRegistries"`
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
	Name              string            `json:"name"`
	ApiHost           bool              `json:"apiHost"`
	Location          string            `json:"location"`
	KubernetesVersion string            `json:"kubernetesVersion,omitempty"`
	UserNodePools     []*NodePoolConfig `json:"userNodePools"`
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
	TenantId                 string
	SubscriptionId           string
	DefaultLocation          string
	CurrentUserId            string
	CurrentUserDisplayName   string
	BufferStorageAccountName string
	LogsStorageAccountName   string
	DomainName               string
	ApiTenantId              string
}

func RenderConfig(templateValues *ConfigTemplateValues, writer io.Writer) error {
	t, err := template.New("config").Parse(configTemplate)
	if err != nil {
		panic(err)
	}

	return t.Execute(writer, templateValues)
}
