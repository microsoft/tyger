package tyger

developerConfig: #DeveloperConfig & {
	containerRegistry: "eminence"
}

config: #EnvironmentConfig & {
	environmentName: string @tag(environmentName)
	cloud: {
		tenantId:        "72f988bf-86f1-41af-91ab-2d7cd011db47"
		subscriptionId:  "87d8acb3-5176-4651-b457-6ab9cefd8e3d"
		defaultLocation: "westus2"
		compute: {
			clusters: [
				{
					name:    environmentName
					apiHost: true
					userNodePools: [
						{
							name:     "cpunp"
							vmSize:   "Standard_DS2_v2"
							maxCount: 10
						},
						{
							name:     "gpunp"
							vmSize:   "Standard_NC6s_v3"
							maxCount: 10
						},
					]
				},
			]
			managementPrincipalIds: [
				"5b60f594-a0eb-410c-a3fc-dd3c6f4e28d1",
				"c0e60aba-35f0-4778-bc9b-fc5d2af14687",
			]
			privateContainerRegistries: [developerConfig.containerRegistry]
		}
	}
	api: {
		auth: {
			tenantId:  "76d3279b-830e-4bea-baf8-12863cdeba4c"
			apiAppUri: "api://tyger-server"
			cliAppUri: "api://tyger-cli"
		}
	}
}
