// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
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
	"github.com/microsoft/tyger/cli/internal/install"
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

func (envConfig *CloudEnvironmentConfig) QuickValidateConfig(ctx context.Context) error {
	success := true

	if envConfig.EnvironmentName == "" {
		validationError(ctx, &success, "The `environmentName` field is required")
	} else if !ResourceNameRegex.MatchString(envConfig.EnvironmentName) {
		validationError(ctx, &success, "The `environmentName` field must match the pattern %s", ResourceNameRegex)
	}

	envConfig.quickValidateCloudConfig(ctx, &success)

	if len(envConfig.Organizations) > 0 {
		orgNames := make(map[string]any)
		hasCompatibilityMode := false
		hasBuiltInDomain := false
		for _, org := range envConfig.Organizations {
			if org.SingleOrganizationCompatibilityMode {
				if hasCompatibilityMode {
					validationError(ctx, &success, "Only one organization can have `singleOrganizationCompatibilityMode` set to true")
				}
				hasCompatibilityMode = true
			}

			if org.Api == nil {
				validationError(ctx, &success, "The `api` field is required for organization '%s'", org.Name)
				continue
			}

			if strings.HasSuffix(org.Api.DomainName, BuiltInDomainNameSuffix) {
				if hasBuiltInDomain {
					validationError(ctx, &success, "Only one organization can have a built-in domain name")
				} else {
					label := strings.Split(org.Api.DomainName, ".")[0]
					if envConfig.Cloud.Compute.DnsLabel != "" && envConfig.Cloud.Compute.DnsLabel != label {
						validationError(ctx, &success, "If `cloud.compute.dnsLabel` is specified, it must be set to '%s' based on the `api.domainName` field of the organization '%s'", strings.Split(org.Api.DomainName, ".")[0], org.Name)
					} else {
						envConfig.Cloud.Compute.DnsLabel = label
					}
				}
				hasBuiltInDomain = true
			}

			if _, ok := orgNames[org.Name]; ok {
				validationError(ctx, &success, "Organization names must be unique")
			}

			orgNames[org.Name] = nil

			if !hasBuiltInDomain && envConfig.Cloud.Compute.DnsLabel == "" {
				validationError(ctx, &success, "`cloud.compute.dnsLabel` must be set")
			}

			quickValidateOrganizationConfig(ctx, &success, envConfig, org)
		}
	}

	if success {
		return nil
	}

	return install.ErrAlreadyLoggedError
}

func (envConfig *CloudEnvironmentConfig) quickValidateCloudConfig(ctx context.Context, success *bool) {
	cloudConfig := envConfig.Cloud
	if cloudConfig == nil {
		validationError(ctx, success, "The `cloud` field is required")
		return
	}

	if cloudConfig.SubscriptionID == "" {
		validationError(ctx, success, "The `cloud.subscriptionId` field is required")
	}

	if cloudConfig.DefaultLocation == "" {
		validationError(ctx, success, "The `cloud.defaultLocation` field is required")
	}

	if cloudConfig.ResourceGroup == "" {
		cloudConfig.ResourceGroup = envConfig.EnvironmentName
	} else if !ResourceNameRegex.MatchString(cloudConfig.ResourceGroup) {
		validationError(ctx, success, "The `cloud.resourceGroup` field must match the pattern %s", ResourceNameRegex)
	}

	quickValidateDatabaseServerConfig(ctx, success, cloudConfig)
	quickValidateComputeConfig(ctx, success, cloudConfig)
	quickValidateTlsConfig(ctx, success, cloudConfig.TlsCertificate)
}

