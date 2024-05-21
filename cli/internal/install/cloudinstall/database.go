// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/operationalinsights/armoperationalinsights"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/docker/docker/pkg/namesgenerator"
	"github.com/golang-jwt/jwt/v5"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
)

const (
	DefaultDatabaseComputeTier   = armpostgresqlflexibleservers.SKUTierBurstable
	DefaultDatabaseVMSize        = "Standard_B1ms"
	DefaultPostgresMajorVersion  = 16
	DefaultInitialDatabaseSizeGb = 32
	DefaultBackupRetentionDays   = 7
)

const (
	ownersRole           = "tyger-owners"
	databasePort         = 5432
	databaseName         = "postgres"
	dbConfiguredTagValue = "1" // bump this when we change the database configuration logic
)

func (inst *Installer) createDatabase(ctx context.Context, tygerServerManagedIdentityPromise, migrationRunnerManagedIdentityPromise *install.Promise[*armmsi.Identity]) (any, error) {
	databaseConfig := inst.Config.Cloud.DatabaseConfig

	tygerServerManagedIdentity, err := tygerServerManagedIdentityPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	serverName, err := inst.getDatabaseServerName(ctx, tygerServerManagedIdentity, true)
	if err != nil {
		return nil, err
	}

	client, err := armpostgresqlflexibleservers.NewServersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL server client: %w", err)
	}

	var tags map[string]*string
	var existingServer *armpostgresqlflexibleservers.Server

	if existingServerResponse, err := client.Get(ctx, inst.Config.Cloud.ResourceGroup, serverName, nil); err == nil {
		existingServer = &existingServerResponse.Server
		if existingTag, ok := existingServerResponse.Tags[TagKey]; ok {
			if *existingTag != inst.Config.EnvironmentName {
				return nil, fmt.Errorf("database server '%s' is already in use by environment '%s'", *existingServerResponse.Name, *existingTag)
			}
			tags = existingServerResponse.Tags
		}
	} else {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
		} else {
			return nil, fmt.Errorf("failed to get database server: %w", err)
		}
	}

	if tags == nil {
		tags = make(map[string]*string)
	}
	tags[TagKey] = &inst.Config.EnvironmentName

	geoRedundantBackup := armpostgresqlflexibleservers.GeoRedundantBackupEnumDisabled
	if databaseConfig.BackupGeoRedundancy {
		geoRedundantBackup = armpostgresqlflexibleservers.GeoRedundantBackupEnumEnabled
	}

	serverParameters := armpostgresqlflexibleservers.Server{
		Tags:     tags,
		Location: &databaseConfig.Location,
		SKU: &armpostgresqlflexibleservers.SKU{
			Name: &databaseConfig.VMSize,
			Tier: Ptr(armpostgresqlflexibleservers.SKUTier(databaseConfig.ComputeTier)),
		},
		Properties: &armpostgresqlflexibleservers.ServerProperties{
			AuthConfig: &armpostgresqlflexibleservers.AuthConfig{
				ActiveDirectoryAuth: Ptr(armpostgresqlflexibleservers.ActiveDirectoryAuthEnumEnabled),
				PasswordAuth:        Ptr(armpostgresqlflexibleservers.PasswordAuthEnumDisabled),
			},
			Version: Ptr(armpostgresqlflexibleservers.ServerVersion(strconv.Itoa(databaseConfig.PostgresMajorVersion))),
			Storage: &armpostgresqlflexibleservers.Storage{
				AutoGrow:      Ptr(armpostgresqlflexibleservers.StorageAutoGrowEnabled),
				StorageSizeGB: Ptr(int32(databaseConfig.StorageSizeGB)),
			},
			Network: &armpostgresqlflexibleservers.Network{
				PublicNetworkAccess: Ptr(armpostgresqlflexibleservers.ServerPublicNetworkAccessStateEnabled),
			},
			Backup: &armpostgresqlflexibleservers.Backup{
				BackupRetentionDays: Ptr(int32(databaseConfig.BackupRetentionDays)),
				GeoRedundantBackup:  &geoRedundantBackup,
			},
			HighAvailability: &armpostgresqlflexibleservers.HighAvailability{
				Mode: Ptr(armpostgresqlflexibleservers.HighAvailabilityModeDisabled),
			},
			CreateMode: Ptr(armpostgresqlflexibleservers.CreateModeCreate),
		},
	}

	serverNeedsUpdate := existingServer == nil
	if !serverNeedsUpdate {
		serverParameters, serverNeedsUpdate = databaseServerNeedsUpdate(serverParameters, *existingServer)
	}

	if serverNeedsUpdate {
		log.Info().Msg("Creating or updating PostgreSQL server")
		poller, err := client.BeginCreate(ctx, inst.Config.Cloud.ResourceGroup, serverName, serverParameters, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create PostgreSQL server: %w", err)
		}

		createdDatabaseServer, err := poller.PollUntilDone(ctx, nil)
		existingServer = &createdDatabaseServer.Server
		if err != nil {
			return nil, fmt.Errorf("failed to create PostgreSQL server: %w", err)
		}
	} else {
		log.Info().Msgf("PostgreSQL server '%s' appears to be up to date", *existingServer.Name)
	}

	if err := assignRbacRole(ctx, inst.Config.Cloud.Compute.GetManagementPrincipalIds(), true, *existingServer.ID, "Reader", inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
		return nil, fmt.Errorf("failed to assign RBAC role on database: %w", err)
	}

	if inst.Config.Cloud.LogAnalyticsWorkspace != nil {
		if err := inst.enableDiagnosticSettings(ctx, existingServer); err != nil {
			return nil, err
		}
	}

	desiredFirewallRules := map[string]armpostgresqlflexibleservers.FirewallRule{
		"AllowAllAzureServicesAndResources": {
			Properties: &armpostgresqlflexibleservers.FirewallRuleProperties{
				StartIPAddress: Ptr("0.0.0.0"),
				EndIPAddress:   Ptr("0.0.0.0"),
			},
		},
	}

	for _, rule := range databaseConfig.FirewallRules {
		desiredFirewallRules[rule.Name] = armpostgresqlflexibleservers.FirewallRule{
			Properties: &armpostgresqlflexibleservers.FirewallRuleProperties{
				StartIPAddress: Ptr(rule.StartIpAddress),
				EndIPAddress:   Ptr(rule.EndIpAddress),
			},
		}
	}

	firewallClient, err := armpostgresqlflexibleservers.NewFirewallRulesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL server firewall client: %w", err)
	}

	existingFirewallRules := make(map[string]armpostgresqlflexibleservers.FirewallRule)

	pager := firewallClient.NewListByServerPager(inst.Config.Cloud.ResourceGroup, serverName, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list PostgreSQL server firewall rules: %w", err)
		}
		for _, fr := range page.Value {
			existingFirewallRules[*fr.Name] = *fr
		}
	}

	promiseGroup := &install.PromiseGroup{}

	for name := range desiredFirewallRules {
		nameSnapshot := name
		desiredRule := desiredFirewallRules[nameSnapshot]
		if existingRule, ok := existingFirewallRules[nameSnapshot]; ok &&
			*existingRule.Properties.StartIPAddress == *desiredRule.Properties.StartIPAddress &&
			*existingRule.Properties.EndIPAddress == *desiredRule.Properties.EndIPAddress {
			continue
		}

		install.NewPromise(ctx, promiseGroup, func(ctx context.Context) (any, error) {
			log.Info().Msgf("Creating or updating PostgreSQL server firewall rule '%s'", nameSnapshot)
			_, err = retryableAsyncOperation(ctx, func(ctx context.Context) (*runtime.Poller[armpostgresqlflexibleservers.FirewallRulesClientCreateOrUpdateResponse], error) {
				return firewallClient.BeginCreateOrUpdate(ctx, inst.Config.Cloud.ResourceGroup, serverName, nameSnapshot, desiredRule, nil)
			})
			if err != nil {
				return nil, fmt.Errorf("failed to create PostgreSQL server firewall rule: %w", err)
			}
			return nil, nil
		})
	}

	for name := range existingFirewallRules {
		nameSnapshot := name
		if _, ok := desiredFirewallRules[nameSnapshot]; !ok {
			install.NewPromise(ctx, promiseGroup, func(ctx context.Context) (any, error) {
				log.Info().Msgf("Deleting PostgreSQL server firewall rule '%s'", nameSnapshot)
				_, err = retryableAsyncOperation(ctx, func(ctx context.Context) (*runtime.Poller[armpostgresqlflexibleservers.FirewallRulesClientDeleteResponse], error) {
					return firewallClient.BeginDelete(ctx, inst.Config.Cloud.ResourceGroup, serverName, nameSnapshot, nil)
				})
				if err != nil {
					return nil, fmt.Errorf("failed to delete PostgreSQL server firewall rule: %w", err)
				}
				return nil, nil
			})
		}
	}

	// wait for the tasks to complete
	for _, p := range *promiseGroup {
		if err := p.AwaitErr(); err != nil && err != install.ErrDependencyFailed {
			return nil, err
		}
	}

	// check if the database has already been configured and we can skip the steps that follow
	if value, ok := tags[inst.getDatabaseConfiguredTagKey()]; ok && value != nil && *value == dbConfiguredTagValue {
		log.Info().Msg("PostgreSQL server is already configured")
		return nil, nil
	}

	migrationRunnerManagedIdentity, err := migrationRunnerManagedIdentityPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	currentPrincipalDisplayName, err := createDatabaseAdmins(ctx, inst.Config, serverName, inst.Credential, migrationRunnerManagedIdentity)

	if err := inst.createRoles(ctx, existingServer, currentPrincipalDisplayName, tygerServerManagedIdentity, migrationRunnerManagedIdentity); err != nil {
		return nil, err
	}

	// add a tag on the server to indicate that it is configured, so we can skip the slow firewall configuration next time
	existingServer.Tags[inst.getDatabaseConfiguredTagKey()] = Ptr(dbConfiguredTagValue)

	tagsClient, err := armresources.NewTagsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create tags client: %w", err)
	}

	_, err = tagsClient.CreateOrUpdateAtScope(ctx, *existingServer.ID, armresources.TagsResource{Properties: &armresources.Tags{Tags: existingServer.Tags}}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to tag PostgreSQL server: %w", err)
	}

	return nil, nil
}

