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

const (
	BuiltInDomainNameSuffix = ".cloudapp.azure.com"
)

var (
	ResourceNameRegex         = regexp.MustCompile(`^[a-z][a-z\-0-9]{2,23}$`)
	StorageAccountNameRegex   = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
	SubdomainRegex            = regexp.MustCompile(`^[a-zA-Z]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?$`)
	DatabaseServerNameRegex   = regexp.MustCompile(`^[a-z0-9](?:[a-z0-9\-]{1,61}[a-z0-9])?$`)
	reservedOrganizationNames = []string{"postgres"}
)

func (inst *Installer) QuickValidateConfig() bool {
	success := true

	if inst.Config.EnvironmentName == "" {
		validationError(&success, "The `environmentName` field is required")
	} else if !ResourceNameRegex.MatchString(inst.Config.EnvironmentName) {
		validationError(&success, "The `environmentName` field must match the pattern %s", ResourceNameRegex)
	}

	inst.quickValidateCloudConfig(&success)

	if len(inst.Config.Organizations) > 0 {
		orgNames := make(map[string]any)
		hasCompatibilityMode := false
		hasBuiltInDomain := false
		for _, org := range inst.Config.Organizations {
			if org.SingleOrganizationCompatibilityMode {
				if hasCompatibilityMode {
					validationError(&success, "Only one organization can have `singleOrganizationCompatibilityMode` set to true")
				}
				hasCompatibilityMode = true
			}

			if strings.HasSuffix(org.Api.DomainName, BuiltInDomainNameSuffix) {
				if hasBuiltInDomain {
					validationError(&success, "Only one organization can have a built-in domain name")
				} else {
					label := strings.Split(org.Api.DomainName, ".")[0]
					if inst.Config.Cloud.Compute.DnsLabel != "" && inst.Config.Cloud.Compute.DnsLabel != label {
						validationError(&success, "If `cloud.compute.dnsLabel` is specified, it must be set to '%s' based on the `api.domainName` field of the organization '%s'", strings.Split(org.Api.DomainName, ".")[0], org.Name)
					} else {
						inst.Config.Cloud.Compute.DnsLabel = label
					}
				}
				hasBuiltInDomain = true
			}

			if _, ok := orgNames[org.Name]; ok {
				validationError(&success, "Organization names must be unique")
			}

			orgNames[org.Name] = nil

			if !hasBuiltInDomain && inst.Config.Cloud.Compute.DnsLabel == "" {
				validationError(&success, "`cloud.compute.dnsLabel` must be set")
			}

			quickValidateOrganizationConfig(&success, inst.Config, org)
		}
	}

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

	quickValidateDatabaseServerConfig(success, cloudConfig)
	quickValidateComputeConfig(success, cloudConfig)
	quickValidateTlsConfig(success, cloudConfig.TlsCertificate)
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

	if np.OsSku == "" {
		np.OsSku = string(armcontainerservice.OSSKUAzureLinux)
	} else {
		supportedValues := []string{string(armcontainerservice.OSSKUAzureLinux), string(armcontainerservice.OSSKUUbuntu)}
		if !slices.Contains(supportedValues, np.OsSku) {
			validationError(success, "The `osSku` field must be one of [%s]", strings.Join(supportedValues, ", "))
		}
	}
}

func quickValidateStorageConfig(success *bool, cloudConfig *CloudConfig, organizationConfig *OrganizationConfig) {
	storageConfig := organizationConfig.Cloud.Storage
	if storageConfig == nil {
		validationError(success, "The `cloud.storage` field is required for organization '%s'", organizationConfig.Name)
		return
	}

	if storageConfig.Logs == nil {
		validationError(success, "The `cloud.storage.logs` field is required for organization '%s'", organizationConfig.Name)
	} else {
		quickValidateStorageAccountConfig(success, cloudConfig, organizationConfig, "cloud.storage.logs", storageConfig.Logs)
	}

	if len(storageConfig.Buffers) == 0 {
		validationError(success, "At least one `cloud.storage.buffers` account must be specified for organization '%s'", organizationConfig.Name)
	}
	for i, buf := range storageConfig.Buffers {
		quickValidateStorageAccountConfig(success, cloudConfig, organizationConfig, fmt.Sprintf("cloud.storage.buffers[%d]", i), buf)
	}
}