func quickValidateComputeConfig(ctx context.Context, success *bool, cloudConfig *CloudConfig) {
	computeConfig := cloudConfig.Compute
	if computeConfig == nil {
		validationError(ctx, success, "The `cloud.compute` field is required")
		return
	}

	if len(computeConfig.Clusters) == 0 {
		validationError(ctx, success, "At least one cluster must be specified")
	}

	hasApiHost := false
	clusterNames := make(map[string]any)
	for _, cluster := range computeConfig.Clusters {
		if cluster.Name == "" {
			validationError(ctx, success, "The `name` field is required on a cluster")
		} else if !ResourceNameRegex.MatchString(cluster.Name) {
			validationError(ctx, success, "The cluster `name` field must match the pattern %s", ResourceNameRegex)
		} else {
			if _, ok := clusterNames[cluster.Name]; ok {
				validationError(ctx, success, "Cluster names must be unique")
			}
			clusterNames[cluster.Name] = nil
		}

		if cluster.Location == "" {
			cluster.Location = cloudConfig.DefaultLocation
		}

		if cluster.KubernetesVersion == "" {
			cluster.KubernetesVersion = DefaultKubernetesVersion
		}

		cluster.Sku = validateOptionalEnumField(
			ctx,
			success,
			cluster.Sku,
			armcontainerservice.ManagedClusterSKUTierStandard,
			armcontainerservice.PossibleManagedClusterSKUTierValues(),
			fmt.Sprintf("The `sku` field must be one of %%v for cluster '%s'", cluster.Name))

		if cluster.SystemNodePool == nil {
			validationError(ctx, success, "The `systemNodePool` field is required on a cluster `%s`", cluster.Name)
		} else {
			quickValidateNodePoolConfig(ctx, success, cluster.SystemNodePool, 1)
		}

		if len(cluster.UserNodePools) == 0 {
			validationError(ctx, success, "At least one user node pool must be specified")
		}
		for _, np := range cluster.UserNodePools {
			quickValidateNodePoolConfig(ctx, success, np, 0)
		}

		if cluster.ApiHost {
			if hasApiHost {
				validationError(ctx, success, "Only one cluster can be the API host")
			}
			hasApiHost = true
		}
	}

	if !hasApiHost {
		validationError(ctx, success, "One cluster must have `apiHost` set to true")
	}

	if len(computeConfig.ManagementPrincipals) == 0 {
		validationError(ctx, success, "At least one management principal is required")
	}

	for _, p := range computeConfig.ManagementPrincipals {
		switch p.Kind {
		case PrincipalKindUser:
			if p.UserPrincipalName == "" {
				validationError(ctx, success, "The `userPrincipalName` field is required on a user principal")
			}
		case PrincipalKindGroup, PrincipalKindServicePrincipal:
		case "":
			validationError(ctx, success, "The `kind` field is required on a management principal")
		default:
			validationError(ctx, success, "The `kind` field must be one of %v", []PrincipalKind{PrincipalKindUser, PrincipalKindGroup, PrincipalKindServicePrincipal})
		}

		if p.ObjectId == "" {
			validationError(ctx, success, "The `objectId` field is required on a management principal")
		} else if _, err := uuid.Parse(p.ObjectId); err != nil {
			validationError(ctx, success, "The `objectId` field must be a GUID")
		}
	}
}

func quickValidateNodePoolConfig(ctx context.Context, success *bool, np *NodePoolConfig, minNodeCount int) {
	if np.Name == "" {
		validationError(ctx, success, "The `name` field is required on a node pool")
	} else if !ResourceNameRegex.MatchString(np.Name) {
		validationError(ctx, success, "The node pool `name` field must match the pattern %s", ResourceNameRegex)
	}

	if np.VMSize == "" {
		validationError(ctx, success, "The `vmSize` field is required on a node pool")
	}

	if np.MinCount < int32(minNodeCount) {
		validationError(ctx, success, "The `minCount` field must be greater than or equal to %d", minNodeCount)
	}

	if np.MaxCount < 0 {
		validationError(ctx, success, "The `maxCount` field must be greater than or equal to %d", minNodeCount)
	}

	if np.MinCount > np.MaxCount {
		validationError(ctx, success, "The `minCount` field must be less than or equal to the `maxCount` field")
	}

	np.OsSku = validateOptionalEnumField(
		ctx,
		success,
		np.OsSku,
		armcontainerservice.OSSKUAzureLinux,
		[]armcontainerservice.OSSKU{armcontainerservice.OSSKUAzureLinux, armcontainerservice.OSSKUUbuntu},
		fmt.Sprintf("The `osSku` field must be one of %%v for node pool '%s'", np.Name))

}