// Add the given managed identity and the current user as admins on the database server.
// These are not superusers.
func createDatabaseAdmins(
	ctx context.Context,
	config *CloudEnvironmentConfig,
	serverName string,
	cred azcore.TokenCredential,
	migrationRunnerManagedIdentity *armmsi.Identity,
) (currentPrincipalDisplayname string, err error) {
	adminClient, err := armpostgresqlflexibleservers.NewAdministratorsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create PostgreSQL server admin client: %w", err)
	}

	log.Info().Msgf("Creating PostgreSQL server admin '%s'", *migrationRunnerManagedIdentity.Name)

	migrationRunnerAdmin := armpostgresqlflexibleservers.ActiveDirectoryAdministratorAdd{
		Properties: &armpostgresqlflexibleservers.AdministratorPropertiesForAdd{
			PrincipalName: migrationRunnerManagedIdentity.Name,
			PrincipalType: Ptr(armpostgresqlflexibleservers.PrincipalTypeServicePrincipal),
			TenantID:      Ptr(config.Cloud.TenantID),
		},
	}
	migrationRunnerPoller, err := adminClient.BeginCreate(ctx, config.Cloud.ResourceGroup, serverName, *migrationRunnerManagedIdentity.Properties.PrincipalID, migrationRunnerAdmin, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create PostgreSQL server admin: %w", err)
	}

	currentPrincipalDisplayName, currentPrincipalObjectId, currentPrincipalType, err := getCurrentPrincipalForDatabase(ctx, cred)
	if err != nil {
		return "", fmt.Errorf("failed to get current principal information: %w", err)
	}

	log.Info().Msgf("Creating PostgreSQL server admin '%s'", currentPrincipalDisplayName)

	currentUserAdmin := armpostgresqlflexibleservers.ActiveDirectoryAdministratorAdd{
		Properties: &armpostgresqlflexibleservers.AdministratorPropertiesForAdd{
			PrincipalName: &currentPrincipalDisplayName,
			PrincipalType: &currentPrincipalType,
			TenantID:      Ptr(config.Cloud.TenantID),
		},
	}
	currentUserPoller, err := adminClient.BeginCreate(ctx, config.Cloud.ResourceGroup, serverName, currentPrincipalObjectId, currentUserAdmin, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create PostgreSQL server admin: %w", err)
	}

	if _, err := migrationRunnerPoller.PollUntilDone(ctx, nil); err != nil {
		return "", fmt.Errorf("failed to create PostgreSQL server admin: %w", err)
	}
	if _, err := currentUserPoller.PollUntilDone(ctx, nil); err != nil {
		return "", fmt.Errorf("failed to create PostgreSQL server admin: %w", err)
	}

	return currentPrincipalDisplayName, nil
}

