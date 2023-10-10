package tyger

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
							vmSize:   "Standard_DS12_v2"
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
			logAnalyticsWorkspace: {
				resourceGroup: "eminence"
				name:          "eminence"
			}
			managementPrincipals: [
				{
					id: "5b60f594-a0eb-410c-a3fc-dd3c6f4e28d1"
					kind:     "ServicePrincipal"
				},
				{
					id: "c0e60aba-35f0-4778-bc9b-fc5d2af14687"
					kind:     "Group"
				},
			]
			privateContainerRegistries: [developerConfig.wipContainerRegistry.name]
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

developerConfig: #DeveloperConfig & {
	wipContainerRegistry: {
		name: "eminence"
	}
	officialContainerRegistry: {
		name: "tyger"
	}
	keyVault:   "eminence"
	testAppUri: "api://tyger-test-client"
	pemCertSecret: {
		name:    "tyger-test-client-cert"
		version: "1db664a6a3c74b6f817f3d842424003d"
	}
	pkcs12CertSecret: {
		name:    "tyger-test-client-cert-pkcs12"
		version: "f8b1b7dde7034217bf12ce4ea772b470"
	}
}
