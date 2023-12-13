package install

import (
	"fmt"
	"net/url"
	"regexp"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/rs/zerolog/log"
)

var (
	ResourceNameRegex       = regexp.MustCompile(`^[a-z][a-z\-0-9]{1,23}$`)
	StorageAccountNameRegex = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
	SubdomainRegex          = regexp.MustCompile(`^[a-zA-Z]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?$`)
	DatabaseServerNameRegex = regexp.MustCompile(`^([a-z0-9](?:[a-z0-9\-]{1,61}[a-z0-9])?)?$`)
)

func QuickValidateEnvironmentConfig(config *EnvironmentConfig) bool {
	success := true

	if config.EnvironmentName == "" {
		validationError(&success, "The `environmentName` field is required")
	} else if !ResourceNameRegex.MatchString(config.EnvironmentName) {
		validationError(&success, "The `environmentName` field must match the pattern "+ResourceNameRegex.String())
	}

	quickValidateCloudConfig(&success, config)
	quickValidateApiConfig(&success, config)

	return success
}

func quickValidateCloudConfig(success *bool, environmentConfig *EnvironmentConfig) {
	cloudConfig := environmentConfig.Cloud
	if cloudConfig == nil {
		validationError(success, "The `cloud` field is required")
		return
	}

	if cloudConfig.SubscriptionID == "" {
		validationError(success, "The `cloud.subscriptionId` field is required")
	}

	if cloudConfig.DefaultLocation == "" {
		validationError(success, "The `cloud.defaultLocation` field is required")
	}

	if cloudConfig.ResourceGroup == "" {
		cloudConfig.ResourceGroup = environmentConfig.EnvironmentName
	} else if !ResourceNameRegex.MatchString(cloudConfig.ResourceGroup) {
		validationError(success, "The `cloud.resourceGroup` field must match the pattern "+ResourceNameRegex.String())
	}

	quickValidateComputeConfig(success, cloudConfig)
	quickValidateStorageConfig(success, cloudConfig)
	quickValidateDatabaseConfig(success, cloudConfig)
}

func quickValidateComputeConfig(success *bool, cloudConfig *CloudConfig) {
	computeConfig := cloudConfig.Compute
	if computeConfig == nil {
		validationError(success, "The `cloud.compute` field is required")
		return
	}

	if len(computeConfig.Clusters) == 0 {
		validationError(success, "At least one cluster must be specified")
	}

	hasApiHost := false
	clusterNames := make(map[string]any)
	for _, cluster := range computeConfig.Clusters {
		if cluster.Name == "" {
			validationError(success, "The `name` field is required on a cluster")
		} else if !ResourceNameRegex.MatchString(cluster.Name) {
			validationError(success, "The cluster `name` field must match the pattern "+ResourceNameRegex.String())
		} else {
			if _, ok := clusterNames[cluster.Name]; ok {
				validationError(success, "Cluster names must be unique")
			}
			clusterNames[cluster.Name] = nil
		}

		if cluster.Location == "" {
			cluster.Location = cloudConfig.DefaultLocation
		}

		if cluster.KubernetesVersion == "" {
			cluster.KubernetesVersion = DefaultKubernetesVersion
		}

		if len(cluster.UserNodePools) == 0 {
			validationError(success, "At least one user node pool must be specified")
		}
		for _, np := range cluster.UserNodePools {
			if np.Name == "" {
				validationError(success, "The `name` field is required on a node pool")
			} else if !ResourceNameRegex.MatchString(np.Name) {
				validationError(success, "The node pool `name` field must match the pattern "+ResourceNameRegex.String())
			}

			if np.VMSize == "" {
				validationError(success, "The `vmSize` field is required on a node pool")
			}

			if np.MinCount < 0 {
				validationError(success, "The `minCount` field must be greater than or equal to zero")
			}

			if np.MaxCount < 0 {
				validationError(success, "The `maxCount` field must be greater than or equal to zero")
			}

			if np.MinCount > np.MaxCount {
				validationError(success, "The `minCount` field must be less than or equal to the `maxCount` field")
			}
		}

		if cluster.ApiHost {
			if hasApiHost {
				validationError(success, "Only one cluster can be the API host")
			}
			hasApiHost = true
		}
	}

	if !hasApiHost {
		validationError(success, "One cluster must have `apiHost` set to true")
	}

	if len(computeConfig.ManagementPrincipals) == 0 {
		validationError(success, "At least one management principal is required")
	}

	for _, p := range computeConfig.ManagementPrincipals {
		switch p.Kind {
		case PrincipalKindUser, PrincipalKindGroup, PrincipalKindServicePrincipal:
		case "":
			validationError(success, "The `kind` field is required on a management principal")
		default:
			validationError(success, "The `kind` field must be one of %v", []PrincipalKind{PrincipalKindUser, PrincipalKindGroup, PrincipalKindServicePrincipal})
		}
	}
}

func quickValidateStorageConfig(success *bool, cloudConfig *CloudConfig) {
	storageConfig := cloudConfig.Storage
	if storageConfig == nil {
		validationError(success, "The `cloud.storage` field is required")
		return
	}

	if storageConfig.Logs == nil {
		validationError(success, "The `cloud.storage.logs` field is required")
	} else {
		quickValidateStorageAccountConfig(success, cloudConfig, "cloud.storage.logs", storageConfig.Logs)
	}

	if len(storageConfig.Buffers) == 0 {
		validationError(success, "At least one `cloud.storage.buffers` account must be specified")
	}
	for i, buf := range storageConfig.Buffers {
		quickValidateStorageAccountConfig(success, cloudConfig, fmt.Sprintf("cloud.storage.buffers[%d]", i), buf)
	}
}

