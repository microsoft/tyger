package tyger

// specify dependencies that we use in our dev environments
_devDependencies: #Dependencies & {
	let resourceName = "eminence"
	subscription: "BiomedicalImaging-NonProd"
	dnsZone: {
		name:          "eminence.ms"
		resourceGroup: resourceName
	}
	containerRegistry: resourceName
	keyVault:          #KeyVault & {
		name:               resourceName
		resourceGroup:      resourceName
		tlsCertificateName: "eminence-tls-cert"
	}
}

// further restrict or set default choices for environment values
#Environment: {
	defaultRegion: *"westus2" | string
	dependencies:  _devDependencies
	name:          string
	isEphemeral:   true
	let environmentName = name

	#OrganizationWithDefaults: _ // Already defined but redeclared here so that we can reference it within this scope
	organizations:             *close({
		lamna: {
			_name:     string
			authority: "https://login.microsoftonline.com/76d3279b-830e-4bea-baf8-12863cdeba4c/"
			subdomain: "\(environmentName)-\(_name)"
		}
	}) | _
}

// injected from the cli using -t environment=xyz
_environmentName: *"" | string @tag(environment)

_envs: {
	tygereastus: #Environment & {
		defaultRegion: "eastus"
	}

	// environments created with defaults
	"\(_environmentName)": #Environment & {
		name: _environmentName
	}
}

if _environmentName != "" {_envs["\(_environmentName)"]}