func quickValidateStorageConfig(ctx context.Context, success *bool, cloudConfig *CloudConfig, organizationConfig *OrganizationConfig) {
	storageConfig := organizationConfig.Cloud.Storage
	if storageConfig == nil {
		validationError(ctx, success, "The `cloud.storage` field is required for organization '%s'", organizationConfig.Name)
		return
	}

	if storageConfig.Logs == nil {
		validationError(ctx, success, "The `cloud.storage.logs` field is required for organization '%s'", organizationConfig.Name)
	} else {
		quickValidateStorageAccountConfig(ctx, success, cloudConfig, organizationConfig, "cloud.storage.logs", storageConfig.Logs)
	}

	if len(storageConfig.Buffers) == 0 {
		validationError(ctx, success, "At least one `cloud.storage.buffers` account must be specified for organization '%s'", organizationConfig.Name)
	}
	for i, buf := range storageConfig.Buffers {
		quickValidateStorageAccountConfig(ctx, success, cloudConfig, organizationConfig, fmt.Sprintf("cloud.storage.buffers[%d]", i), buf)
	}
}

func quickValidateTlsConfig(ctx context.Context, success *bool, cloudConfig *TlsCertificate) {
	if cloudConfig == nil {
		return
	}

	if cloudConfig.KeyVault == nil {
		validationError(ctx, success, "The if `cloud.tlsCertificate` is specified, `cloud.tlsCertificate.keyVault` must be specified")

	} else {
		if cloudConfig.KeyVault.ResourceGroup == "" {
			validationError(ctx, success, "The `cloud.tls.keyVault.resourceGroup` field is required")
		}

		if cloudConfig.KeyVault.Name == "" {
			validationError(ctx, success, "The `cloud.tls.keyVault.name` field is required")
		}
	}

	if cloudConfig.CertificateName == "" {
		validationError(ctx, success, "The `cloud.tls.certificateName` field is required")
	}
}

func quickValidateDatabaseServerConfig(ctx context.Context, success *bool, cloudConfig *CloudConfig) {
	databaseConfig := cloudConfig.Database
	if databaseConfig == nil {
		validationError(ctx, success, "The `cloud.database` field is required")
		return
	}

	if databaseConfig.ServerName == "" {
		validationError(ctx, success, "The `cloud.database.serverName` field is required")
	} else if !DatabaseServerNameRegex.MatchString(databaseConfig.ServerName) {
		validationError(ctx, success, "The `cloud.database.serverName` field must match the pattern %s", DatabaseServerNameRegex)
	}

	if databaseConfig.Location == "" {
		databaseConfig.Location = cloudConfig.DefaultLocation
	}

	databaseConfig.ComputeTier = validateOptionalEnumField(
		ctx,
		success,
		databaseConfig.ComputeTier,
		DefaultDatabaseComputeTier,
		armpostgresqlflexibleservers.PossibleSKUTierValues(),
		"The `cloud.database.computeTier` field must be one of %v")

	if databaseConfig.VMSize == "" {
		databaseConfig.VMSize = DefaultDatabaseVMSize
	}

	if databaseConfig.PostgresMajorVersion == nil {
		databaseConfig.PostgresMajorVersion = Ptr(DefaultPostgresMajorVersion)
	}

	if databaseConfig.StorageSizeGB == nil {
		databaseConfig.StorageSizeGB = Ptr(DefaultInitialDatabaseSizeGb)
	} else if *databaseConfig.StorageSizeGB < 0 {
		validationError(ctx, success, "The `cloud.database.initialDatabaseSizeGb` field must be greater than or equal to zero")
	}

	if databaseConfig.BackupRetentionDays == nil {
		databaseConfig.BackupRetentionDays = Ptr(DefaultBackupRetentionDays)
	} else if *databaseConfig.BackupRetentionDays < 0 {
		validationError(ctx, success, "The `cloud.database.backupRetentionDays` field must be greater than or equal to zero")
	}

	for i, fr := range databaseConfig.FirewallRules {
		if fr.Name == "" {
			validationError(ctx, success, "The `cloud.database.firewallRules[%d].name` field is required", i)
		} else {
			if ip := net.ParseIP(fr.StartIpAddress); ip == nil {
				validationError(ctx, success, "The `Firewall rule '%s' must have a valid IP address as `startIpAddress`", fr.Name)
			}

			if ip := net.ParseIP(fr.EndIpAddress); ip == nil {
				validationError(ctx, success, "The `Firewall rule '%s' must have a valid IP address as `endIpAddress`", fr.Name)
			}
		}
	}
}

