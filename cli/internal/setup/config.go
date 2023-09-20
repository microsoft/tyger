package setup

type Config struct {
	EnvironmentName string `json:"environmentName"`
	SubscriptionID  string `json:"subscriptionId"`
	Location        string `json:"location"`

	AttachedContainerRegistries   []string `json:"containerRegistries"`
	ClusterUserPrincipalObjectIds []string `json:"clusterUserPrincipalObjectIds"`

	Clusters []*ClusterConfig        `json:"clusters"`
	Buffers  []*StorageAccountConfig `json:"buffers"`
}

func (c *Config) GetControlPlaneCluster() *ClusterConfig {
	for _, cluster := range c.Clusters {
		if cluster.ControlPlane != nil {
			return cluster
		}
	}

	panic("no control plane cluster found - this should have been validated")
}

type ClusterConfig struct {
	Name              string                     `json:"name"`
	Location          string                     `json:"location"`
	KubernetesVersion string                     `json:"kubernetesVersion"`
	UserNodePools     []*NodePoolConfig          `json:"userNodePools"`
	ControlPlane      *ControlPlaneClusterConfig `json:"controlPlane,omitempty"`
}

type ControlPlaneClusterConfig struct {
	DnsLabel           string                `json:"dnsLabel"`
	LogStorage         *StorageAccountConfig `json:"logStorage"`
	TraefikVersion     string                `json:"traefikVersion,omitempty"`
	CertManagerVersion string                `json:"certManagerVersion,omitempty"`
}

type NodePoolConfig struct {
	Name     string `json:"name"`
	VMSize   string `json:"vmSize"`
	MinCount int32  `json:"minCount"`
	MaxCount int32  `json:"maxCount"`
	Count    int32  `json:"count"`
}

type StorageAccountConfig struct {
	Name     string `json:"name"`
	Location string `json:"location"`
	Sku      string `json:"sku"`
}

type Options struct {
	SkipClusterSetup bool
	SkipAttachAcr    bool
}