// Create a tyger-owners role and grant it to the current principal and the migration runner's managed identity.
// The migration runner will grant full access to the tables it creates to this role.
func (inst *Installer) createRoles(
	ctx context.Context,
	server *armpostgresqlflexibleservers.Server,
	currentPrincipalDisplayName string,
	tygerServerIdentity *armmsi.Identity,
	migrationRunnerIdentity *armmsi.Identity,
) error {
	log.Info().Msg("Creating PostgreSQL roles")

	token, err := inst.Credential.GetToken(ctx, policy.TokenRequestOptions{
		TenantID: inst.Config.Cloud.TenantID,
		Scopes:   []string{"https://ossrdbms-aad.database.windows.net"},
	})

	if err != nil {
		return fmt.Errorf("failed to get database token: %w", err)
	}

	connectionString := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=verify-full",
		*server.Properties.FullyQualifiedDomainName, databasePort, currentPrincipalDisplayName, token.Token, databaseName)

	db, err := sql.Open("postgres", connectionString)
	if err != nil {
		return fmt.Errorf("failed to open database connection: %w", err)
	}
	defer db.Close()

	_, err = db.Exec(fmt.Sprintf(`
DO $$
BEGIN
	IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = '%s') THEN
		CREATE ROLE "%s";
	END IF;
END
$$`, ownersRole, ownersRole))
	if err != nil {
		return fmt.Errorf("failed to create %s database role: %w", ownersRole, err)
	}

	_, err = db.Exec(fmt.Sprintf(`GRANT "%s" TO "%s" WITH ADMIN TRUE`, ownersRole, *migrationRunnerIdentity.Name))
	if err != nil {
		return fmt.Errorf("failed to grant database role: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf(`GRANT "%s" TO "%s"`, ownersRole, currentPrincipalDisplayName))
	if err != nil {
		return fmt.Errorf("failed to grant database role: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf(`
DO $$
BEGIN
	IF NOT EXISTS (SELECT FROM pgaadauth_list_principals(false) WHERE objectId = '%s') THEN
		PERFORM pgaadauth_create_principal_with_oid('%s', '%s', 'service', false, false);
	END IF;
END
$$`, *tygerServerIdentity.Properties.PrincipalID, *tygerServerIdentity.Name, *tygerServerIdentity.Properties.PrincipalID))

	if err != nil {
		return fmt.Errorf("failed to create tyger server database principal: %w", err)
	}

	return nil
}

// Extract the current principal's display name, object ID and type from an ARM OAuth token.
// Note that we don't want to call the Graph API because that would permissions that are not always available in CI pipelines.
func getCurrentPrincipalForDatabase(ctx context.Context, cred azcore.TokenCredential) (displayName string, objectId string, principalType armpostgresqlflexibleservers.PrincipalType, err error) {
	tokenResponse, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{cloud.AzurePublic.Services[cloud.ResourceManager].Audience}})
	if err != nil {
		return displayName, objectId, principalType, fmt.Errorf("failed to get token: %w", err)
	}

	claims := jwt.MapClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(tokenResponse.Token, claims)
	if err != nil {
		return displayName, objectId, principalType, fmt.Errorf("failed to parse token: %w", err)
	}

	objectId = claims["oid"].(string)
	if uniqueName, ok := claims["unique_name"].(string); ok {
		displayName = uniqueName
	} else if appId, ok := claims["appid"].(string); ok {
		displayName = appId
	} else {
		displayName = objectId
	}

	if idType, ok := claims["idtyp"].(string); ok {
		switch idType {
		case "user":
			principalType = armpostgresqlflexibleservers.PrincipalTypeUser
		case "app":
			principalType = armpostgresqlflexibleservers.PrincipalTypeServicePrincipal
		default:
			return displayName, objectId, principalType, fmt.Errorf("unknown idtyp claim: %s", idType)
		}
	} else {
		return displayName, objectId, principalType, fmt.Errorf("missing idtyp claim")
	}

	return displayName, objectId, principalType, nil
}

// determine whether we need to update the database server by comparing the existing state and the desired state
func databaseServerNeedsUpdate(newServer, existingServer armpostgresqlflexibleservers.Server) (merged armpostgresqlflexibleservers.Server, needsUpdate bool) {
	needsUpdate = false
	merged = existingServer

	if *existingServer.Properties.Storage.Type == armpostgresqlflexibleservers.StorageTypePremiumLRS {
		// trying to update with these fields set will result in an error
		merged.Properties.Storage.Iops = nil
		merged.Properties.Storage.Iops = nil
	}

	if *newServer.SKU.Tier != *existingServer.SKU.Tier {
		merged.SKU.Tier = newServer.SKU.Tier
		needsUpdate = true
	}
	if *newServer.SKU.Name != *existingServer.SKU.Name {
		merged.SKU.Name = newServer.SKU.Name
		needsUpdate = true
	}

	if *newServer.Properties.Version != *existingServer.Properties.Version {
		merged.Properties.Version = newServer.Properties.Version
		needsUpdate = true
	}

	if *newServer.Properties.Backup.BackupRetentionDays != *existingServer.Properties.Backup.BackupRetentionDays {
		merged.Properties.Backup.BackupRetentionDays = newServer.Properties.Backup.BackupRetentionDays
		needsUpdate = true
	}

	if *newServer.Properties.Backup.GeoRedundantBackup != *existingServer.Properties.Backup.GeoRedundantBackup {
		merged.Properties.Backup.GeoRedundantBackup = newServer.Properties.Backup.GeoRedundantBackup
		needsUpdate = true
	}

	return merged, needsUpdate
}

// Creating a firewall rule seems to fail with an internal server error right after the server was created. This
// helper retries an operation a few times before giving up.
func retryableAsyncOperation[T any](ctx context.Context, begin func(context.Context) (*runtime.Poller[T], error)) (T, error) {
	for i := 0; ; i++ {
		poller, err := begin(ctx)
		var res T
		if err == nil {
			res, err = poller.PollUntilDone(ctx, nil)
		}

		if err != nil || i < 5 {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) && respErr.ErrorCode == "InternalServerError" {
				log.Debug().Str("errorCode", respErr.ErrorCode).Msg("Retrying after error")
				continue
			}
		}
		return res, nil
	}
}

func getRandomName() string {
	return strings.ReplaceAll(namesgenerator.GetRandomName(0), "_", "-")
}

func (inst *Installer) getUniqueSuffixTagKey() string {
	return fmt.Sprintf("tyger-unique-suffix-%s", inst.Config.EnvironmentName)
}

func (inst *Installer) getDatabaseConfiguredTagKey() string {
	return fmt.Sprintf("tyger-database-configured-%s", inst.Config.EnvironmentName)
}

// If you try to create a database server less than five days after one with the same name was deleted, you might get an error.
// For this reason, we allow the database server name to be left empty in the config, and we will give it a random name.
// The suffix of this name is stored in a tag on the managed identity resource.
// We used to store this tag on the resource group, but performing an API install would require persistent read access
// to the resource group, which is not allowed by an internal Microsoft policy.
func (inst *Installer) getDatabaseServerName(ctx context.Context, tygerServerManagedIdentity *armmsi.Identity, generateIfNecessary bool) (string, error) {
	if inst.Config.Cloud.DatabaseConfig.ServerName != "" {
		return inst.Config.Cloud.DatabaseConfig.ServerName, nil
	}

	// Use a generated name for the database.
	// Use or create a unique suffix and stored as a tag.

	tagsClient, err := armresources.NewTagsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create tags client: %w", err)
	}

	suffixTagKey := inst.getUniqueSuffixTagKey()

	miTagsResponse, err := tagsClient.GetAtScope(ctx, *tygerServerManagedIdentity.ID, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get tags: %w", err)
	}

	var suffix string
	if suffixTagValue, ok := miTagsResponse.TagsResource.Properties.Tags[suffixTagKey]; ok && suffixTagValue != nil {
		suffix = *suffixTagValue
	} else {
		// See if it is stored on the resource group, which is where it used to be stored.
		resourceGroupScope := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", inst.Config.Cloud.SubscriptionID, inst.Config.Cloud.ResourceGroup)
		rgTagsResponse, err := tagsClient.GetAtScope(ctx, resourceGroupScope, nil)
		if err != nil {
			return "", fmt.Errorf("failed to get tags: %w", err)
		}
		if suffixTagValue, ok := rgTagsResponse.TagsResource.Properties.Tags[suffixTagKey]; ok && suffixTagValue != nil {
			suffix = *suffixTagValue
			if generateIfNecessary {
				miTagsResponse.TagsResource.Properties.Tags[suffixTagKey] = suffixTagValue
				if _, err := tagsClient.CreateOrUpdateAtScope(ctx, *tygerServerManagedIdentity.ID, miTagsResponse.TagsResource, nil); err != nil {
					return "", fmt.Errorf("failed to set tags: %w", err)
				}
			}
		} else {
			if !generateIfNecessary {
				return "", errors.New("database server name is not set and no existing suffix is found")
			}

			suffix = getRandomName()
			miTagsResponse.TagsResource.Properties.Tags[suffixTagKey] = &suffix
			if _, err := tagsClient.CreateOrUpdateAtScope(ctx, *tygerServerManagedIdentity.ID, miTagsResponse.TagsResource, nil); err != nil {
				return "", fmt.Errorf("failed to set tags: %w", err)
			}
			rgTagsResponse.TagsResource.Properties.Tags[suffixTagKey] = &suffix
			if _, err := tagsClient.CreateOrUpdateAtScope(ctx, resourceGroupScope, rgTagsResponse.TagsResource, nil); err != nil {
				return "", fmt.Errorf("failed to set tags: %w", err)
			}
		}
	}

	return fmt.Sprintf("%s-tyger-%s", inst.Config.EnvironmentName, suffix), nil
}

// Export logs to Log Analytics.
func (inst *Installer) enableDiagnosticSettings(ctx context.Context, server *armpostgresqlflexibleservers.Server) error {
	log.Info().Msg("Enabling diagnostics on PostgreSQL server")

	diagnosticsSettingsClient, err := armmonitor.NewDiagnosticSettingsClient(inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create diagnostics settings client: %w", err)
	}

	oic, err := armoperationalinsights.NewWorkspacesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create operational insights client: %w", err)
	}

	workspace, err := oic.Get(ctx, inst.Config.Cloud.LogAnalyticsWorkspace.ResourceGroup, inst.Config.Cloud.LogAnalyticsWorkspace.Name, nil)
	if err != nil {
		return fmt.Errorf("failed to get Log Analytics workspace: %w", err)
	}

	settings := armmonitor.DiagnosticSettingsResource{
		Properties: &armmonitor.DiagnosticSettings{
			Logs: []*armmonitor.LogSettings{
				{
					CategoryGroup: Ptr("audit"),
					Enabled:       Ptr(true),
				},
				{
					CategoryGroup: Ptr("allLogs"),
					Enabled:       Ptr(true),
				},
			},
			Metrics: []*armmonitor.MetricSettings{
				{
					Category: Ptr("AllMetrics"),
					Enabled:  Ptr(true),
				},
			},
			WorkspaceID: workspace.ID,
		},
	}
	if _, err := diagnosticsSettingsClient.CreateOrUpdate(ctx, *server.ID, "allLogs", settings, nil); err != nil {
		return fmt.Errorf("failed to enable diagnostics on PostgreSQL server: %w", err)
	}

	log.Info().Msg("Diagnostics on PostgreSQL server enabled")
	return nil
}