func quickValidateStorageAccountConfig(ctx context.Context, success *bool, cloudConfig *CloudConfig, orgConfig *OrganizationConfig, path string, storageConfig *StorageAccountConfig) {
	if storageConfig.Name == "" {
		validationError(ctx, success, "The `%s.name` field is required for organization '%s'", path, orgConfig.Name)
	} else if !StorageAccountNameRegex.MatchString(storageConfig.Name) {
		validationError(ctx, success, "The `%s.name` field must match the pattern %s for organization '%s'", path, StorageAccountNameRegex, orgConfig.Name)
	}

	if storageConfig.Location == "" {
		storageConfig.Location = cloudConfig.DefaultLocation
	}

	storageConfig.Sku = validateOptionalEnumField(
		ctx,
		success,
		storageConfig.Sku,
		armstorage.SKUNameStandardLRS,
		armstorage.PossibleSKUNameValues(),
		fmt.Sprintf("The `%s.sku` field must be one of %%v for organization '%s'", path, orgConfig.Name))

	storageConfig.DnsEndpointType = validateOptionalEnumField(
		ctx,
		success,
		storageConfig.DnsEndpointType,
		armstorage.DNSEndpointTypeStandard,
		armstorage.PossibleDNSEndpointTypeValues(),
		fmt.Sprintf("The `%s.dnsEndpointType` field must be one of %%v for organization '%s'", path, orgConfig.Name))
}

func quickValidateOrganizationConfig(ctx context.Context, success *bool, config *CloudEnvironmentConfig, org *OrganizationConfig) {
	if org == nil {
		return
	}

	if org.Name == "" {
		validationError(ctx, success, "The `organization.name` field is required")
	} else if !SubdomainRegex.MatchString(org.Name) {
		validationError(ctx, success, "The `organization.name` field must match the pattern %s", SubdomainRegex)
	} else if slices.ContainsFunc(reservedOrganizationNames, func(name string) bool { return strings.EqualFold(name, org.Name) }) {
		validationError(ctx, success, "The `organization.name` field cannot be %s", org.Name)
	}

	if org.Cloud == nil {
		validationError(ctx, success, "The `organization.cloud` field is required")
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
				validationError(ctx, success, "The `identities` field must not contain empty strings for organization '%s'", org.Name)
			}
			if isSystemManagedIdentityName(id) {
				validationError(ctx, success, "The `identities` field must not contain the reserved name '%s' for organization '%s'", id, org.Name)
			}
		}
	}

	quickValidateStorageConfig(ctx, success, config.Cloud, org)
	quickValidateApiConfig(ctx, success, config, org)
}

func GetDomainNameSuffix(location string) string {
	return fmt.Sprintf(".%s%s", location, BuiltInDomainNameSuffix)
}

func GetDomainNameRegex(location string) *regexp.Regexp {
	return regexp.MustCompile(fmt.Sprintf(`^[a-zA-Z]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?%s$`, regexp.QuoteMeta(GetDomainNameSuffix(location))))
}

