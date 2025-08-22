// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"maps"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/monitor/armmonitor"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/operationalinsights/armoperationalinsights/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v3"
	"github.com/golang-jwt/jwt/v5"
	"github.com/google/uuid"
	"github.com/hashicorp/go-cleanhttp"
	"github.com/ipinfo/go/v2/ipinfo"
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
	unqualifiedOwnersRole  = "tyger-owners"
	databasePort           = 5432
	defaultDatabaseName    = "postgres"
	dbServerInstanceTagKey = "tyger-database-instance"
	dbConfiguredTagVersion = "3" // bump this when we change the database configuration logic
)

func (inst *Installer) createDatabaseServer(ctx context.Context) (any, error) {
	databaseConfig := inst.Config.Cloud.Database

	serverName := inst.Config.Cloud.Database.ServerName

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
			tags = make(map[string]*string)
			maps.Copy(tags, existingServerResponse.Tags)
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
	if instanceKey := tags[dbServerInstanceTagKey]; instanceKey == nil || *instanceKey == "" {
		tags[dbServerInstanceTagKey] = Ptr(uuid.NewString())
	}

	geoRedundantBackup := armpostgresqlflexibleservers.GeoRedundantBackupEnumDisabled
	if databaseConfig.BackupGeoRedundancy {
		geoRedundantBackup = armpostgresqlflexibleservers.GeoRedundantBackupEnumEnabled
	}

	var publicNetworkAccess *armpostgresqlflexibleservers.ServerPublicNetworkAccessState
	if inst.Config.Cloud.PrivateNetworking {
		publicNetworkAccess = Ptr(armpostgresqlflexibleservers.ServerPublicNetworkAccessStateDisabled)
	} else {
		publicNetworkAccess = Ptr(armpostgresqlflexibleservers.ServerPublicNetworkAccessStateEnabled)
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
			Version: Ptr(armpostgresqlflexibleservers.ServerVersion(strconv.Itoa(*databaseConfig.PostgresMajorVersion))),
			Storage: &armpostgresqlflexibleservers.Storage{
				AutoGrow:      Ptr(armpostgresqlflexibleservers.StorageAutoGrowEnabled),
				StorageSizeGB: Ptr(int32(*databaseConfig.StorageSizeGB)),
			},
			Network: &armpostgresqlflexibleservers.Network{
				PublicNetworkAccess: publicNetworkAccess,
			},
			Backup: &armpostgresqlflexibleservers.Backup{
				BackupRetentionDays: Ptr(int32(*databaseConfig.BackupRetentionDays)),
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
		log.Ctx(ctx).Info().Msgf("Creating or updating PostgreSQL server '%s'", serverName)

		poller, err := client.BeginCreate(ctx, inst.Config.Cloud.ResourceGroup, serverName, serverParameters, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create PostgreSQL server: %w", err)
		}

		createdDatabaseServer, err := poller.PollUntilDone(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create PostgreSQL server: %w", err)
		}

		if existingServer != nil {
			if *serverParameters.SKU.Tier != *existingServer.SKU.Tier || *serverParameters.SKU.Name != *existingServer.SKU.Name {

				// We have scaled the server down or up. The `max_connections` parameter should be updated to match the new size according to
				// the table in https://learn.microsoft.com/en-us/azure/postgresql/flexible-server/concepts-limits#maximum-connections.
				// There are two problems with this:
				// 1. Setting the parameter requires a server restart, which causes downtime, so we shouldn't do this automatically.
				// 2. The call to set the config (a call to the RP) does not always stick. If it doesn't, restarting the server and trying again seems to do the trick.

				// What we do is print out a bash one-liner to that uses the Azure CLI to set the parameter and restart the server. Not pretty, but it seems to be
				// better than `tyger cloud install` causing downtime.

				configClient, err := armpostgresqlflexibleservers.NewConfigurationsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
				if err != nil {
					return nil, fmt.Errorf("failed to create PostgreSQL server configuration client: %w", err)
				}

				configResponse, err := configClient.Get(ctx, inst.Config.Cloud.ResourceGroup, serverName, "max_connections", nil)
				if err != nil {
					return nil, fmt.Errorf("failed to get PostgreSQL server max_connections configuration: %w", err)
				}

				if *configResponse.Properties.Value != *configResponse.Properties.DefaultValue || *configResponse.Properties.IsConfigPendingRestart {
					commandToRun := fmt.Sprintf(
						`DESIRED_VALUE=%s; RESOURCE_GROUP="%s"; SERVER_NAME="%s"; SUBSCRIPTION_ID="%s"; `+
							`until [ "$(az postgres flexible-server parameter set --name max_connections --value $DESIRED_VALUE --resource-group $RESOURCE_GROUP --server-name $SERVER_NAME --subscription $SUBSCRIPTION_ID  -o tsv --query 'value')" = "$DESIRED_VALUE" ] && echo "Parameter set successfully to $DESIRED_VALUE."; `+
							`do echo "Failed to set parameter. Restarting server and retrying..."; `+
							`az postgres flexible-server restart --resource-group $RESOURCE_GROUP --name $SERVER_NAME --subscription $SUBSCRIPTION_ID; `+
							`done; `+
							`echo "Restarting the server to apply changes..."; `+
							`az postgres flexible-server restart --resource-group $RESOURCE_GROUP --name $SERVER_NAME --subscription $SUBSCRIPTION_ID`,
						*configResponse.Properties.DefaultValue, inst.Config.Cloud.ResourceGroup, serverName, inst.Config.Cloud.SubscriptionID)
					log.Ctx(ctx).Warn().Msgf("The database server size has been changed. It is recommended to update the max_connections parameter suitable for the new server size.")
					log.Ctx(ctx).Warn().Msgf("Run the following command to update the parameter and restart the server: `%s` ", commandToRun)
					log.Ctx(ctx).Warn().Msg("Note that running the above command will restart the database server and cause downtime.")
				}
			}
		}

		existingServer = &createdDatabaseServer.Server
	} else {
		log.Ctx(ctx).Info().Msgf("PostgreSQL server '%s' appears to be up to date", *existingServer.Name)
	}

	if err := assignRbacRole(ctx, inst.Config.Cloud.Compute.GetManagementPrincipalIds(), true, *existingServer.ID, "Reader", inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
		return nil, fmt.Errorf("failed to assign RBAC role on database: %w", err)
	}

	if inst.Config.Cloud.LogAnalyticsWorkspace != nil {
		if err := inst.enableDiagnosticSettings(ctx, existingServer); err != nil {
			return nil, err
		}
	}

	if err := createDatabaseServerAdmin(ctx, inst.Config, serverName, inst.Credential); err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL server admin: %w", err)
	}

	if inst.Config.Cloud.PrivateNetworking {
		if err := inst.createPrivateEndpointsForPostgresFlexibleServer(ctx, existingServer); err != nil {
			return nil, fmt.Errorf("failed to create private endpoints for PostgreSQL server '%s': %w", serverName, err)
		}
	} else {
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
				log.Ctx(ctx).Info().Msgf("Creating or updating PostgreSQL server firewall rule '%s'", nameSnapshot)
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
					log.Ctx(ctx).Info().Msgf("Deleting PostgreSQL server firewall rule '%s'", nameSnapshot)
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
	}

	return nil, nil
}

