package install

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

func InstallIdentities(ctx context.Context, cred azcore.TokenCredential) error {
	const permissionScopeId = "6291652f-fd9d-4a31-aa5f-87306c599bb6"
	config := GetConfigFromContext(ctx)

	serverApp := aadApp{
		DisplayName:    "Tyger API",
		IdentifierUris: []string{config.Api.Auth.ApiAppUri},
		SignInAudience: "AzureADMyOrg",
		Api: aadAppApi{
			RequestedAccessTokenVersion: 1,
			Oauth2PermissionScopes: []aadAppAuth2PermissionScope{
				{
					Id:                      permissionScopeId,
					AdminConsentDescription: "Allows the app to access Tyger API on behalf of the signed-in user.",
					AdminConsentDisplayName: "Access Tyger API",
					IsEnabled:               true,
					Type:                    "User",
					UserConsentDescription:  "Allows the app to access Tyger API on your behalf.",
					UserConsentDisplayName:  "Access Tyger API",
					Value:                   "Read.Write",
				},
			},
		},
	}

	serverObjectId, err := CreateOrUpdateAppByUri(ctx, cred, serverApp)
	if err != nil {
		return fmt.Errorf("failed to create or update server app: %w", err)
	}

	serverApp, err = GetAppByUri(ctx, cred, config.Api.Auth.ApiAppUri)
	if err != nil {
		return fmt.Errorf("failed to get server app: %w", err)
	}

	if _, err := GetServicePrincipalByAppId(ctx, cred, serverApp.AppId); err != nil {
		if err != errNotFound {
			return fmt.Errorf("failed to get service principal for server app: %w", err)
		}
		if _, err := CreateServicePrincipal(ctx, cred, serverApp.AppId); err != nil {
			return fmt.Errorf("failed to create service principal for server app: %w", err)
		}
	}

	cliApp := aadApp{
		DisplayName:    "Tyger CLI",
		IdentifierUris: []string{config.Api.Auth.CliAppUri},
		RequiredResourceAccess: []aadAppRequiredResourceAccess{
			{
				ResourceAppId: serverObjectId,
				ResourceAccess: []aadAppResourceAccess{
					{
						Id:   permissionScopeId,
						Type: "Scope",
					},
				},
			},
		},
		IsFallbackPublicClient: true,
		PublicClient: &aadAppPublicClient{
			RedirectUris: []string{
				"http://localhost",
			},
		},
		SignInAudience: "AzureADMyOrg",
	}

	if _, err := CreateOrUpdateAppByUri(ctx, cred, cliApp); err != nil {
		return fmt.Errorf("failed to create or update CLI app: %w", err)
	}

	cliApp, err = GetAppByUri(ctx, cred, config.Api.Auth.CliAppUri)
	if err != nil {
		return fmt.Errorf("failed to get CLI app: %w", err)
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