func quickValidateDatabaseConfig(success *bool, cloudConfig *CloudConfig) {
	databaseConfig := cloudConfig.DatabaseConfig
	if databaseConfig == nil {
		validationError(success, "The `cloud.database` field is required")
		return
	}

	if !DatabaseServerNameRegex.MatchString(databaseConfig.ServerName) {
		validationError(success, "The `cloud.database.serverName` field must match the pattern "+DatabaseServerNameRegex.String())
	}

	if databaseConfig.Location == "" {
		databaseConfig.Location = cloudConfig.DefaultLocation
	}

	if databaseConfig.ComputeTier == "" {
		databaseConfig.ComputeTier = string(DefaultDatabaseComputeTier)
	} else {
		match := false
		for _, at := range armpostgresqlflexibleservers.PossibleSKUTierValues() {
			if databaseConfig.ComputeTier == string(at) {
				match = true
				break
			}
		}
		if !match {
			validationError(success, "The `cloud.database.computeTier` field must be one of %v", armpostgresqlflexibleservers.PossibleSKUTierValues())
		}
	}

	if databaseConfig.VMSize == "" {
		databaseConfig.VMSize = DefaultDatabaseVMSize
	}

	if databaseConfig.PostgresMajorVersion == 0 {
		databaseConfig.PostgresMajorVersion = DefaultPostgresMajorVersion
	}

	if databaseConfig.InitialDatabaseSizeGb == 0 {
		databaseConfig.InitialDatabaseSizeGb = DefaultInitialDatabaseSizeGb
	} else if databaseConfig.InitialDatabaseSizeGb < 0 {
		validationError(success, "The `cloud.database.initialDatabaseSizeGb` field must be greater than or equal to zero")
	}

	if databaseConfig.BackupRetentionDays == 0 {
		databaseConfig.BackupRetentionDays = DefaultBackupRetentionDays
	} else if databaseConfig.BackupRetentionDays < 0 {
		validationError(success, "The `cloud.database.backupRetentionDays` field must be greater than or equal to zero")
	}
}

func quickValidateStorageAccountConfig(success *bool, cloudConfig *CloudConfig, path string, storageConfig *StorageAccountConfig) {
	if storageConfig.Name == "" {
		validationError(success, "The `%s.name` field is required", path)
	} else if !StorageAccountNameRegex.MatchString(storageConfig.Name) {
		validationError(success, "The `%s.name` field must match the pattern "+StorageAccountNameRegex.String())
	}

	if storageConfig.Location == "" {
		storageConfig.Location = cloudConfig.DefaultLocation
	}

	if storageConfig.Sku == "" {
		storageConfig.Sku = string(armstorage.SKUNameStandardLRS)
	} else {
		match := false
		for _, at := range armstorage.PossibleSKUNameValues() {
			if storageConfig.Sku == string(at) {
				match = true
				break
			}
		}
		if !match {
			validationError(success, "The `%s.sku` field must be one of %v", path, armstorage.PossibleSKUNameValues())
		}
	}
}

func GetDomainNameSuffix(location string) string {
	return fmt.Sprintf(".%s.cloudapp.azure.com", location)
}

func GetDomainNameRegex(location string) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`^[a-zA-Z]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?%s$`, regexp.QuoteMeta(GetDomainNameSuffix(location))))
}

func quickValidateApiConfig(success *bool, environmentConfig *EnvironmentConfig) {
	apiConfig := environmentConfig.Api
	if apiConfig == nil {
		validationError(success, "The `api` field is required")
		return
	}

	if environmentConfig.Cloud != nil && environmentConfig.Cloud.Compute != nil {
		apiHostCluster := environmentConfig.Cloud.Compute.GetApiHostCluster()
		if apiHostCluster.Location != "" {
			apiHostLocation := environmentConfig.Cloud.Compute.GetApiHostCluster().Location
			domainNameRegex := GetDomainNameRegex(apiHostLocation)
			if !domainNameRegex.MatchString(apiConfig.DomainName) {
				validationError(success, "The `api.domainName` field must match the pattern "+domainNameRegex.String())
			}
		}
	}

	if apiConfig.Auth == nil {
		validationError(success, "The `api.auth` field is required")
	} else {
		authConfig := apiConfig.Auth
		if authConfig.TenantID == "" {
			validationError(success, "The `api.auth.tenantId` field is required")
		}

		if authConfig.ApiAppUri == "" {
			validationError(success, "The `api.auth.apiAppUri` field is required")
		} else {
			if _, err := url.ParseRequestURI(authConfig.ApiAppUri); err != nil {
				validationError(success, "The `api.auth.apiAppUri` field must be a valid URI")
			}
		}

		if authConfig.CliAppUri == "" {
			validationError(success, "The `api.auth.cliAppUri` field is required")
		} else {
			if _, err := url.ParseRequestURI(authConfig.CliAppUri); err != nil {
				validationError(success, "The `api.auth.cliAppUri` field must be a valid URI")
			}
		}
	}
}

func validationError(success *bool, format string, args ...any) {
	*success = false
	log.Error().Msgf(format, args...)
}
