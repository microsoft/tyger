package tyger

import "strings"

#EnvironmentConfig: {
	environmentName!: string
	cloud!:           #CloudConfig & {
		resourceGroup: *environmentName | string
		compute: {
			#DefaultedClusterConfig: #ClusterConfig & {
				location: *cloud.defaultLocation | string
			}
			clusters: [#DefaultedClusterConfig, ...#DefaultedClusterConfig]
		}
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

#Principal: {
	id!: string
	kind!:     "User" | "Group" | "ServicePrincipal"
}

#ComputeConfig: {
	clusters!: [...#ClusterConfig]
	managementPrincipals?: [...#Principal]
	logAnalyticsWorkspace?: #NamedAzureResource
	privateContainerRegistries?: [...string]
}

#NamedAzureResource: {
	resourceGroup!: string
	name!:          string
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
	chartRef?:  string
	values?: [string]: _
}

#DeveloperConfig: {
	wipContainerRegistry:      #ContainerRegistry
	officialContainerRegistry: #ContainerRegistry

	keyVault!:         string
	testAppUri!:       string
	pemCertSecret!:    #Secret
	pkcs12CertSecret!: #Secret
}

#Secret: {
	name!:    string
	version!: string
}

#ContainerRegistry: {
	name!: string
	fqdn:  *"\(name).azurecr.io" | string
}
