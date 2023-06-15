package tyger

import "strings"

#Region: "asia" | "asiapacific" | "australia" | "australiacentral" | "australiacentral2" | "australiaeast" | "australiasoutheast" | "brazil" | "brazilsouth" | "brazilsoutheast" | "canada" | "canadacentral" | "canadaeast" | "centralindia" | "centralus" | "centraluseuap" | "centralusstage" | "eastasia" | "eastasiastage" | "eastus" | "eastus2" | "eastus2euap" | "eastus2stage" | "eastusstage" | "europe" | "france" | "francecentral" | "francesouth" | "germany" | "germanynorth" | "germanywestcentral" | "global" | "india" | "japan" | "japaneast" | "japanwest" | "jioindiacentral" | "jioindiawest" | "korea" | "koreacentral" | "koreasouth" | "northcentralus" | "northcentralusstage" | "northeurope" | "norway" | "norwayeast" | "norwaywest" | "southafrica" | "southafricanorth" | "southafricawest" | "southcentralus" | "southcentralusstage" | "southeastasia" | "southeastasiastage" | "southindia" | "swedencentral" | "switzerland" | "switzerlandnorth" | "switzerlandwest" | "uae" | "uaecentral" | "uaenorth" | "uk" | "uksouth" | "ukwest" | "unitedstates" | "unitedstateseuap" | "westcentralus" | "westeurope" | "westindia" | "westus" | "westus2" | "westus2stage" | "westus3" | "westusstage"

#Dependencies: {
	subscription:       string
	dnsZone:            #DnsZone
	containerRegistry:  string
	keyVault:           #KeyVault
	logAnalytics: 	    #LogAnalytics
	servicePrincipalId: string
	userGroupId:        string
}

#Resource: {
	name:               string
	resourceGroup:      string
}

#KeyVault: {
	#Resource
	tlsCertificateName: string
}

#DnsZone: {
	#Resource
}

#LogAnalytics: {
	#Resource
}

#StorageAccount: {
	name:   =~"^[a-z0-9]{3,24}$"
	region: #Region
}

#Environment: {
	let environmentName = name
	name:          =~"^[a-z][a-z\\-0-9]*$"
	resourceGroup: *name | string
	defaultRegion: #Region
	subscription:  *dependencies.subscription | string
	isEphemeral:   *false | bool
	dependencies: #Dependencies

	let defaultCluster = #Cluster & {
		isPrimary: true
		region:    defaultRegion
	}

	clusters: *{"\(environmentName)": defaultCluster} | {[string]: #Cluster}

	// validate that there is exactly one primary cluster
	primaryCluster: string & {
		for k, v in clusters {
			if v.isPrimary {
				k
			}
		}
	}

	#OrganizationWithDefaults: {
		#Organization
		_name:          string
		namespace: *_name | string
		resourceGroup: *"\(environmentName)-\(_name)" | string
		let organizationName = _name
		storage: {
			buffers: *[{name: *strings.Replace("\(environmentName)\(organizationName)buf", "-", "", -1) | string, region: defaultRegion}] | [#StorageAccount, ...#StorageAccount]
			storageServer: {name: *strings.Replace("\(environmentName)\(organizationName)sto", "-", "", -1) | string, region: *defaultRegion | #Region}
			logs: {name: *strings.Replace("\(environmentName)\(organizationName)log", "-", "", -1) | string, region: *defaultRegion | #Region}
		}
	}

	organizations: { [Name=string]: #OrganizationWithDefaults & { _name: Name } }
}

#Cluster: {
	isPrimary:      *false | bool
	region:         #Region
	systemNodeSize: *"Standard_DS2_v2" | string

	userNodePools: *defaultNodePools | {[string]: #NodePool}

	let defaultNodePools = {[string]: #NodePool} & {
		cpunp: vmSize: "Standard_DS12_v2"
		gpunp: vmSize: "Standard_NC6s_v3"
	}
}

#NodePool: {
	vmSize:   string
	minCount: *0 | >0
	maxCount: *10 | >=minCount
}

#Organization: {
	namespace?:     string
	resourceGroup?: string
	subdomain:     string
	authority:     string
	audience:      *"api://tyger-server" | string
	storage: {
		buffers: [#StorageAccount, ...#StorageAccount]
		storageServer: #StorageAccount
		logs: #StorageAccount
	}
}