func (inst *Installer) deleteDatabase(ctx context.Context, org *OrganizationConfig) (any, error) {
	client, err := armpostgresqlflexibleservers.NewServersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL server client: %w", err)
	}

	server, err := client.Get(ctx, inst.Config.Cloud.ResourceGroup, inst.Config.Cloud.Database.ServerName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get PostgreSQL server: %w", err)
	}

	dbClient, err := armpostgresqlflexibleservers.NewDatabasesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL server database client: %w", err)
	}

	poller, err := dbClient.BeginDelete(ctx, inst.Config.Cloud.ResourceGroup, inst.Config.Cloud.Database.ServerName, org.Cloud.DatabaseName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to delete PostgreSQL database: %w", err)
	}

	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return nil, fmt.Errorf("failed to delete PostgreSQL database: %w", err)
	}

	err = inst.runWithTemporaryFilewallRuleIfNeeded(ctx, org, func() error {
		currentPrincipalDisplayName, _, _, err := getCurrentPrincipalForDatabase(ctx, inst.Credential)
		if err != nil {
			return fmt.Errorf("failed to get current principal information: %w", err)
		}

		return inst.dropRoles(ctx, &server.Server, org, currentPrincipalDisplayName)
	})

	if err != nil {
		return nil, fmt.Errorf("failed to drop PostgreSQL roles: %w", err)
	}

	return nil, nil
}

