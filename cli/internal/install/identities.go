package install

import (
	"context"
	"fmt"
)

func InstallIdentities(ctx context.Context) error {
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

	serverObjectId, err := CreateOrUpdateAppByUri(ctx, serverApp)
	if err != nil {
		return fmt.Errorf("failed to create or update server app: %w", err)
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
	}

	if _, err := CreateOrUpdateAppByUri(ctx, cliApp); err != nil {
		return fmt.Errorf("failed to create or update CLI app: %w", err)
	}

	return nil
}
