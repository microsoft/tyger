package tyger

import "strings"

#EnvironmentConfig: {
	environmentName!: string
	cloud!:           #CloudConfig & {
		storage: {
			buffers: *[{name: *strings.Replace("\(environmentName)tygerbuf", "-", "", -1) | string}] | [#StorageAccountConfig, ...#StorageAccountConfig]
			logs: {name: *strings.Replace("\(environmentName)tygerlog", "-", "", -1) | string}
		}
	}
	api!: #ApiConfig & {
		domainName: *"\(environmentName)-tyger.\(cloud.defaultLocation).cloudapp.azure.com" | string
	}
}

#CloudConfig: {
	tenantId!:        string
	subscriptionId!:  string
	defaultLocation!: string
	resourceGroup?:   string
	compute!:         #ComputeConfig
	storage!:         #StorageConfig
}

#ComputeConfig: {
	clusters!: [...#ClusterConfig]
	managementPrincipalIds?: [...string]
	privateContainerRegistries?: [...string]
}

#ClusterConfig: {
	name!:              string
	apiHost!:           bool
	location?:          string
	kubernetesVersion?: string
	userNodePools!: [...#NodePoolConfig]
}

#NodePoolConfig: {
	name!:     string
	vmSize!:   string
	minCount?: int
	maxCount!: int
}

#StorageConfig: {
	buffers!: [#StorageAccountConfig, ...#StorageAccountConfig]
	logs!: #StorageAccountConfig
}

#StorageAccountConfig: {
	name!:     string
	location?: string
	sku?:      string
}

#ApiConfig: {
	domainName!: string
	auth:        #AuthConfig
	helm:        #HelmConfig
}

#AuthConfig: {
	tenantId!:  string
	apiAppUri!: string
	cliAppUri!: string
}

#HelmConfig: {
	tyger: #HelmChartConfig & {
		chartRef: string @tag(tygerHelmChartDir)
	}
	traefik?:            #HelmChartConfig
	certManager?:        #HelmChartConfig
	nvidiaDevicePlugin?: #HelmChartConfig
}

#HelmChartConfig: {
	chartRepo?: string
	chartRef?: string
	values?: [string]: _
}

#DeveloperConfig: {
	containerRegistry!: string
	containerRegistryFQDN: *"\(containerRegistry).azurecr.io" | string
}
