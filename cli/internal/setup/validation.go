package setup

import (
	"fmt"
	"net/url"
	"regexp"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/rs/zerolog/log"
)

var (
	resourceNameRegex       = regexp.MustCompile(`^[a-z][a-z\-0-9]*$`)
	storageAccountNameRegex = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
)

func quickValidateEnvironmentConfig(config *EnvironmentConfig) {
	log.Debug().Msg("Validating configuration")

	if config.EnvironmentName == "" {
		log.Error().Msg("The `environmentName` field is required")
	} else if !resourceNameRegex.MatchString(config.EnvironmentName) {
		log.Error().Msg("The `environmentName` field must match the pattern " + resourceNameRegex.String())
	}
	quickValidateCloudConfig(config)
	quickValidateApiConfig(config)
}

func quickValidateCloudConfig(environmentConfig *EnvironmentConfig) {
	cloudConfig := environmentConfig.Cloud
	if cloudConfig == nil {
		log.Error().Msg("The `cloud` field is required")
		return
	}

	if cloudConfig.SubscriptionID == "" {
		log.Error().Msg("The `cloud.subscriptionId` field is required")
	}

	if cloudConfig.DefaultLocation == "" {
		log.Error().Msg("The `cloud.defaultLocation` field is required")
	}

	if cloudConfig.ResourceGroup == "" {
		cloudConfig.ResourceGroup = environmentConfig.EnvironmentName
	} else if !resourceNameRegex.MatchString(cloudConfig.ResourceGroup) {
		log.Error().Msg("The `cloud.resourceGroup` field must match the pattern " + resourceNameRegex.String())
	}

	quickValidateComputeConfig(cloudConfig)
	quickValidateStorageConfig(cloudConfig)
}

func quickValidateComputeConfig(cloudConfig *CloudConfig) {
	computeConfig := cloudConfig.Compute
	if computeConfig == nil {
		log.Error().Msg("The `cloud.compute` field is required")
		return
	}

	if len(computeConfig.Clusters) == 0 {
		log.Error().Msg("At least one cluster must be specified")
	}

	hasApiHost := false
	clusterNames := make(map[string]any)
	for _, cluster := range computeConfig.Clusters {
		if cluster.Name == "" {
			log.Error().Msg("The `name` field is required on a cluster")
		} else if !resourceNameRegex.MatchString(cluster.Name) {
			log.Error().Msg("The cluster `name` field must match the pattern " + resourceNameRegex.String())
		} else {
			if _, ok := clusterNames[cluster.Name]; ok {
				log.Error().Msg("Cluster names must be unique")
			}
			clusterNames[cluster.Name] = nil
		}

		if cluster.Location == "" {
			cluster.Location = cloudConfig.DefaultLocation
		}

		if len(cluster.UserNodePools) == 0 {
			log.Error().Msg("At least one user node pool must be specified")
		}
		for _, np := range cluster.UserNodePools {
			if np.Name == "" {
				log.Error().Msg("The `name` field is required on a node pool")
			} else if !resourceNameRegex.MatchString(np.Name) {
				log.Error().Msg("The node pool `name` field must match the pattern " + resourceNameRegex.String())
			}

			if np.VMSize == "" {
				log.Error().Msg("The `vmSize` field is required on a node pool")
			}

			if np.MinCount < 0 {
				log.Error().Msg("The `minCount` field must be greater than or equal to zero")
			}

			if np.MaxCount < 0 {
				log.Error().Msg("The `maxCount` field must be greater than or equal to zero")
			}

			if np.MinCount > np.MaxCount {
				log.Error().Msg("The `minCount` field must be less than or equal to the `maxCount` field")
			}
		}

		if cluster.ApiHost {
			if hasApiHost {
				log.Error().Msg("Only one cluster can be the API host")
			}
			hasApiHost = true
		}
	}

	if !hasApiHost {
		log.Error().Msg("One cluster must have `apiHost` set to true")
	}
}

func quickValidateStorageConfig(cloudConfig *CloudConfig) {
	storageConfig := cloudConfig.Storage
	if storageConfig == nil {
		log.Error().Msg("The `cloud.storage` field is required")
		return
	}

	if storageConfig.Logs == nil {
		log.Error().Msg("The `cloud.storage.logs` field is required")
	} else {
		quickValidateStorageAccountConfig(cloudConfig, "cloud.storage.logs", storageConfig.Logs)
	}

	if len(storageConfig.Buffers) == 0 {
		log.Error().Msg("At least one `cloud.storage.buffers` account must be specified")
	}
	for i, buf := range storageConfig.Buffers {
		quickValidateStorageAccountConfig(cloudConfig, fmt.Sprintf("cloud.storage.buffers[%d]", i), buf)
	}
}

func quickValidateStorageAccountConfig(cloudConfig *CloudConfig, path string, storageConfig *StorageAccountConfig) {
	if storageConfig.Name == "" {
		log.Error().Msgf("The `%s.name` field is required", path)
	} else if !storageAccountNameRegex.MatchString(storageConfig.Name) {
		log.Error().Msg("The `%s.name` field must match the pattern " + storageAccountNameRegex.String())
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
			log.Error().Msgf("The `%s.sku` field must be one of %v", path, armstorage.PossibleSKUNameValues())
		}
	}
}

func quickValidateApiConfig(environmentConfig *EnvironmentConfig) {
	apiConfig := environmentConfig.Api
	if apiConfig == nil {
		log.Error().Msg("The `api` field is required")
		return
	}

	if environmentConfig.Cloud != nil && environmentConfig.Cloud.Compute != nil {
		apiHostCluster := environmentConfig.Cloud.Compute.GetApiHostCluster()
		if apiHostCluster.Location != "" {
			apiHostLocation := environmentConfig.Cloud.Compute.GetApiHostCluster().Location
			domainNameRegex := regexp.MustCompile(fmt.Sprintf(`^[a-zA-Z]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?\.%s.cloudapp.azure.com$`, apiHostLocation))
			if !domainNameRegex.MatchString(apiConfig.DomainName) {
				log.Error().Msgf("The `api.domainName` field must match the pattern " + domainNameRegex.String())
			}
		}
	}

	if apiConfig.Auth == nil {
		log.Error().Msg("The `api.auth` field is required")
	} else {
		authConfig := apiConfig.Auth
		if authConfig.TenantID == "" {
			log.Error().Msg("The `api.auth.tenantId` field is required")
		}

		if authConfig.ApiAppUri == "" {
			log.Error().Msg("The `api.auth.apiAppUri` field is required")
		} else {
			if _, err := url.ParseRequestURI(authConfig.ApiAppUri); err != nil {
				log.Error().Msg("The `api.auth.apiAppUri` field must be a valid URI")
			}
		}

		if authConfig.CliAppUri == "" {
			log.Error().Msg("The `api.auth.cliAppUri` field is required")
		} else {
			if _, err := url.ParseRequestURI(authConfig.CliAppUri); err != nil {
				log.Error().Msg("The `api.auth.cliAppUri` field must be a valid URI")
			}
		}
	}
}
