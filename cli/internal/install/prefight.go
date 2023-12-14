package install

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/IGLOU-EU/go-wildcard/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/rs/zerolog/log"
)

func preflightCheck(ctx context.Context) error {
	config := GetConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	if err := checkRPsRegistered(ctx, config, cred); err != nil {
		return err
	}

	if err := checkRbac(ctx, config, cred); err != nil {
		return err
	}

	return nil
}

func checkRPsRegistered(ctx context.Context, config *EnvironmentConfig, cred azcore.TokenCredential) error {
	providersClient, err := armresources.NewProvidersClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create providers client: %w", err)
	}

	requiredProviders := []string{
		"Microsoft.Storage",
		"Microsoft.ContainerService",
	}

	if config.Cloud.LogAnalyticsWorkspace != nil {
		requiredProviders = append(requiredProviders, "Microsoft.OperationsManagement", "Microsoft.OperationalInsights")
	}

	for _, p := range requiredProviders {
		if err := checkRPRegistered(ctx, providersClient, p); err != nil {
			return err
		}
	}

	return nil
}

func checkRPRegistered(ctx context.Context, providersClient *armresources.ProvidersClient, providerNamespace string) error {
	rp, err := providersClient.Get(ctx, providerNamespace, nil)
	if err != nil {
		return fmt.Errorf("failed to get %s provider: %w", providerNamespace, err)
	}

	if *rp.RegistrationState == "NotRegistered" || *rp.RegistrationState == "Unregistered" {
		log.Info().Msgf("Registering %s provider", providerNamespace)
		_, err := providersClient.Register(ctx, providerNamespace, nil)
		if err != nil {
			return fmt.Errorf("failed to register %s provider: %w", providerNamespace, err)
		}
	}

	return nil
}

func checkRbac(ctx context.Context, config *EnvironmentConfig, cred azcore.TokenCredential) error {
	tokenResponse, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{cloud.AzurePublic.Services[cloud.ResourceManager].Audience}})
	if err != nil {
		return err
	}

	claims := jwt.MapClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(tokenResponse.Token, claims)
	if err != nil {
		return fmt.Errorf("failed to parse token: %w", err)
	}
	principalId := claims["oid"].(string)

	assignmentClient, err := armauthorization.NewRoleAssignmentsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create role assignment client: %w", err)
	}

	assignmentsPager := assignmentClient.NewListForSubscriptionPager(&armauthorization.RoleAssignmentsClientListForSubscriptionOptions{
		Filter: Ptr(fmt.Sprintf("assignedTo('%s')", principalId)),
	})

	roleAssignments := make([]armauthorization.RoleAssignment, 0)

	for assignmentsPager.More() {
		page, err := assignmentsPager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to get role assignments: %w", err)
		}
		for _, ra := range page.RoleAssignmentListResult.Value {
			roleAssignments = append(roleAssignments, *ra)
		}
	}

	roleDefsClient, err := armauthorization.NewRoleDefinitionsClient(cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create role definitions client: %w", err)
	}

	roleDefs := make(map[string]armauthorization.RoleDefinition)

	roleDefsPager := roleDefsClient.NewListPager(fmt.Sprintf("/subscriptions/%s", config.Cloud.SubscriptionID), nil)
	for roleDefsPager.More() {
		page, err := roleDefsPager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to get role definitions: %w", err)
		}
		for _, rd := range page.RoleDefinitionListResult.Value {
			roleDefs[*rd.ID] = *rd
		}
	}

	hasErr := false

	// storage
	storageAccountRequiredActions := []string{
		"Microsoft.Storage/storageAccounts/listKeys/action",
		"Microsoft.Storage/storageAccounts/write",
	}

	storageScopes := []string{
		fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", config.Cloud.SubscriptionID, config.Cloud.ResourceGroup, config.Cloud.Storage.Logs.Name),
	}

	for _, bufferAccount := range config.Cloud.Storage.Buffers {
		storageScopes = append(storageScopes, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", config.Cloud.SubscriptionID, config.Cloud.ResourceGroup, bufferAccount.Name))
	}

	for _, scope := range storageScopes {
		for _, a := range storageAccountRequiredActions {
			if err := checkAccess(scope, a, roleAssignments, roleDefs); err != nil {
				hasErr = true
			}
		}
	}

	// AKS
	aksRequiredActions := []string{
		"Microsoft.ContainerService/managedClusters/listClusterAdminCredential/action",
		"Microsoft.ContainerService/managedClusters/listClusterUserCredential/action",
		"Microsoft.ContainerService/managedClusters/write",
	}

	aksScopes := make([]string, 0)
	for _, c := range config.Cloud.Compute.Clusters {
		aksScopes = append(aksScopes, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerService/managedClusters/%s", config.Cloud.SubscriptionID, config.Cloud.ResourceGroup, c.Name))
	}

	for _, scope := range aksScopes {
		for _, a := range aksRequiredActions {
			if err := checkAccess(scope, a, roleAssignments, roleDefs); err != nil {
				hasErr = true
			}
		}
	}

	// Attached container registries
	for _, acr := range config.Cloud.Compute.PrivateContainerRegistries {
		id, err := getContainerRegistryId(ctx, acr, config.Cloud.SubscriptionID, cred)
		if err != nil {
			return err
		}

		if err := checkAccess(id, "Microsoft.Authorization/roleAssignments/write", roleAssignments, roleDefs); err != nil {
			hasErr = true
		}
	}

	// Log Analytics
	if config.Cloud.LogAnalyticsWorkspace != nil {
		scope := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.OperationalInsights/workspaces/%s", config.Cloud.SubscriptionID, config.Cloud.LogAnalyticsWorkspace.ResourceGroup, config.Cloud.LogAnalyticsWorkspace.Name)
		laRequiredActions := []string{
			"Microsoft.ManagedIdentity/userAssignedIdentities/assign/action",
			"Microsoft.OperationalInsights/workspaces/read",
			"Microsoft.OperationalInsights/workspaces/sharedkeys/read",
			"Microsoft.OperationsManagement/solutions/read",
			"Microsoft.OperationsManagement/solutions/write",
		}
		for _, a := range laRequiredActions {
			if err := checkAccess(scope, a, roleAssignments, roleDefs); err != nil {
				hasErr = true
			}
		}
	}

	if hasErr {
		return ErrAlreadyLoggedError
	}

	return nil
}

// This is not meant to be a complete check and may result in false positives. For instance, we are ignoring conditions and deny assignments.
func checkAccess(scope, permission string, roleAssignments []armauthorization.RoleAssignment, roleDefs map[string]armauthorization.RoleDefinition) error {
	for _, ra := range roleAssignments {
		if !strings.HasPrefix(strings.ToLower(scope), strings.ToLower(*ra.Properties.Scope)) {
			// This role assignment is not applicable
			continue
		}

		roleDef, ok := roleDefs[*ra.Properties.RoleDefinitionID]
		if !ok {
			log.Debug().Msgf("role definition '%s' not found", *ra.Properties.RoleDefinitionID)
			continue
		}

		for _, p := range roleDef.Properties.Permissions {
			for _, a := range p.Actions {
				if permissionMatches(permission, *a) {
					excluded := false
					for _, na := range p.NotActions {
						if permissionMatches(permission, *na) {
							excluded = true
							break
						}
					}
					if !excluded {
						return nil
					}
				}
			}
		}
	}

	log.Error().Str("permission", permission).Str("scope", scope).Msg("Missing required permission")
	return ErrAlreadyLoggedError
}

func permissionMatches(required, actual string) bool {
	return wildcard.Match(actual, required)
}