func (inst *Installer) createDatabase(ctx context.Context, org *OrganizationConfig, tygerServerManagedIdentityPromise, migrationRunnerManagedIdentityPromise *install.Promise[*armmsi.Identity]) (any, error) {
	serverName := inst.Config.Cloud.Database.ServerName

	client, err := armpostgresqlflexibleservers.NewServersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL server client: %w", err)
	}

	existingServerResponse, err := client.Get(ctx, inst.Config.Cloud.ResourceGroup, serverName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get PostgreSQL server: %w", err)
	}

	existingServer := &existingServerResponse.Server

	databasesClient, err := armpostgresqlflexibleservers.NewDatabasesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL server database client: %w", err)
	}

	if _, err := databasesClient.Get(ctx, inst.Config.Cloud.ResourceGroup, serverName, org.Cloud.DatabaseName, nil); err != nil {
		var repErr *azcore.ResponseError
		if errors.As(err, &repErr) && repErr.StatusCode == http.StatusNotFound {
			log.Ctx(ctx).Info().Msgf("Creating PostgreSQL server database '%s'", org.Cloud.DatabaseName)
			if poller, err := databasesClient.BeginCreate(ctx, inst.Config.Cloud.ResourceGroup, serverName, org.Cloud.DatabaseName, armpostgresqlflexibleservers.Database{}, nil); err != nil {
				return nil, fmt.Errorf("failed to create PostgreSQL server database: %w", err)
			} else {
				if _, err := poller.PollUntilDone(ctx, nil); err != nil {
					return nil, fmt.Errorf("failed to create PostgreSQL server database: %w", err)
				}
			}
		} else {
			return nil, fmt.Errorf("failed to get PostgreSQL server database: %w", err)
		}
	}

	tygerServerManagedIdentity, err := tygerServerManagedIdentityPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	migrationRunnerManagedIdentity, err := migrationRunnerManagedIdentityPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	// check if the database has already been configured and we can skip the steps that follow.
	// Note that we tag the database server with a GUID so that the tag will not be valid if the database server is deleted and recreated.
	instanceKeyValue := existingServerResponse.Tags[dbServerInstanceTagKey]
	var dbConfiguredTagValue string
	if instanceKeyValue != nil && *instanceKeyValue != "" {
		dbConfiguredTagValue = fmt.Sprintf("%s-%s", dbConfiguredTagVersion, *instanceKeyValue)
		if migrationRunnerManagedIdentity.Tags != nil {
			if value, ok := migrationRunnerManagedIdentity.Tags[inst.getDatabaseConfiguredTagKey()]; ok && value != nil && *value == dbConfiguredTagValue {
				log.Ctx(ctx).Info().Msg("PostgreSQL server is already configured")
				return nil, nil
			}
		}
	}

	err = inst.runWithTemporaryFilewallRuleIfNeeded(ctx, org, func() error {
		currentPrincipalDisplayName, _, _, err := getCurrentPrincipalForDatabase(ctx, inst.Credential)
		if err != nil {
			return fmt.Errorf("failed to get current principal information: %w", err)
		}

		return inst.createRoles(ctx, existingServer, org, currentPrincipalDisplayName, tygerServerManagedIdentity, migrationRunnerManagedIdentity)
	})

	if err != nil {
		return nil, fmt.Errorf("failed to create PostgreSQL roles: %w", err)
	}

	if dbConfiguredTagValue != "" {
		// add a tag on the server to indicate that it is configured, so we can skip the slow firewall configuration next time
		if migrationRunnerManagedIdentity.Tags == nil {
			migrationRunnerManagedIdentity.Tags = make(map[string]*string)
		}
		migrationRunnerManagedIdentity.Tags[inst.getDatabaseConfiguredTagKey()] = Ptr(dbConfiguredTagValue)

		tagsClient, err := armresources.NewTagsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create tags client: %w", err)
		}

		poller, err := tagsClient.BeginCreateOrUpdateAtScope(ctx, *migrationRunnerManagedIdentity.ID, armresources.TagsResource{Properties: &armresources.Tags{Tags: migrationRunnerManagedIdentity.Tags}}, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to tag managed identity: %w", err)
		}
		if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			return nil, fmt.Errorf("failed to tag managed identity: %w", err)
		}
	}

	return nil, nil
}

