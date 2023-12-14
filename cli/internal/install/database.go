package install

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
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/docker/docker/pkg/namesgenerator"
	"github.com/golang-jwt/jwt/v5"
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
	ownersRole         = "tyger-owners"
	databasePort       = 5432
	databaseName       = "postgres"
	dbConfiguredTagKey = "tyger-db-configured"
)

func createDatabase(ctx context.Context, managedIdentityPromise *Promise[*armmsi.Identity]) (any, error) {
	config := GetConfigFromContext(ctx)
	databaseConfig := *config.Cloud.DatabaseConfig
	cred := GetAzureCredentialFromContext(ctx)

	serverName, err := getDatabaseServerName(ctx, config, cred, true)
	if err != nil {
		return nil, err
	}

	client, err := armpostgresqlflexibleservers.NewServersClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL server client: %w", err)
	}

	var tags map[string]*string
	var existingServer *armpostgresqlflexibleservers.Server

	if existingServerResponse, err := client.Get(ctx, config.Cloud.ResourceGroup, serverName, nil); err == nil {
		existingServer = &existingServerResponse.Server
		if existingTag, ok := existingServerResponse.Tags[TagKey]; ok {
			if *existingTag != config.EnvironmentName {
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
	tags[TagKey] = &config.EnvironmentName

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
		poller, err := client.BeginCreate(ctx, config.Cloud.ResourceGroup, serverName, serverParameters, nil)
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

	// check if the database has already been configured and we can skip the steps that follow
	if value, ok := tags[dbConfiguredTagKey]; ok && value != nil && *value == config.EnvironmentName {
		log.Info().Msg("PostgreSQL server is already configured")
		return nil, nil
	}

	mi, err := managedIdentityPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	currentPrincipalDisplayName, err := createDatabaseAdmins(ctx, config, serverName, cred, databaseConfig, mi)
	if err != nil {
		return nil, err
	}

	firewallClient, err := armpostgresqlflexibleservers.NewFirewallRulesClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL server firewall client: %w", err)
	}

	// Run two tasks in parallel:
	promiseGroup := &PromiseGroup{}

	// Task one:
	// 1. create a temporary firewall rule that allows connections from anywhere (so that we can connect from this machine)
	// 2. create the necessary database roles
	// 3. delete the temporary firewall rule
	NewPromise(ctx, promiseGroup, func(ctx context.Context) (any, error) {
		log.Info().Msg("Creating temporary PostgreSQL server firewall rule")

		temporaryAllowAllFirewallRule := "TemporaryAllowAllRule"
		temporaryAllowAllRule := armpostgresqlflexibleservers.FirewallRule{
			Properties: &armpostgresqlflexibleservers.FirewallRuleProperties{
				StartIPAddress: Ptr("0.0.0.0"),
				EndIPAddress:   Ptr("255.255.255.255"),
			},
		}

		_, err = retryableAsyncOperation(ctx, func(ctx context.Context) (*runtime.Poller[armpostgresqlflexibleservers.FirewallRulesClientCreateOrUpdateResponse], error) {
			return firewallClient.BeginCreateOrUpdate(ctx, config.Cloud.ResourceGroup, serverName, temporaryAllowAllFirewallRule, temporaryAllowAllRule, nil)
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create temporary PostgreSQL server firewall rule: %w", err)
		}

		if err := createRoles(ctx, cred, config, existingServer, currentPrincipalDisplayName); err != nil {
			return nil, err
		}

		log.Info().Msg("Deleting temporary PostgreSQL server firewall rule")

		deletePoller, err := firewallClient.BeginDelete(ctx, config.Cloud.ResourceGroup, serverName, temporaryAllowAllFirewallRule, nil)
		if err == nil {
			_, err = deletePoller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to delete temporary PostgreSQL server firewall rule: %w", err)
		}
		return nil, err
	})

	// Task two: create a permanent firewall rule that allows connections from Azure services and resources
	// (we should support private networking in the future)
	NewPromise(ctx, promiseGroup, func(ctx context.Context) (any, error) {
		log.Info().Msg("Adding permanent firewall rule")
		allowAllAzureRule := armpostgresqlflexibleservers.FirewallRule{
			Properties: &armpostgresqlflexibleservers.FirewallRuleProperties{
				StartIPAddress: Ptr("0.0.0.0"),
				EndIPAddress:   Ptr("0.0.0.0"),
			},
		}

		_, err := retryableAsyncOperation(ctx, func(ctx context.Context) (*runtime.Poller[armpostgresqlflexibleservers.FirewallRulesClientCreateOrUpdateResponse], error) {
			return firewallClient.BeginCreateOrUpdate(ctx, config.Cloud.ResourceGroup, serverName, "AllowAllAzureServicesAndResources", allowAllAzureRule, nil)
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create PostgreSQL server firewall rule: %w", err)
		}

		return nil, nil
	})

	// wait for the two tasks to complete
	for _, p := range *promiseGroup {
		if err := p.AwaitErr(); err != nil {
			return nil, err
		}
	}

	// add a tag on the server to indicate that it is configured, so we can skip the slow firewall configuration next time
	existingServer.Tags[dbConfiguredTagKey] = &config.EnvironmentName

	tagsClient, err := armresources.NewTagsClient(config.Cloud.SubscriptionID, cred, nil)
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
func createDatabaseAdmins(ctx context.Context, config *EnvironmentConfig, serverName string, cred azcore.TokenCredential, databaseConfig DatabaseConfig, mi *armmsi.Identity) (currentPrincipalDisplayname string, err error) {
	adminClient, err := armpostgresqlflexibleservers.NewAdministratorsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create PostgreSQL server admin client: %w", err)
	}

	log.Info().Msg("Creating PostgreSQL server admins")

	if _, err = adminClient.BeginCreate(ctx, config.Cloud.ResourceGroup, serverName, *mi.Properties.PrincipalID, armpostgresqlflexibleservers.ActiveDirectoryAdministratorAdd{
		Properties: &armpostgresqlflexibleservers.AdministratorPropertiesForAdd{
			PrincipalName: mi.Name,
			PrincipalType: Ptr(armpostgresqlflexibleservers.PrincipalTypeServicePrincipal),
			TenantID:      Ptr(config.Cloud.TenantID),
		},
	}, nil); err != nil {
		return "", fmt.Errorf("failed to create PostgreSQL server admin: %w", err)
	}

	currentPrincipalDisplayName, currentPrincipalObjectId, currentPrincipalType, err := getCurrentPrincipalForDatabase(ctx, cred)
	if err != nil {
		return "", fmt.Errorf("failed to get current principal information: %w", err)
	}

	if _, err = adminClient.BeginCreate(ctx, config.Cloud.ResourceGroup, serverName, currentPrincipalObjectId, armpostgresqlflexibleservers.ActiveDirectoryAdministratorAdd{
		Properties: &armpostgresqlflexibleservers.AdministratorPropertiesForAdd{
			PrincipalName: &currentPrincipalDisplayName,
			PrincipalType: &currentPrincipalType,
			TenantID:      Ptr(config.Cloud.TenantID),
		},
	}, nil); err != nil {
		return "", fmt.Errorf("failed to create PostgreSQL server admin: %w", err)
	}
	return currentPrincipalDisplayName, nil
}

// Create a tyger-owners role and grant it to the current principal and the migration runner's managed identity.
// The migration runner will grant full access to the tables it creates to this role.
func createRoles(ctx context.Context, cred azcore.TokenCredential, config *EnvironmentConfig, server *armpostgresqlflexibleservers.Server, currentPrincipalDisplayName string) error {
	log.Info().Msg("Creating PostgreSQL roles")

	token, err := cred.GetToken(ctx, policy.TokenRequestOptions{
		TenantID: config.Cloud.TenantID,
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
		return fmt.Errorf("failed to create database role: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf(`GRANT "%s" TO "%s" WITH ADMIN TRUE`, ownersRole, tygerManagedIdentityName))
	if err != nil {
		return fmt.Errorf("failed to grant database role: %w", err)
	}

	_, err = db.Exec(fmt.Sprintf(`GRANT "%s" TO "%s"`, ownersRole, currentPrincipalDisplayName))
	if err != nil {
		return fmt.Errorf("failed to grant database role: %w", err)
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
				log.Info().Str("errorCode", respErr.ErrorCode).Msg("retrying after error")
				continue
			}
		}
		return res, nil
	}
}

func getRandomName() string {
	return strings.ReplaceAll(namesgenerator.GetRandomName(0), "_", "-")
}

func getUniqueSuffixTagKey(config *EnvironmentConfig) string {
	return fmt.Sprintf("tyger-unique-suffix-%s", config.EnvironmentName)
}

// If you try to create a database server less than five days after one with the same name was deleted, you might get an error.
// For this reason, we allow the database server name to be left empty in the config, and we will give it a random name.
// The suffix of this name is stored in a tag on the resource group.
func getDatabaseServerName(ctx context.Context, config *EnvironmentConfig, cred azcore.TokenCredential, generateIfNecessary bool) (string, error) {
	if config.Cloud.DatabaseConfig.ServerName != "" {
		return config.Cloud.DatabaseConfig.ServerName, nil
	}

	// Use a generated name for the database.
	// Use or create a unique suffix and stored as a tag.

	tagsClient, err := armresources.NewTagsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create tags client: %w", err)
	}

	suffixTagKey := getUniqueSuffixTagKey(config)
	scope := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s", config.Cloud.SubscriptionID, config.Cloud.ResourceGroup)
	getTagsResponse, err := tagsClient.GetAtScope(ctx, scope, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get tags: %w", err)
	}

	var suffix string
	if suffixTagValue, ok := getTagsResponse.TagsResource.Properties.Tags[suffixTagKey]; ok && suffixTagValue != nil {
		suffix = *suffixTagValue
	} else {
		if !generateIfNecessary {
			return "", errors.New("database server name is not set and no existing suffix is found")
		}

		suffix = getRandomName()
		getTagsResponse.TagsResource.Properties.Tags[suffixTagKey] = &suffix
		if _, err := tagsClient.CreateOrUpdateAtScope(ctx, scope, getTagsResponse.TagsResource, nil); err != nil {
			return "", fmt.Errorf("failed to set tags: %w", err)
		}
	}

	return fmt.Sprintf("%s-tyger-%s", config.EnvironmentName, suffix), nil
}
