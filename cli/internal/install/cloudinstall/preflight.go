// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v2"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v7"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources/v3"
	"github.com/IGLOU-EU/go-wildcard/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
)

func (inst *Installer) preflightCheck(ctx context.Context) error {
	if err := inst.checkRPsRegistered(ctx); err != nil {
		return err
	}

	if err := inst.checkRbac(ctx); err != nil {
		return err
	}

	if err := inst.checkNoLegacyPrivateLinkResourceGroups(ctx); err != nil {
		return err
	}

	return nil
}

func (inst *Installer) checkRPsRegistered(ctx context.Context) error {
	providersClient, err := armresources.NewProvidersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create providers client: %w", err)
	}

	requiredProviders := []string{
		"Microsoft.Storage",
		"Microsoft.ContainerService",
	}

	if inst.Config.Cloud.LogAnalyticsWorkspace != nil {
		requiredProviders = append(requiredProviders, "Microsoft.OperationsManagement", "Microsoft.OperationalInsights")
	}

	for _, p := range requiredProviders {
		if err := inst.checkRPRegistered(ctx, providersClient, p); err != nil {
			return err
		}
	}

	return nil
}

func (inst *Installer) checkRPRegistered(ctx context.Context, providersClient *armresources.ProvidersClient, providerNamespace string) error {
	rp, err := providersClient.Get(ctx, providerNamespace, nil)
	if err != nil {
		return fmt.Errorf("failed to get %s provider: %w", providerNamespace, err)
	}

	if *rp.RegistrationState == "NotRegistered" || *rp.RegistrationState == "Unregistered" {
		log.Ctx(ctx).Info().Msgf("Registering %s provider", providerNamespace)
		_, err := providersClient.Register(ctx, providerNamespace, nil)
		if err != nil {
			return fmt.Errorf("failed to register %s provider: %w", providerNamespace, err)
		}
	}

	return nil
}