func quickValidateApiConfig(ctx context.Context, success *bool, config *CloudEnvironmentConfig, org *OrganizationConfig) {
	apiConfig := org.Api
	if apiConfig == nil {
		validationError(ctx, success, "The `api` field is required for organization '%s'", org.Name)
		return
	}

	if apiConfig.DomainName == "" {
		validationError(ctx, success, "The `api.domainName` field is required for organization '%s'", org.Name)
	} else if strings.HasSuffix(apiConfig.DomainName, BuiltInDomainNameSuffix) {
		apiHostCluster := config.Cloud.Compute.GetApiHostCluster()
		if apiHostCluster.Location != "" {
			apiHostLocation := config.Cloud.Compute.GetApiHostCluster().Location
			domainNameRegex := GetDomainNameRegex(apiHostLocation)
			if !domainNameRegex.MatchString(apiConfig.DomainName) {
				validationError(ctx, success, "The `api.domainName` field must match the pattern %s or use a custom domain for organization '%s'", domainNameRegex, org.Name)
			}
		}
	} else {
		if config.Cloud.DnsZone == nil || config.Cloud.DnsZone.Name == "" {
			validationError(ctx, success, "The `cloud.dnsZone.name` field is required for the custom domain name for organization '%s'", org.Name)
		} else {
			if !strings.HasSuffix(apiConfig.DomainName, "."+config.Cloud.DnsZone.Name) {
				validationError(ctx, success, "The `api.domainName` field must be a subdomain of DNS zone name '%s' defined in `cloud.dnsZone` for organization '%s'", config.Cloud.DnsZone.Name, org.Name)
			}
		}
	}

	if apiConfig.TlsCertificateProvider != TlsCertificateProviderKeyVault &&
		apiConfig.TlsCertificateProvider != TlsCertificateProviderLetsEncrypt {
		validationError(ctx, success, "The `api.tlsCertificateProvider` field must be one of %s for organization '%s'", []TlsCertificateProvider{TlsCertificateProviderKeyVault, TlsCertificateProviderLetsEncrypt}, org.Name)
	}

	if apiConfig.Auth == nil {
		validationError(ctx, success, "The `api.auth` field is required for organization '%s'", org.Name)
	} else {
		authConfig := apiConfig.Auth
		if authConfig.RbacEnabled == nil {
			validationError(ctx, success, "The `api.auth.rbacEnabled` field is required for organization '%s'", org.Name)
		}

		if authConfig.TenantID == "" {
			validationError(ctx, success, "The `api.auth.tenantId` field is required for organization '%s'", org.Name)
		}

		if authConfig.ApiAppUri == "" {
			validationError(ctx, success, "The `api.auth.apiAppUri` field is required for organization '%s'", org.Name)
		} else {
			if _, err := url.ParseRequestURI(authConfig.ApiAppUri); err != nil {
				validationError(ctx, success, "The `api.auth.apiAppUri` field must be a valid URL for organization '%s'", org.Name)
			}
		}

		if authConfig.CliAppUri == "" {
			validationError(ctx, success, "The `api.auth.cliAppUri` field is required for organization '%s'", org.Name)
		} else {
			if _, err := url.ParseRequestURI(authConfig.CliAppUri); err != nil {
				validationError(ctx, success, "The `api.auth.cliAppUri` field must be a valid URL for organization '%s'", org.Name)
			}
		}

		if authConfig.ApiAppId == "" {
			validationError(ctx, success, "The `api.auth.apiAppId` field is required for organization '%s'. Run `tyger auth apply` to retrieve the value.", org.Name)
		} else if _, err := uuid.Parse(authConfig.ApiAppId); err != nil {
			validationError(ctx, success, "The `api.auth.apiAppId` field must be a GUID for organization '%s'", org.Name)
		}

		if authConfig.CliAppId == "" {
			validationError(ctx, success, "The `api.auth.cliAppId` field is required for organization '%s'. Run `tyger auth apply` to retrieve the value.", org.Name)
		} else if _, err := uuid.Parse(authConfig.CliAppId); err != nil {
			validationError(ctx, success, "The `api.auth.cliAppId` field must be a GUID for organization '%s'", org.Name)
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
		validationError(ctx, success, "The `api.buffers.activeLifetime` field must be a valid TTL (D.HH:MM:SS) for organization '%s'", org.Name)
	}

	if _, err := common.ParseTimeToLive(buffersConfig.SoftDeletedLifetime); err != nil {
		validationError(ctx, success, "The `api.buffers.softDeletedLifetime` field must be a valid TTL (D.HH:MM:SS) for organization '%s'", org.Name)
	}

	if apiConfig.Helm == nil {
		apiConfig.Helm = &OrganizationHelmConfig{}
	}
}

func validationError(ctx context.Context, success *bool, format string, args ...any) {
	*success = false
	log.Ctx(ctx).Error().Msgf(format, args...)
}

func validateOptionalEnumField[TField ~string, TEnum ~string](ctx context.Context, success *bool, value TField, defaultValue TEnum, possibleValues []TEnum, errorTemplate string) TField {
	if value == "" {
		return TField(defaultValue)
	}

	for _, pv := range possibleValues {
		if value == TField(pv) {
			return value
		}
	}

	possibleValuesQuoted := make([]string, len(possibleValues))
	for i, pv := range possibleValues {
		possibleValuesQuoted[i] = fmt.Sprintf("`%s`", string(pv))
	}

	possibleValuesString := fmt.Sprintf("[%s]", strings.Join(possibleValuesQuoted, ", "))

	validationError(ctx, success, errorTemplate, possibleValuesString)
	return value
}