func quickValidateTlsConfig(success *bool, cloudConfig *TlsCertificate) {
	if cloudConfig == nil {
		return
	}

	if cloudConfig.KeyVault == nil {
		validationError(success, "The if `cloud.tlsCertificate` is specified, `cloud.tlsCertificate.keyVault` must be specified")

	} else {
		if cloudConfig.KeyVault.ResourceGroup == "" {
			validationError(success, "The `cloud.tls.keyVault.resourceGroup` field is required")
		}

		if cloudConfig.KeyVault.Name == "" {
			validationError(success, "The `cloud.tls.keyVault.name` field is required")
		}
	}

	if cloudConfig.CertificateName == "" {
		validationError(success, "The `cloud.tls.certificateName` field is required")
	}
}

func quickValidateDatabaseServerConfig(success *bool, cloudConfig *CloudConfig) {
	databaseConfig := cloudConfig.Database
	if databaseConfig == nil {
		validationError(success, "The `cloud.database` field is required")
		return
	}

	if databaseConfig.ServerName == "" {
		validationError(success, "The `cloud.database.serverName` field is required")
	} else if !DatabaseServerNameRegex.MatchString(databaseConfig.ServerName) {
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

	if databaseConfig.PostgresMajorVersion == nil {
		databaseConfig.PostgresMajorVersion = Ptr(DefaultPostgresMajorVersion)
	}

	if databaseConfig.StorageSizeGB == nil {
		databaseConfig.StorageSizeGB = Ptr(DefaultInitialDatabaseSizeGb)
	} else if *databaseConfig.StorageSizeGB < 0 {
		validationError(success, "The `cloud.database.initialDatabaseSizeGb` field must be greater than or equal to zero")
	}

	if databaseConfig.BackupRetentionDays == nil {
		databaseConfig.BackupRetentionDays = Ptr(DefaultBackupRetentionDays)
	} else if *databaseConfig.BackupRetentionDays < 0 {
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

func quickValidateStorageAccountConfig(success *bool, cloudConfig *CloudConfig, orgConfig *OrganizationConfig, path string, storageConfig *StorageAccountConfig) {
	if storageConfig.Name == "" {
		validationError(success, "The `%s.name` field is required for organization '%s'", path, orgConfig.Name)
	} else if !StorageAccountNameRegex.MatchString(storageConfig.Name) {
		validationError(success, "The `%s.name` field must match the pattern %s for organization '%s'", path, StorageAccountNameRegex, orgConfig.Name)
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
			validationError(success, "The `%s.sku` field must be one of %v for organization '%s'", path, armstorage.PossibleSKUNameValues(), orgConfig.Name)
		}
	}
}

func quickValidateOrganizationConfig(success *bool, config *CloudEnvironmentConfig, org *OrganizationConfig) {
	if org == nil {
		return
	}

	if org.Name == "" {
		validationError(success, "The `organization.name` field is required")
	} else if !SubdomainRegex.MatchString(org.Name) {
		validationError(success, "The `organization.name` field must match the pattern %s", SubdomainRegex)
	} else if slices.ContainsFunc(reservedOrganizationNames, func(name string) bool { return strings.EqualFold(name, org.Name) }) {
		validationError(success, "The `organization.name` field cannot be %s", org.Name)
	}

	if org.Cloud == nil {
		validationError(success, "The `organization.cloud` field is required")
		return
	}

	cloudConfig := org.Cloud

	if org.SingleOrganizationCompatibilityMode {
		if org.Cloud.DatabaseName == "" {
			cloudConfig.DatabaseName = defaultDatabaseName
		}
		cloudConfig.ResourceGroup = config.Cloud.ResourceGroup
		cloudConfig.KubernetesNamespace = "tyger"
	} else {
		if cloudConfig.DatabaseName == "" {
			cloudConfig.DatabaseName = org.Name
		}
		cloudConfig.ResourceGroup = fmt.Sprintf("%s-%s", config.Cloud.ResourceGroup, org.Name)
		cloudConfig.KubernetesNamespace = org.Name
	}

	if cloudConfig.Identities != nil {
		for _, id := range cloudConfig.Identities {
			if id == "" {
				validationError(success, "The `identities` field must not contain empty strings for organization '%s'", org.Name)
			}
			if isSystemManagedIdentityName(id) {
				validationError(success, "The `identities` field must not contain the reserved name '%s' for organization '%s'", id, org.Name)
			}
		}
	}

	quickValidateStorageConfig(success, config.Cloud, org)
	quickValidateApiConfig(success, config, org)
}

func GetDomainNameSuffix(location string) string {
	return fmt.Sprintf(".%s%s", location, BuiltInDomainNameSuffix)
}

func GetDomainNameRegex(location string) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`^[a-zA-Z]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?%s$`, regexp.QuoteMeta(GetDomainNameSuffix(location))))
}