func (inst *Installer) runWithTemporaryFilewallRuleIfNeeded(ctx context.Context, org *OrganizationConfig, action func() error) error {
	if !inst.Config.Cloud.PrivateNetworking {
		return action()
	}

	temporaryFirewallRuleName := fmt.Sprintf("temp_allow_installer_%s", org.Name)
	var temporaryFirewallRule *armpostgresqlflexibleservers.FirewallRule
	// Get the current IP address. Create a new "clean" HTTP client that does not use any proxy, as database connections will not use one.
	ipInfoClient := ipinfo.NewClient(cleanhttp.DefaultClient(), nil, "")
	if ip, err := ipInfoClient.GetIPInfo(nil); err != nil {
		log.Ctx(ctx).Warn().Msgf("Unable to determine the current public IP address. You may need to add the current IP address manually as a database firewall rule to allow connectivity")
	} else {
		temporaryFirewallRule = &armpostgresqlflexibleservers.FirewallRule{
			Properties: &armpostgresqlflexibleservers.FirewallRuleProperties{
				StartIPAddress: Ptr(ip.IP.String()),
				EndIPAddress:   Ptr(ip.IP.String()),
			},
		}
	}

	firewallClient, err := armpostgresqlflexibleservers.NewFirewallRulesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL server firewall client: %w", err)
	}

	if temporaryFirewallRule != nil {
		log.Ctx(ctx).Info().Msgf("Creating temporary PostgreSQL server firewall rule '%s'", temporaryFirewallRuleName)
		_, err = retryableAsyncOperation(ctx, func(ctx context.Context) (*runtime.Poller[armpostgresqlflexibleservers.FirewallRulesClientCreateOrUpdateResponse], error) {
			return firewallClient.BeginCreateOrUpdate(ctx, inst.Config.Cloud.ResourceGroup, inst.Config.Cloud.Database.ServerName, temporaryFirewallRuleName, *temporaryFirewallRule, nil)
		})
		if err != nil {
			return fmt.Errorf("failed to create PostgreSQL server firewall rule: %w", err)
		}

		defer func() {
			log.Ctx(ctx).Info().Msgf("Deleting temporary PostgreSQL server firewall rule '%s'", temporaryFirewallRuleName)
			_, err = retryableAsyncOperation(ctx, func(ctx context.Context) (*runtime.Poller[armpostgresqlflexibleservers.FirewallRulesClientDeleteResponse], error) {
				return firewallClient.BeginDelete(ctx, inst.Config.Cloud.ResourceGroup, inst.Config.Cloud.Database.ServerName, temporaryFirewallRuleName, nil)
			})
			if err != nil {
				log.Warn().Msgf("failed to delete PostgreSQL server firewall rule: %v", err)
			}
		}()
	}

	return action()
}

