// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/google/uuid"
	"github.com/microsoft/tyger/cli/internal/common"
	"github.com/rs/zerolog/log"
)

var (
	ResourceNameRegex       = regexp.MustCompile(`^[a-z][a-z\-0-9]{2,23}$`)
	StorageAccountNameRegex = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
	SubdomainRegex          = regexp.MustCompile(`^[a-zA-Z]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?$`)
	DatabaseServerNameRegex = regexp.MustCompile(`^([a-z0-9](?:[a-z0-9\-]{1,61}[a-z0-9])?)?$`)
)

func (inst *Installer) QuickValidateConfig() bool {
	success := true

	if inst.Config.EnvironmentName == "" {
		validationError(&success, "The `environmentName` field is required")
	} else if !ResourceNameRegex.MatchString(inst.Config.EnvironmentName) {
		validationError(&success, "The `environmentName` field must match the pattern %s", ResourceNameRegex)
	}

	inst.quickValidateCloudConfig(&success)
	inst.quickValidateApiConfig(&success)

	return success
}

func (inst *Installer) quickValidateCloudConfig(success *bool) {
	cloudConfig := inst.Config.Cloud
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
		cloudConfig.ResourceGroup = inst.Config.EnvironmentName
	} else if !ResourceNameRegex.MatchString(cloudConfig.ResourceGroup) {
		validationError(success, "The `cloud.resourceGroup` field must match the pattern %s", ResourceNameRegex)
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
			validationError(success, "The cluster `name` field must match the pattern %s", ResourceNameRegex)
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

		if cluster.Sku == "" {
			cluster.Sku = armcontainerservice.ManagedClusterSKUTierStandard
		} else {
			possibleValues := armcontainerservice.PossibleManagedClusterSKUTierValues()
			if !slices.Contains(possibleValues, cluster.Sku) {
				formattedPossibleValues := make([]string, len(possibleValues))
				for i, v := range possibleValues {
					formattedPossibleValues[i] = fmt.Sprintf("`%s`", v)
				}
				validationError(success, "The `sku` field of the cluster `%s` must be one of [%s]", cluster.Name, strings.Join(formattedPossibleValues, ", "))
			}
		}

		if cluster.SystemNodePool == nil {
			validationError(success, "The `systemNodePool` field is required on a cluster `%s`", cluster.Name)
		} else {
			quickValidateNodePoolConfig(success, cluster.SystemNodePool, 1)
		}

		if len(cluster.UserNodePools) == 0 {
			validationError(success, "At least one user node pool must be specified")
		}
		for _, np := range cluster.UserNodePools {
			quickValidateNodePoolConfig(success, np, 0)
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
		case PrincipalKindUser:
			if p.UserPrincipalName == "" {
				validationError(success, "The `userPrincipalName` field is required on a user principal")
			}
		case PrincipalKindGroup, PrincipalKindServicePrincipal:
		case "":
			validationError(success, "The `kind` field is required on a management principal")
		default:
			validationError(success, "The `kind` field must be one of %v", []PrincipalKind{PrincipalKindUser, PrincipalKindGroup, PrincipalKindServicePrincipal})
		}

		if p.ObjectId == "" {
			validationError(success, "The `objectId` field is required on a management principal")
		} else if _, err := uuid.Parse(p.ObjectId); err != nil {
			validationError(success, "The `objectId` field must be a GUID")
		}

		if p.Id != "" {
			validationError(success, "The `id` field is no longer supported a management principal. Use `objectId` instead")
		}
	}

	if computeConfig.Identities != nil {
		for _, id := range computeConfig.Identities {
			if id == "" {
				validationError(success, "The `identities` field must not contain empty strings")
			}
			if isSystemManagedIdentityName(id) {
				validationError(success, "The `identities` field must not contain the reserved name '%s'", id)
			}
		}
	}
}

