package install

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/http"
	"strconv"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
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

	client, err := armpostgresqlflexibleservers.NewServersClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL server client: %w", err)
	}

	var tags map[string]*string
	var existingServer *armpostgresqlflexibleservers.Server

	if existingServerResponse, err := client.Get(ctx, config.Cloud.ResourceGroup, databaseConfig.ServerName, nil); err == nil {
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
				StorageSizeGB: Ptr(int32(databaseConfig.InitialDatabaseSizeGb)),
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
			CreateMode: Ptr(armpostgresqlflexibleservers.CreateModeReviveDropped),
		},
	}

	serverNeedsUpdate := existingServer == nil
	if !serverNeedsUpdate {
		serverParameters, serverNeedsUpdate = databaseServerNeedsUpdate(serverParameters, *existingServer)
	}

	if serverNeedsUpdate {
		log.Info().Msg("Creating or updating PostgreSQL server")
		poller, err := client.BeginCreate(ctx, config.Cloud.ResourceGroup, databaseConfig.ServerName, serverParameters, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create PostgreSQL server: %w", err)
		}

		log.Info().Msg("Waiting for PostgreSQL server to be created")

		createdDatabaseServer, err := poller.PollUntilDone(ctx, nil)
		existingServer = &createdDatabaseServer.Server
		if err != nil {
			return nil, fmt.Errorf("failed to create PostgreSQL server: %w", err)
		}
	} else {
		log.Info().Msgf("PostgreSQL server '%s' appears to be up to date", *existingServer.Name)
	}

	if value, ok := tags[dbConfiguredTagKey]; ok && value != nil && *value == config.EnvironmentName {
		log.Info().Msg("PostgreSQL server is already configured")
		return nil, nil
	}

	mi, err := managedIdentityPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	currentPrincipalDisplayName, err := createDatabaseAdmins(ctx, config, cred, databaseConfig, mi)
	if err != nil {
		return nil, err
	}

	firewallClient, err := armpostgresqlflexibleservers.NewFirewallRulesClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL server firewall client: %w", err)
	}

	promiseGroup := &PromiseGroup{}
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
			return firewallClient.BeginCreateOrUpdate(ctx, config.Cloud.ResourceGroup, databaseConfig.ServerName, temporaryAllowAllFirewallRule, temporaryAllowAllRule, nil)
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create temporary PostgreSQL server firewall rule: %w", err)
		}

		if err := createRoles(ctx, cred, config, existingServer, currentPrincipalDisplayName); err != nil {
			return nil, err
		}

		log.Info().Msg("Deleting temporary PostgreSQL server firewall rules")

		deletePoller, err := firewallClient.BeginDelete(ctx, config.Cloud.ResourceGroup, databaseConfig.ServerName, temporaryAllowAllFirewallRule, nil)
		if err == nil {
			_, err = deletePoller.PollUntilDone(ctx, nil)
		}
		if err != nil {
			return nil, fmt.Errorf("failed to delete temporary PostgreSQL server firewall rule: %w", err)
		}
		return nil, err
	})

	NewPromise(ctx, promiseGroup, func(ctx context.Context) (any, error) {
		log.Info().Msg("Adding permanent firewall rule")
		allowAllAzureRule := armpostgresqlflexibleservers.FirewallRule{
			Properties: &armpostgresqlflexibleservers.FirewallRuleProperties{
				StartIPAddress: Ptr("0.0.0.0"),
				EndIPAddress:   Ptr("0.0.0.0"),
			},
		}

		_, err := retryableAsyncOperation(ctx, func(ctx context.Context) (*runtime.Poller[armpostgresqlflexibleservers.FirewallRulesClientCreateOrUpdateResponse], error) {
			return firewallClient.BeginCreateOrUpdate(ctx, config.Cloud.ResourceGroup, databaseConfig.ServerName, "AllowAllAzureServicesAndResources", allowAllAzureRule, nil)
		})
		if err != nil {
			return nil, fmt.Errorf("failed to create PostgreSQL server firewall rule: %w", err)
		}

		return nil, nil
	})

	for _, p := range *promiseGroup {
		if err := p.AwaitErr(); err != nil {
			return nil, err
		}
	}

	// add a tag on the server to indicate that it is configured, so we can skip the slow firewall configuration next time
	tagsClient, err := armresources.NewTagsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create tags client: %w", err)
	}

	existingServer.Tags[dbConfiguredTagKey] = &config.EnvironmentName

	_, err = tagsClient.CreateOrUpdateAtScope(ctx, *existingServer.ID, armresources.TagsResource{Properties: &armresources.Tags{Tags: existingServer.Tags}}, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to tag PostgreSQL server: %w", err)
	}

	return nil, nil
}

// Add the given managed identity and the current user as admins
func createDatabaseAdmins(ctx context.Context, config *EnvironmentConfig, cred azcore.TokenCredential, databaseConfig DatabaseConfig, mi *armmsi.Identity) (currentPrincipalDisplayname string, err error) {
	adminClient, err := armpostgresqlflexibleservers.NewAdministratorsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create PostgreSQL server admin client: %w", err)
	}

	log.Info().Msg("Creating PostgreSQL server admins")

	if _, err = adminClient.BeginCreate(ctx, config.Cloud.ResourceGroup, databaseConfig.ServerName, *mi.Properties.PrincipalID, armpostgresqlflexibleservers.ActiveDirectoryAdministratorAdd{
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

	if _, err = adminClient.BeginCreate(ctx, config.Cloud.ResourceGroup, databaseConfig.ServerName, currentPrincipalObjectId, armpostgresqlflexibleservers.ActiveDirectoryAdministratorAdd{
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

func databaseServerNeedsUpdate(newServer, existingServer armpostgresqlflexibleservers.Server) (merged armpostgresqlflexibleservers.Server, needsUpdate bool) {
	needsUpdate = false
	merged = existingServer

	// trying to update with these fields will result in an error
	merged.Properties.Storage.Iops = nil
	merged.Properties.Storage.Iops = nil

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

// creating a firewall rule seems to fail with an internal server error right after the server was created. This
// helper retries the operation a few times before giving up.
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