func (inst *Installer) checkRbac(ctx context.Context) error {
	tokenResponse, err := inst.Credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{cloud.AzurePublic.Services[cloud.ResourceManager].Audience}})
	if err != nil {
		return err
	}

	claims := jwt.MapClaims{}
	_, _, err = jwt.NewParser().ParseUnverified(tokenResponse.Token, claims)
	if err != nil {
		return fmt.Errorf("failed to parse token: %w", err)
	}
	principalId := claims["oid"].(string)

	assignmentClient, err := armauthorization.NewRoleAssignmentsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
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

	roleDefsClient, err := armauthorization.NewRoleDefinitionsClient(inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create role definitions client: %w", err)
	}

	roleDefs := make(map[string]armauthorization.RoleDefinition)

	roleDefsPager := roleDefsClient.NewListPager(fmt.Sprintf("/subscriptions/%s", inst.Config.Cloud.SubscriptionID), nil)
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

	for _, org := range inst.Config.Organizations {

		storageScopes := []string{
			fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", inst.Config.Cloud.SubscriptionID, org.Cloud.ResourceGroup, org.Cloud.Storage.Logs.Name),
		}

		for _, bufferAccount := range org.Cloud.Storage.Buffers {
			storageScopes = append(storageScopes, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Storage/storageAccounts/%s", inst.Config.Cloud.SubscriptionID, org.Cloud.ResourceGroup, bufferAccount.Name))
		}

		for _, scope := range storageScopes {
			for _, a := range storageAccountRequiredActions {
				if err := checkAccess(ctx, scope, a, roleAssignments, roleDefs); err != nil {
					hasErr = true
				}
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
	for _, c := range inst.Config.Cloud.Compute.Clusters {
		aksScopes = append(aksScopes, fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.ContainerService/managedClusters/%s", inst.Config.Cloud.SubscriptionID, inst.Config.Cloud.ResourceGroup, c.Name))
	}

	for _, scope := range aksScopes {
		for _, a := range aksRequiredActions {
			if err := checkAccess(ctx, scope, a, roleAssignments, roleDefs); err != nil {
				hasErr = true
			}
		}
	}

	// Attached container registries
	attachedRegistryRequiredActions := []string{
		"Microsoft.Authorization/roleAssignments/write",
	}
	mirrorRegistryRequiredActions := []string{
		"Microsoft.ContainerRegistry/registries/read",
		"Microsoft.ContainerRegistry/registries/write",
		"Microsoft.ContainerRegistry/registries/importImage/action",
	}
	registryMirror := inst.Config.Cloud.GetContainerRegistryMirrorName()
	for _, acr := range inst.Config.Cloud.containerRegistriesForClusterAccess() {
		id, err := getContainerRegistryId(ctx, acr, inst.Config.Cloud.SubscriptionID, inst.Credential)
		if err != nil {
			return err
		}

		for _, a := range attachedRegistryRequiredActions {
			if err := checkAccess(ctx, id, a, roleAssignments, roleDefs); err != nil {
				hasErr = true
			}
		}

		if acr == registryMirror {
			for _, a := range mirrorRegistryRequiredActions {
				if err := checkAccess(ctx, id, a, roleAssignments, roleDefs); err != nil {
					hasErr = true
				}
			}
		}
	}

	// Log Analytics
	if inst.Config.Cloud.LogAnalyticsWorkspace != nil {
		scope := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.OperationalInsights/workspaces/%s", inst.Config.Cloud.SubscriptionID, inst.Config.Cloud.LogAnalyticsWorkspace.ResourceGroup, inst.Config.Cloud.LogAnalyticsWorkspace.Name)
		laRequiredActions := []string{
			"Microsoft.ManagedIdentity/userAssignedIdentities/assign/action",
			"Microsoft.OperationalInsights/workspaces/read",
			"Microsoft.OperationalInsights/workspaces/sharedkeys/read",
			"Microsoft.OperationsManagement/solutions/read",
			"Microsoft.OperationsManagement/solutions/write",
		}
		for _, a := range laRequiredActions {
			if err := checkAccess(ctx, scope, a, roleAssignments, roleDefs); err != nil {
				hasErr = true
			}
		}
	}

	if hasErr {
		return install.ErrAlreadyLoggedError
	}

	return nil
}

// This is not meant to be a complete check and may result in false positives. For instance, we are ignoring conditions and deny assignments.
func checkAccess(ctx context.Context, scope, permission string, roleAssignments []armauthorization.RoleAssignment, roleDefs map[string]armauthorization.RoleDefinition) error {
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

	log.Ctx(ctx).Error().Str("permission", permission).Str("scope", scope).Msg("Missing required permission")
	return install.ErrAlreadyLoggedError
}

func permissionMatches(required, actual string) bool {
	return wildcard.Match(actual, required)
}

// checkNoLegacyPrivateLinkResourceGroups checks whether any legacy dedicated
// private-link resource groups (pattern "<rg>-privatelink-<subnetRG>-<vnet>")
// still exist. If they do, the deployment is aborted because private-link
// resources now live in the shared or per-organization resource groups.
func (inst *Installer) checkNoLegacyPrivateLinkResourceGroups(ctx context.Context) error {
	if !inst.Config.Cloud.PrivateNetworking {
		return nil
	}

	rgClient, err := armresources.NewResourceGroupsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create resource groups client: %w", err)
	}

	for _, cluster := range inst.Config.Cloud.Compute.Clusters {
		if cluster.ExistingSubnet == nil {
			continue
		}

		legacyRG := fmt.Sprintf("%s-privatelink-%s-%s",
			inst.Config.Cloud.ResourceGroup,
			cluster.ExistingSubnet.ResourceGroup,
			cluster.ExistingSubnet.VNetName)

		if _, err := rgClient.Get(ctx, legacyRG, nil); err == nil {
			var msg strings.Builder
			msg.WriteString(fmt.Sprintf(
				"legacy private-link resource group '%s' still exists. "+
					"Private-link resources are now placed in the shared and per-organization resource groups. "+
					"AKS does not allow changing the private DNS zone resource group on an existing cluster, "+
					"so the clusters must be deleted and re-created.\n\n"+
					"To proceed, run the following commands and then re-deploy:\n\n",
				legacyRG))

			aksClient, err := armcontainerservice.NewManagedClustersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
			if err != nil {
				return fmt.Errorf("failed to create AKS client: %w", err)
			}

			for _, c := range inst.Config.Cloud.Compute.Clusters {
				if _, err := aksClient.Get(ctx, inst.Config.Cloud.ResourceGroup, c.Name, nil); err == nil {
					msg.WriteString(fmt.Sprintf("  az aks delete -n %s -g %s --subscription %s --yes\n",
						c.Name, inst.Config.Cloud.ResourceGroup, inst.Config.Cloud.SubscriptionID))
				}
			}

			msg.WriteString(fmt.Sprintf("\n  az group delete -n %s --subscription %s --yes\n",
				legacyRG, inst.Config.Cloud.SubscriptionID))

			msg.WriteString("\nNote: this will cause a service outage until the re-deployment completes.")

			return fmt.Errorf("%s", msg.String())
		}
	}

	return nil
}