// Add the current user as admins on the database server.
// These are not superusers.
func createDatabaseServerAdmin(
	ctx context.Context,
	config *CloudEnvironmentConfig,
	serverName string,
	cred azcore.TokenCredential,
) (err error) {
	adminClient, err := armpostgresqlflexibleservers.NewAdministratorsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL server admin client: %w", err)
	}

	currentPrincipalDisplayName, currentPrincipalObjectId, currentPrincipalType, err := getCurrentPrincipalForDatabase(ctx, cred)
	if err != nil {
		return fmt.Errorf("failed to get current principal information: %w", err)
	}

	currentUserAdmin := armpostgresqlflexibleservers.ActiveDirectoryAdministratorAdd{
		Properties: &armpostgresqlflexibleservers.AdministratorPropertiesForAdd{
			PrincipalName: &currentPrincipalDisplayName,
			PrincipalType: &currentPrincipalType,
			TenantID:      Ptr(config.Cloud.TenantID),
		},
	}

	if _, err = adminClient.Get(ctx, config.Cloud.ResourceGroup, serverName, currentPrincipalObjectId, nil); err != nil {
		var repErr *azcore.ResponseError
		if !errors.As(err, &repErr) || repErr.StatusCode != http.StatusNotFound {
			return fmt.Errorf("failed to get PostgreSQL server admin: %w", err)
		}
	} else {
		log.Ctx(ctx).Info().Msgf("PostgreSQL server admin '%s' already exists", currentPrincipalDisplayName)
		return nil
	}

	log.Ctx(ctx).Info().Msgf("Creating PostgreSQL server admin '%s'", currentPrincipalDisplayName)

	currentUserPoller, err := adminClient.BeginCreate(ctx, config.Cloud.ResourceGroup, serverName, currentPrincipalObjectId, currentUserAdmin, nil)
	if err != nil {
		return fmt.Errorf("failed to create PostgreSQL server admin: %w", err)
	}

	if _, err := currentUserPoller.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("failed to create PostgreSQL server admin: %w", err)
	}

	return nil
}

func getDatabaseRoleName(org *OrganizationConfig, simpleRoleName string) string {
	if org.SingleOrganizationCompatibilityMode {
		return simpleRoleName
	}

	return fmt.Sprintf("%s-%s", org.Name, simpleRoleName)
}

func (inst *Installer) executeOnDatabase(ctx context.Context, host, databaseName, roleName string, action func(db *sql.DB) error) error {
	token, err := inst.Credential.GetToken(ctx, policy.TokenRequestOptions{
		TenantID: inst.Config.Cloud.TenantID,
		Scopes:   []string{"https://ossrdbms-aad.database.windows.net"},
	})

	if err != nil {
		return fmt.Errorf("failed to get database token: %w", err)
	}

	connectionString := fmt.Sprintf("host=%s port=%d user=%s password=%s dbname=%s sslmode=verify-full",
		host, databasePort, roleName, token.Token, databaseName)

	db, err := sql.Open("postgres", connectionString)
	if err == nil {
		err = db.PingContext(ctx)
	}
	if err != nil {
		log.Ctx(ctx).Warn().Msgf("For network connectivity problems to the database, try adding your current public IP address to the database firewall rules in the config file and verify that there is no firewall in your environment that is blocking access to %s on port %d.", host, databasePort)
		return fmt.Errorf("failed to open database connection: %w", err)
	}
	defer db.Close()

	return action(db)
}