func quickValidateApiConfig(success *bool, config *CloudEnvironmentConfig, org *OrganizationConfig) {
	apiConfig := org.Api
	if apiConfig == nil {
		validationError(success, "The `api` field is required for organization '%s'", org.Name)
		return
	}

	if apiConfig.DomainName == "" {
		validationError(success, "The `api.domainName` field is required for organization '%s'", org.Name)
	} else if strings.HasSuffix(apiConfig.DomainName, BuiltInDomainNameSuffix) {
		apiHostCluster := config.Cloud.Compute.GetApiHostCluster()
		if apiHostCluster.Location != "" {
			apiHostLocation := config.Cloud.Compute.GetApiHostCluster().Location
			domainNameRegex := GetDomainNameRegex(apiHostLocation)
			if !domainNameRegex.MatchString(apiConfig.DomainName) {
				validationError(success, "The `api.domainName` field must match the pattern %s or use a custom domain for organization '%s'", domainNameRegex, org.Name)
			}
		}
	}

	if apiConfig.TlsCertificateProvider != TlsCertificateProviderKeyVault &&
		apiConfig.TlsCertificateProvider != TlsCertificateProviderLetsEncrypt {
		validationError(success, "The `api.tlsCertificateProvider` field must be one of %s for organization '%s'", []TlsCertificateProvider{TlsCertificateProviderKeyVault, TlsCertificateProviderLetsEncrypt}, org.Name)
	}

	if apiConfig.Auth == nil {
		validationError(success, "The `api.auth` field is required for organization '%s'", org.Name)
	} else {
		authConfig := apiConfig.Auth
		if authConfig.TenantID == "" {
			validationError(success, "The `api.auth.tenantId` field is required for organization '%s'", org.Name)
		}

		if authConfig.ApiAppUri == "" {
			validationError(success, "The `api.auth.apiAppUri` field is required for organization '%s'", org.Name)
		} else {
			if _, err := url.ParseRequestURI(authConfig.ApiAppUri); err != nil {
				validationError(success, "The `api.auth.apiAppUri` field must be a valid URI for organization '%s'", org.Name)
			}
		}

		if authConfig.CliAppUri == "" {
			validationError(success, "The `api.auth.cliAppUri` field is required for organization '%s'", org.Name)
		} else {
			if _, err := url.ParseRequestURI(authConfig.CliAppUri); err != nil {
				validationError(success, "The `api.auth.cliAppUri` field must be a valid URI for organization '%s'", org.Name)
			}
		}
	}

	if apiConfig.Buffers == nil {
		apiConfig.Buffers = &BuffersConfig{}
	}
	buffersConfig := apiConfig.Buffers
	if buffersConfig.ActiveLifetime == "" {
		buffersConfig.ActiveLifetime = "0.00:00"
	}

	if buffersConfig.SoftDeletedLifetime == "" {
		buffersConfig.SoftDeletedLifetime = "1.00:00"
	}

	if _, err := common.ParseTimeToLive(buffersConfig.ActiveLifetime); err != nil {
		validationError(success, "The `api.buffers.activeLifetime` field must be a valid TTL (D.HH:MM:SS) for organization '%s'", org.Name)
	}

	if _, err := common.ParseTimeToLive(buffersConfig.SoftDeletedLifetime); err != nil {
		validationError(success, "The `api.buffers.softDeletedLifetime` field must be a valid TTL (D.HH:MM:SS) for organization '%s'", org.Name)
	}

	if apiConfig.Helm == nil {
		apiConfig.Helm = &OrganizationHelmConfig{}
	}
}

func validationError(success *bool, format string, args ...any) {
	*success = false
	log.Error().Msgf(format, args...)
}