func quickValidateNodePoolConfig(success *bool, np *NodePoolConfig, minNodeCount int) {
	if np.Name == "" {
		validationError(success, "The `name` field is required on a node pool")
	} else if !ResourceNameRegex.MatchString(np.Name) {
		validationError(success, "The node pool `name` field must match the pattern %s", ResourceNameRegex)
	}

	if np.VMSize == "" {
		validationError(success, "The `vmSize` field is required on a node pool")
	}

	if np.MinCount < int32(minNodeCount) {
		validationError(success, "The `minCount` field must be greater than or equal to %d", minNodeCount)
	}

	if np.MaxCount < 0 {
		validationError(success, "The `maxCount` field must be greater than or equal to %d", minNodeCount)
	}

	if np.MinCount > np.MaxCount {
		validationError(success, "The `minCount` field must be less than or equal to the `maxCount` field")
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
		validationError(success, "The `cloud.database.serverName` field must match the pattern %s", DatabaseServerNameRegex)
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

	if databaseConfig.StorageSizeGB == 0 {
		databaseConfig.StorageSizeGB = DefaultInitialDatabaseSizeGb
	} else if databaseConfig.StorageSizeGB < 0 {
		validationError(success, "The `cloud.database.initialDatabaseSizeGb` field must be greater than or equal to zero")
	}

	if databaseConfig.BackupRetentionDays == 0 {
		databaseConfig.BackupRetentionDays = DefaultBackupRetentionDays
	} else if databaseConfig.BackupRetentionDays < 0 {
		validationError(success, "The `cloud.database.backupRetentionDays` field must be greater than or equal to zero")
	}

	for i, fr := range databaseConfig.FirewallRules {
		if fr.Name == "" {
			validationError(success, "The `cloud.database.firewallRules[%d].name` field is required", i)
		} else {
			if ip := net.ParseIP(fr.StartIpAddress); ip == nil {
				validationError(success, "The `Firewall rule '%s' must have a valid IP address as `startIpAddress`", fr.Name)
			}

			if ip := net.ParseIP(fr.EndIpAddress); ip == nil {
				validationError(success, "The `Firewall rule '%s' must have a valid IP address as `endIpAddress`", fr.Name)
			}
		}
	}
}

func quickValidateStorageAccountConfig(success *bool, cloudConfig *CloudConfig, path string, storageConfig *StorageAccountConfig) {
	if storageConfig.Name == "" {
		validationError(success, "The `%s.name` field is required", path)
	} else if !StorageAccountNameRegex.MatchString(storageConfig.Name) {
		validationError(success, "The `%s.name` field must match the pattern %s", path, StorageAccountNameRegex)
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

func (inst *Installer) quickValidateApiConfig(success *bool) {
	apiConfig := inst.Config.Api
	if apiConfig == nil {
		validationError(success, "The `api` field is required")
		return
	}

	if inst.Config.Cloud != nil && inst.Config.Cloud.Compute != nil {
		apiHostCluster := inst.Config.Cloud.Compute.GetApiHostCluster()
		if apiHostCluster.Location != "" {
			apiHostLocation := inst.Config.Cloud.Compute.GetApiHostCluster().Location
			domainNameRegex := GetDomainNameRegex(apiHostLocation)
			if !domainNameRegex.MatchString(apiConfig.DomainName) {
				validationError(success, "The `api.domainName` field must match the pattern %s", domainNameRegex)
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

	if apiConfig.Buffers == nil {
		validationError(success, "The `api.buffers` field is required")
	} else {
		buffersConfig := apiConfig.Buffers
		if buffersConfig.ActiveLifetime == "" {
			buffersConfig.ActiveLifetime = "0.00:00"
		}

		if buffersConfig.SoftDeletedLifetime == "" {
			buffersConfig.SoftDeletedLifetime = "0.00:00"
		}

		if _, err := common.ParseTimeToLive(buffersConfig.ActiveLifetime); err != nil {
			validationError(success, "The `api.buffers.activeLifetime` field must be a valid TTL (D.HH:MM:SS)")
		}

		if _, err := common.ParseTimeToLive(buffersConfig.SoftDeletedLifetime); err != nil {
			validationError(success, "The `api.buffers.softDeletedLifetime` field be a valid TTL (D.HH:MM:SS)")
		}
	}
}

func validationError(success *bool, format string, args ...any) {
	*success = false
	log.Error().Msgf(format, args...)
}