func (inst *Installer) dropRoles(ctx context.Context, server *armpostgresqlflexibleservers.Server, org *OrganizationConfig, currentPrincipalDisplayName string) error {
	log.Ctx(ctx).Info().Msg("Dropping PostgreSQL roles")

	return inst.executeOnDatabase(ctx, *server.Properties.FullyQualifiedDomainName, defaultDatabaseName, currentPrincipalDisplayName, func(db *sql.DB) error {
		dropRole := func(roleName string) error {
			if _, err := db.Exec(fmt.Sprintf(`DROP ROLE IF EXISTS "%s"`, roleName)); err != nil {
				return fmt.Errorf("failed to drop %s database role: %w", roleName, err)
			}

			return nil
		}

		if err := dropRole(getDatabaseRoleName(org, tygerServerManagedIdentityName)); err != nil {
			return err
		}
		if err := dropRole(getDatabaseRoleName(org, migrationRunnerManagedIdentityName)); err != nil {
			return err
		}
		if err := dropRole(getDatabaseRoleName(org, unqualifiedOwnersRole)); err != nil {
			return err
		}

		return nil
	})
}

// Create a tyger-owners role and grant it to the current principal and the migration runner's managed identity.
// The migration runner will grant full access to the tables it creates to this role.
func (inst *Installer) createRoles(
	ctx context.Context,
	server *armpostgresqlflexibleservers.Server,
	org *OrganizationConfig,
	currentPrincipalDisplayName string,
	tygerServerIdentity *armmsi.Identity,
	migrationRunnerIdentity *armmsi.Identity,
) error {
	log.Ctx(ctx).Info().Msg("Creating PostgreSQL roles")

	databaseScopedOwnersRole := getDatabaseRoleName(org, unqualifiedOwnersRole)

	const RoleCreateMaxRetries = 20
	var err error
	for roleCreateRetryCount := range RoleCreateMaxRetries {
		if roleCreateRetryCount > 0 {
			time.Sleep(30 * time.Second)
		}

		err = inst.executeOnDatabase(ctx, *server.Properties.FullyQualifiedDomainName, defaultDatabaseName, currentPrincipalDisplayName, func(db *sql.DB) error {
			_, err := db.Exec(fmt.Sprintf(`
			DO $$
			BEGIN
				IF NOT EXISTS (SELECT FROM pgaadauth_list_principals(false) WHERE objectId = '%s') THEN
					PERFORM pgaadauth_create_principal_with_oid('%s', '%s', 'service', false, false);
				END IF;
			END
			$$`, *tygerServerIdentity.Properties.PrincipalID, getDatabaseRoleName(org, *migrationRunnerIdentity.Name), *migrationRunnerIdentity.Properties.PrincipalID))
			if err != nil {
				return fmt.Errorf("failed to create tyger server database principal: %w", err)
			}

			_, err = db.Exec(fmt.Sprintf(`
			DO $$
			BEGIN
				IF NOT EXISTS (SELECT FROM pgaadauth_list_principals(false) WHERE objectId = '%s') THEN
					PERFORM pgaadauth_create_principal_with_oid('%s', '%s', 'service', false, false);
				END IF;
			END
			$$`, *tygerServerIdentity.Properties.PrincipalID, getDatabaseRoleName(org, *tygerServerIdentity.Name), *tygerServerIdentity.Properties.PrincipalID))
			if err != nil {
				return fmt.Errorf("failed to create tyger server database principal: %w", err)
			}

			return nil
		})

		if err == nil {
			break
		}

		if strings.Contains(err.Error(), "OID is not found in the tenant") {
			// It can take some time before the database is able to retrieve principals that have been recently created
			log.Warn().Msgf("Database role creation failed. Attempt %d/%d", roleCreateRetryCount+1, RoleCreateMaxRetries)
			continue
		}

		return err
	}

	if err != nil {
		return err
	}

	err = inst.executeOnDatabase(ctx, *server.Properties.FullyQualifiedDomainName, org.Cloud.DatabaseName, currentPrincipalDisplayName, func(db *sql.DB) error {

		_, err = db.Exec(fmt.Sprintf(`
		DO $$
		BEGIN
			IF NOT EXISTS (SELECT FROM pg_catalog.pg_roles WHERE rolname = '%s') THEN
				CREATE ROLE "%s";
			END IF;
		END
		$$`, databaseScopedOwnersRole, databaseScopedOwnersRole))
		if err != nil {
			return fmt.Errorf("failed to create %s database role: %w", databaseScopedOwnersRole, err)
		}

		if _, err := db.Exec(fmt.Sprintf(`GRANT "%s" TO "%s" WITH ADMIN TRUE`, databaseScopedOwnersRole, getDatabaseRoleName(org, *migrationRunnerIdentity.Name))); err != nil {
			return fmt.Errorf("failed to grant database role: %w", err)
		}

		if _, err := db.Exec(fmt.Sprintf(`GRANT CREATE, USAGE ON SCHEMA public TO "%s"`, getDatabaseRoleName(org, *migrationRunnerIdentity.Name))); err != nil {
			return fmt.Errorf("failed to grant database role: %w", err)
		}

		if _, err := db.Exec(fmt.Sprintf(`GRANT "%s" TO "%s"`, databaseScopedOwnersRole, currentPrincipalDisplayName)); err != nil {
			return fmt.Errorf("failed to grant database role: %w", err)
		}

		if _, err := db.Exec(fmt.Sprintf(`GRANT CREATE, USAGE ON SCHEMA public TO "%s"`, databaseScopedOwnersRole)); err != nil {
			return fmt.Errorf("failed to grant database role: %w", err)
		}

		if _, err := db.Exec(fmt.Sprintf(`GRANT SELECT, INSERT, UPDATE, DELETE ON ALL TABLES IN SCHEMA public TO "%s"`, getDatabaseRoleName(org, *tygerServerIdentity.Name))); err != nil {
			return fmt.Errorf("failed to grant database role: %w", err)
		}

		if _, err := db.Exec(fmt.Sprintf(`GRANT USAGE ON ALL SEQUENCES IN SCHEMA public TO "%s"`, getDatabaseRoleName(org, *tygerServerIdentity.Name))); err != nil {
			return fmt.Errorf("failed to grant database role: %w", err)
		}

		return nil
	})

	if err != nil {
		return err
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
	// make a deep copy of existingServer
	existingJson, err := json.Marshal(existingServer)
	if err != nil {
		panic(fmt.Sprintf("failed to marshal existing database server JSON: %v", err))
	}
	if err := json.Unmarshal(existingJson, &merged); err != nil {
		panic(fmt.Sprintf("failed to unmarshal existing database server JSON: %v", err))
	}

	tagsMatch := false
	if existingServer.Tags != nil && len(existingServer.Tags) == len(newServer.Tags) {
		tagsMatch = true
		for k, v := range newServer.Tags {
			if existingValue, ok := existingServer.Tags[k]; !ok || *existingValue != *v {
				tagsMatch = false
				break
			}
		}
	}

	if !tagsMatch {
		merged.Tags = newServer.Tags
		needsUpdate = true
	}

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

	if (newServer.Properties.Network.PublicNetworkAccess == nil) != (existingServer.Properties.Network.PublicNetworkAccess == nil) ||
		(newServer.Properties.Network.PublicNetworkAccess != nil && *newServer.Properties.Network.PublicNetworkAccess != *existingServer.Properties.Network.PublicNetworkAccess) {

		merged.Properties.Network.PublicNetworkAccess = newServer.Properties.Network.PublicNetworkAccess
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

func (inst *Installer) getDatabaseConfiguredTagKey() string {
	return fmt.Sprintf("tyger-database-configured-%s", inst.Config.EnvironmentName)
}

// Export logs to Log Analytics.
func (inst *Installer) enableDiagnosticSettings(ctx context.Context, server *armpostgresqlflexibleservers.Server) error {
	log.Ctx(ctx).Info().Msg("Enabling diagnostics on PostgreSQL server")

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

	log.Ctx(ctx).Info().Msg("Diagnostics on PostgreSQL server enabled")
	return nil
}
