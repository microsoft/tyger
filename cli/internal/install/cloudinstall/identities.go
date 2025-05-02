// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"fmt"
	"net/http"
	"slices"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

const permissionScopeValue = "Read.Write"
const tygerOwnerRoleValue = "owner"
const tygerContributorRoleValue = "contributor"

func InstallIdentities(ctx context.Context, config *AuthConfig, cred azcore.TokenCredential) error {
	serverApp, err := CreateOrUpdateServerApp(ctx, config, cred)
	if err != nil {
		return err
	}

	if _, err := GetServicePrincipalByAppId(ctx, cred, serverApp.AppId); err != nil {
		if err != errNotFound {
			return fmt.Errorf("failed to get service principal for server app: %w", err)
		}
		if _, err := CreateServicePrincipal(ctx, cred, serverApp.AppId); err != nil {
			return fmt.Errorf("failed to create service principal for server app: %w", err)
		}
	}

	cliApp, err := CreateOrUpdateCliApp(ctx, config, serverApp, cred)
	if err != nil {
		return err
	}

	if _, err := GetServicePrincipalByAppId(ctx, cred, cliApp.AppId); err != nil {
		if err != errNotFound {
			return fmt.Errorf("failed to get service principal for CLI app: %w", err)
		}
		if _, err := CreateServicePrincipal(ctx, cred, cliApp.AppId); err != nil {
			return fmt.Errorf("failed to create service principal for CLI app: %w", err)
		}
	}

	return nil
}

func CreateOrUpdateServerApp(ctx context.Context, config *AuthConfig, cred azcore.TokenCredential) (*aadApp, error) {
	app, err := GetAppByUri(ctx, cred, config.ApiAppUri)
	if err != nil {
		if err == errNotFound {
			app = &aadApp{
				IdentifierUris: []string{config.ApiAppUri},
				Api:            &aadAppApi{},
			}
		} else {
			return nil, fmt.Errorf("failed to get existing app: %w", err)
		}
	}

	app.DisplayName = valueOrDefault(app.DisplayName, "Tyger API")
	app.SignInAudience = valueOrDefault(app.SignInAudience, "AzureADMyOrg")
	app.Api.RequestedAccessTokenVersion = 1

	var ownerAppRole *aadAppRole
	if idx := slices.IndexFunc(app.AppRoles, func(role *aadAppRole) bool {
		return role.Value == tygerOwnerRoleValue
	}); idx != -1 {
		ownerAppRole = app.AppRoles[idx]
	} else {
		ownerAppRole = &aadAppRole{
			Id:    uuid.NewString(),
			Value: tygerOwnerRoleValue,
		}
		app.AppRoles = append(app.AppRoles, ownerAppRole)
	}

	ownerAppRole.Description = valueOrDefault(ownerAppRole.Description, "Allows managing all resources in Tyger.")
	ownerAppRole.DisplayName = valueOrDefault(ownerAppRole.DisplayName, "Tyger Owner")
	ownerAppRole.AllowedMemberTypes = []string{"Application", "User"}
	ownerAppRole.IsEnabled = true

	var contributorAppRole *aadAppRole
	if idx := slices.IndexFunc(app.AppRoles, func(role *aadAppRole) bool {
		return role.Value == tygerContributorRoleValue
	}); idx != -1 {
		contributorAppRole = app.AppRoles[idx]
	} else {
		contributorAppRole = &aadAppRole{
			Id:    uuid.NewString(),
			Value: tygerContributorRoleValue,
		}
		app.AppRoles = append(app.AppRoles, contributorAppRole)
	}
	contributorAppRole.Description = valueOrDefault(contributorAppRole.Description, "Allows creating and updating resources in Tyger.")
	contributorAppRole.DisplayName = valueOrDefault(contributorAppRole.DisplayName, "Tyger Contributor")
	contributorAppRole.AllowedMemberTypes = []string{"Application", "User"}
	contributorAppRole.IsEnabled = true

	var scope *aadAppAuth2PermissionScope
	if idx := slices.IndexFunc(app.Api.Oauth2PermissionScopes, func(scope *aadAppAuth2PermissionScope) bool {
		return scope.Value == permissionScopeValue
	}); idx != -1 {
		scope = app.Api.Oauth2PermissionScopes[idx]
	} else {
		scope = &aadAppAuth2PermissionScope{
			Id:    uuid.NewString(),
			Value: permissionScopeValue,
		}
		app.Api.Oauth2PermissionScopes = append(app.Api.Oauth2PermissionScopes, scope)
	}

	scope.AdminConsentDescription = valueOrDefault(scope.AdminConsentDescription, "Allows the app to access Tyger API on behalf of the signed-in user.")
	scope.AdminConsentDisplayName = valueOrDefault(scope.AdminConsentDisplayName, "Access Tyger API")
	scope.IsEnabled = true
	scope.Type = "User"
	scope.UserConsentDescription = valueOrDefault(scope.UserConsentDescription, "Allows the app to access Tyger API on your behalf.")
	scope.UserConsentDisplayName = valueOrDefault(scope.UserConsentDisplayName, "Access Tyger API")

	if err == errNotFound {
		log.Ctx(ctx).Info().Msgf("Creating app %s", config.ApiAppUri)
		err = executeGraphCall(ctx, cred, http.MethodPost, "https://graph.microsoft.com/beta/applications", app, &app)
	} else {
		log.Ctx(ctx).Info().Msgf("Updating app %s", config.ApiAppUri)
		err = executeGraphCall(ctx, cred, http.MethodPatch, fmt.Sprintf("https://graph.microsoft.com/beta/applications/%s", app.Id), app, nil)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create or update app: %w", err)
	}

	return app, nil
}

func CreateOrUpdateCliApp(ctx context.Context, config *AuthConfig, serverApp *aadApp, cred azcore.TokenCredential) (*aadApp, error) {
	var permissionScopeId string
	if permissionScopeIndex := slices.IndexFunc(serverApp.Api.Oauth2PermissionScopes, func(scope *aadAppAuth2PermissionScope) bool {
		return scope.Value == permissionScopeValue
	}); permissionScopeIndex != -1 {
		permissionScopeId = serverApp.Api.Oauth2PermissionScopes[permissionScopeIndex].Id
	} else {
		panic("permission scope should exist on server app")
	}

	app, err := GetAppByUri(ctx, cred, config.CliAppUri)
	if err != nil {
		if err == errNotFound {
			app = &aadApp{
				IdentifierUris:         []string{config.CliAppUri},
				RequiredResourceAccess: []*aadAppRequiredResourceAccess{},
			}
		} else {
			return nil, fmt.Errorf("failed to get existing app: %w", err)
		}
	}

	app.DisplayName = valueOrDefault(app.DisplayName, "Tyger CLI")
	app.SignInAudience = valueOrDefault(app.SignInAudience, "AzureADMyOrg")
	app.IsFallbackPublicClient = true
	app.PublicClient = &aadAppPublicClient{
		RedirectUris: []string{"http://localhost"},
	}

	var requiredResourceAccess *aadAppRequiredResourceAccess
	if idx := slices.IndexFunc(app.RequiredResourceAccess, func(resourceAccess *aadAppRequiredResourceAccess) bool {
		return resourceAccess.ResourceAppId == serverApp.Id
	}); idx != -1 {
		requiredResourceAccess = app.RequiredResourceAccess[idx]
	} else {
		requiredResourceAccess = &aadAppRequiredResourceAccess{
			ResourceAppId: serverApp.Id,
		}
		app.RequiredResourceAccess = append(app.RequiredResourceAccess, requiredResourceAccess)
	}

	requiredResourceAccess.ResourceAccess = []*aadAppResourceAccess{
		{
			Id:   permissionScopeId,
			Type: "Scope",
		},
	}

	if err == errNotFound {
		log.Ctx(ctx).Info().Msgf("Creating app %s", config.CliAppUri)
		err = executeGraphCall(ctx, cred, http.MethodPost, "https://graph.microsoft.com/beta/applications", app, &app)
	} else {
		log.Ctx(ctx).Info().Msgf("Updating app %s", config.CliAppUri)
		err = executeGraphCall(ctx, cred, http.MethodPatch, fmt.Sprintf("https://graph.microsoft.com/beta/applications/%s", app.Id), app, nil)
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create or update app: %w", err)
	}

	return app, nil
}

func valueOrDefault(value, defaultValue string) string {
	if value == "" {
		return defaultValue
	}
	return value
}
