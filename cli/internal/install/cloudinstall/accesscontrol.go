// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"text/template"

	_ "embed"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/google/uuid"
	"github.com/microsoft/tyger/cli/internal/templatefunctions"
	"github.com/rs/zerolog/log"
)

const permissionScopeValue = "Read.Write"
const tygerOwnerRoleValue = "owner"
const tygerContributorRoleValue = "contributor"

//go:embed access-control-pretty.tpl
var prettyPrintRbacTemplate string

func CompleteAcessControlConfig(ctx context.Context, config *AccessControlConfig, cred azcore.TokenCredential) error {
	serverApp, err := GetAppByAppIdOrUri(ctx, cred, config.ApiAppId, config.ApiAppUri)
	if err != nil {
		if err != errNotFound {
			return fmt.Errorf("failed to get api app: %w", err)
		}
	}

	if serverApp != nil {
		config.ApiAppId = serverApp.AppId
		if config.ApiAppUri == "" {
			config.ApiAppUri = serverApp.IdentifierUris[0]
		}
	}

	cliApp, err := GetAppByAppIdOrUri(ctx, cred, config.CliAppId, config.CliAppUri)
	if err != nil {
		if err != errNotFound {
			return fmt.Errorf("failed to get cli app: %w", err)
		}
	}

	if cliApp != nil {
		config.CliAppId = cliApp.AppId
		if config.CliAppUri == "" {
			config.CliAppUri = cliApp.IdentifierUris[0]
		}
	}

	if serverApp == nil {
		return nil
	}

	config.RoleAssignments, err = getRbacAssignments(ctx, cred, config)
	if err != nil {
		return fmt.Errorf("failed to get role assignments: %w", err)
	}

	return nil
}

func ApplyAccessControlConfig(ctx context.Context, accessControlConfig *AccessControlConfig, cred azcore.TokenCredential) (*AccessControlConfig, error) {
	if err := installIdentities(ctx, accessControlConfig, cred); err != nil {
		return nil, err
	}

	normalizedAssignments, err := ApplyRbacAssignments(ctx, cred, accessControlConfig)
	if err != nil {
		return nil, err
	}

	accessControlConfig.RoleAssignments = normalizedAssignments
	return accessControlConfig, nil
}

func installIdentities(ctx context.Context, accessControlConfig *AccessControlConfig, cred azcore.TokenCredential) error {
	serverApp, err := createOrUpdateServerApp(ctx, accessControlConfig, cred)
	if err != nil {
		return err
	}

	accessControlConfig.ApiAppId = serverApp.AppId
	if accessControlConfig.ApiAppUri == "" {
		accessControlConfig.ApiAppUri = serverApp.IdentifierUris[0]
	}

	if _, err := GetServicePrincipalByAppId(ctx, cred, serverApp.AppId); err != nil {
		if err != errNotFound {
			return fmt.Errorf("failed to get service principal for API app: %w", err)
		}
		if _, err := CreateServicePrincipal(ctx, cred, serverApp.AppId); err != nil {
			return fmt.Errorf("failed to create service principal for API app: %w", err)
		}
	}

	cliApp, err := createOrUpdateCliApp(ctx, accessControlConfig, serverApp, cred)
	if err != nil {
		return err
	}

	accessControlConfig.CliAppId = cliApp.AppId
	if accessControlConfig.CliAppUri == "" {
		accessControlConfig.CliAppUri = cliApp.IdentifierUris[0]
	}

	if _, err := GetServicePrincipalByAppId(ctx, cred, cliApp.AppId); err != nil {
		if err != errNotFound {
			return fmt.Errorf("failed to get service principal for CLI app: %w", err)
		}
		if _, err := CreateServicePrincipal(ctx, cred, cliApp.AppId); err != nil {
			return fmt.Errorf("failed to create service principal for CLI app: %w", err)
		}
	}

	if err := addCliAsPreAuthorizedApp(ctx, serverApp, cliApp, cred); err != nil {
		return fmt.Errorf("failed to add CLI app as pre-authorized app: %w", err)
	}

	return nil
}

func createOrUpdateServerApp(ctx context.Context, accessControlConfig *AccessControlConfig, cred azcore.TokenCredential) (*aadApp, error) {
	if accessControlConfig.ApiAppId == "" && accessControlConfig.ApiAppUri == "" {
		return nil, errors.New("`apiAppUri` must be set")
	}

	app, err := GetAppByAppIdOrUri(ctx, cred, accessControlConfig.ApiAppId, accessControlConfig.ApiAppUri)
	if err != nil {
		if err == errNotFound {
			app = &aadApp{
				IdentifierUris: []string{accessControlConfig.ApiAppUri},
				Api:            &aadAppApi{},
			}
		} else {
			return nil, fmt.Errorf("error getting existing server app: %w", err)
		}
	}

	initialAppBytes, _ := json.Marshal(app)

	app.DisplayName = valueOrDefault(app.DisplayName, fmt.Sprintf("Tyger API (%s)", accessControlConfig.ApiAppUri))
	app.SignInAudience = valueOrDefault(app.SignInAudience, "AzureADMyOrg")
	if app.Api.RequestedAccessTokenVersion == 0 {
		app.Api.RequestedAccessTokenVersion = 2
	}
	app.ServiceManagementReference = accessControlConfig.ServiceManagementReference

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
		log.Ctx(ctx).Info().Msgf("Creating app %s", accessControlConfig.ApiAppUri)
		err = executeGraphCall(ctx, cred, http.MethodPost, "https://graph.microsoft.com/beta/applications", app, &app)
	} else {
		updatedAppBytes, _ := json.Marshal(app)
		if string(initialAppBytes) == string(updatedAppBytes) {
			log.Ctx(ctx).Info().Msgf("No changes detected for app %s, skipping update", accessControlConfig.ApiAppUri)
		} else {
			log.Ctx(ctx).Info().Msgf("Updating app %s", accessControlConfig.ApiAppUri)
			err = executeGraphCall(ctx, cred, http.MethodPatch, fmt.Sprintf("https://graph.microsoft.com/beta/applications/%s", app.Id), app, nil)
		}
	}

	if err != nil {
		return nil, fmt.Errorf("failed to create or update app: %w", err)
	}

	return app, nil
}

func addCliAsPreAuthorizedApp(ctx context.Context, serverApp, cliApp *aadApp, cred azcore.TokenCredential) error {
	var scopeId string
	if scopeIndex := slices.IndexFunc(serverApp.Api.Oauth2PermissionScopes, func(scope *aadAppAuth2PermissionScope) bool {
		return scope.Value == permissionScopeValue
	}); scopeIndex != -1 {
		scopeId = serverApp.Api.Oauth2PermissionScopes[scopeIndex].Id
	} else {
		return fmt.Errorf("permission scope %s not found on server app", permissionScopeValue)
	}

	var preauthorizedApp *aadAppPreAuthorizedApplication
	if idx := slices.IndexFunc(serverApp.Api.PreAuthorizedApplications, func(app *aadAppPreAuthorizedApplication) bool {
		return app.AppId == cliApp.AppId
	}); idx != -1 {
		preauthorizedApp = serverApp.Api.PreAuthorizedApplications[idx]
	} else {
		preauthorizedApp = &aadAppPreAuthorizedApplication{
			AppId:         cliApp.AppId,
			PermissionIds: []string{},
		}
		serverApp.Api.PreAuthorizedApplications = append(serverApp.Api.PreAuthorizedApplications, preauthorizedApp)
	}

	if slices.Index(preauthorizedApp.PermissionIds, scopeId) == -1 {
		preauthorizedApp.PermissionIds = append(preauthorizedApp.PermissionIds, scopeId)
		log.Ctx(ctx).Info().Msgf("Adding CLI app %s as pre-authorized app for server app %s", cliApp.AppId, serverApp.AppId)
		err := executeGraphCall(ctx, cred, http.MethodPatch, fmt.Sprintf("https://graph.microsoft.com/beta/applications/%s", serverApp.Id), serverApp, nil)
		if err != nil {
			return fmt.Errorf("failed to add CLI app as pre-authorized app: %w", err)
		}
	}

	return nil
}

func createOrUpdateCliApp(ctx context.Context, accessControlConfig *AccessControlConfig, serverApp *aadApp, cred azcore.TokenCredential) (*aadApp, error) {
	if accessControlConfig.CliAppId == "" && accessControlConfig.CliAppUri == "" {
		return nil, errors.New("`cliAppUri` must be set")
	}

	app, err := GetAppByAppIdOrUri(ctx, cred, accessControlConfig.CliAppId, accessControlConfig.CliAppUri)
	if err != nil {
		if err == errNotFound {
			app = &aadApp{
				IdentifierUris:         []string{accessControlConfig.CliAppUri},
				RequiredResourceAccess: []*aadAppRequiredResourceAccess{},
			}
		} else {
			return nil, fmt.Errorf("error getting existing CLI app: %w", err)
		}
	}

	initialAppBytes, _ := json.Marshal(app)

	app.DisplayName = valueOrDefault(app.DisplayName, fmt.Sprintf("Tyger CLI (%s)", accessControlConfig.CliAppUri))
	app.SignInAudience = valueOrDefault(app.SignInAudience, "AzureADMyOrg")
	app.IsFallbackPublicClient = true
	app.PublicClient = &aadAppPublicClient{
		RedirectUris: []string{"http://localhost"},
	}

	app.ServiceManagementReference = accessControlConfig.ServiceManagementReference

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

	var permissionScopeId string
	if permissionScopeIndex := slices.IndexFunc(serverApp.Api.Oauth2PermissionScopes, func(scope *aadAppAuth2PermissionScope) bool {
		return scope.Value == permissionScopeValue
	}); permissionScopeIndex != -1 {
		permissionScopeId = serverApp.Api.Oauth2PermissionScopes[permissionScopeIndex].Id
	} else {
		panic("permission scope should exist on server app")
	}

	requiredResourceAccess.ResourceAccess = []*aadAppResourceAccess{
		{
			Id:   permissionScopeId,
			Type: "Scope",
		},
	}

	if err == errNotFound {
		log.Ctx(ctx).Info().Msgf("Creating app %s", accessControlConfig.CliAppUri)
		err = executeGraphCall(ctx, cred, http.MethodPost, "https://graph.microsoft.com/beta/applications", app, &app)
	} else {
		updatedAppBytes, _ := json.Marshal(app)
		if string(initialAppBytes) == string(updatedAppBytes) {
			log.Ctx(ctx).Info().Msgf("No changes detected for app %s, skipping update", accessControlConfig.CliAppUri)
		} else {
			log.Ctx(ctx).Info().Msgf("Updating app %s", accessControlConfig.CliAppUri)
			err = executeGraphCall(ctx, cred, http.MethodPatch, fmt.Sprintf("https://graph.microsoft.com/beta/applications/%s", app.Id), app, nil)
		}
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

func getRbacAssignments(ctx context.Context, cred azcore.TokenCredential, accessControlConfig *AccessControlConfig) (*TygerRbacRoleAssignments, error) {
	serverSp, err := GetServicePrincipalByUri(ctx, cred, accessControlConfig.ApiAppUri)
	if err != nil {
		if err == errNotFound {
			return nil, errors.New("run `tyger apply -f` first to create the service principals")
		}

		return nil, err
	}

	roleAssignments, err := getAssignments(ctx, cred, serverSp)
	if err != nil {
		return nil, fmt.Errorf("failed to get role assignments: %w", err)
	}

	return roleAssignments, nil
}

type roleIds struct {
	ownerRoleId       string
	contributorRoleId string
}

func getRoleIds(serverSp *aadServicePrincipal) (roleIds, error) {
	roleIds := roleIds{}
	for _, role := range serverSp.AppRoles {
		if role.Value == tygerOwnerRoleValue {
			roleIds.ownerRoleId = role.Id
		} else if role.Value == tygerContributorRoleValue {
			roleIds.contributorRoleId = role.Id
		}
	}

	if roleIds.ownerRoleId == "" || roleIds.contributorRoleId == "" {
		return roleIds, errors.New("run `tyger access-control apply` first to create the app roles")
	}

	return roleIds, nil
}

func getAssignments(ctx context.Context, cred azcore.TokenCredential, serverSp *aadServicePrincipal) (*TygerRbacRoleAssignments, error) {
	roleIds, err := getRoleIds(serverSp)
	if err != nil {
		return nil, err
	}

	type responseType struct {
		NextLink string                 `json:"@odata.nextLink"`
		Value    []aadAppRoleAssignment `json:"value"`
	}

	assignments := &TygerRbacRoleAssignments{
		Owner:       []TygerRbacRoleAssignment{},
		Contributor: []TygerRbacRoleAssignment{},
	}

	for url := fmt.Sprintf("https://graph.microsoft.com/v1.0/servicePrincipals/%s/appRoleAssignedTo", serverSp.Id); url != ""; {
		response := responseType{}
		if err := executeGraphCall(ctx, cred, http.MethodGet, url, nil, &response); err != nil {
			return nil, err
		}

		for _, assignment := range response.Value {
			principal := Principal{
				Kind:        PrincipalKind(assignment.PrincipalType),
				ObjectId:    assignment.PrincipalId,
				DisplayName: assignment.PrincipalDisplayName,
			}

			if principal.Kind == PrincipalKindUser {
				var err error
				principal.UserPrincipalName, err = GetUserPrincipalName(ctx, cred, principal.ObjectId)
				if err != nil {
					return nil, fmt.Errorf("failed to get user principal name: %w", err)
				}
				principal.DisplayName = ""
			}

			tygerAssignment := TygerRbacRoleAssignment{
				Principal: principal,
				Details:   &assignment,
			}

			switch assignment.AppRoleId {
			case roleIds.ownerRoleId:
				assignments.Owner = append(assignments.Owner, tygerAssignment)
			case roleIds.contributorRoleId:
				assignments.Contributor = append(assignments.Contributor, tygerAssignment)
			}
		}

		url = response.NextLink
	}

	return assignments, nil
}

func ApplyRbacAssignments(ctx context.Context, cred azcore.TokenCredential, desiredAccessControlConfig *AccessControlConfig) (*TygerRbacRoleAssignments, error) {
	if desiredAccessControlConfig.RoleAssignments == nil {
		desiredAccessControlConfig.RoleAssignments = &TygerRbacRoleAssignments{}
	}

	if desiredAccessControlConfig.RoleAssignments.Owner == nil {
		desiredAccessControlConfig.RoleAssignments.Owner = []TygerRbacRoleAssignment{}
	}
	if desiredAccessControlConfig.RoleAssignments.Contributor == nil {
		desiredAccessControlConfig.RoleAssignments.Contributor = []TygerRbacRoleAssignment{}
	}

	log.Info().Msgf("Validating requested assignments")
	for i, assignment := range desiredAccessControlConfig.RoleAssignments.Owner {
		normalizedPrincipal, err := normalizePrincipal(ctx, cred, assignment.Principal)
		if err != nil {
			return nil, err
		}
		desiredAccessControlConfig.RoleAssignments.Owner[i].Principal = normalizedPrincipal
	}

	for i, assignment := range desiredAccessControlConfig.RoleAssignments.Contributor {
		normalizedPrincipal, err := normalizePrincipal(ctx, cred, assignment.Principal)
		if err != nil {
			return nil, err
		}
		desiredAccessControlConfig.RoleAssignments.Contributor[i].Principal = normalizedPrincipal
	}

	if len(desiredAccessControlConfig.RoleAssignments.Owner) == 0 && len(desiredAccessControlConfig.RoleAssignments.Contributor) == 0 {
		log.Info().Msg("No role assignments specified. The Tyger API will not be accessible to anyone.")
	}

	log.Info().Msgf("Getting existing assignments")
	serverSp, err := GetServicePrincipalByUri(ctx, cred, desiredAccessControlConfig.ApiAppUri)
	if err != nil {
		if err == errNotFound {
			return nil, errors.New("run `tyger access-control apply` first to create the service principals")
		}

		return nil, err
	}

	roleIds, err := getRoleIds(serverSp)
	if err != nil {
		return nil, err
	}

	existingAssignments, err := getAssignments(ctx, cred, serverSp)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing assignments: %w", err)
	}

	isChanged := false
	if err := processRoleAssignmentChanges(ctx, cred, serverSp, desiredAccessControlConfig.RoleAssignments.Owner, existingAssignments.Owner, roleIds.ownerRoleId, tygerOwnerRoleValue, &isChanged); err != nil {
		return nil, fmt.Errorf("failed to process owner role assignments: %w", err)
	}

	if err := processRoleAssignmentChanges(ctx, cred, serverSp, desiredAccessControlConfig.RoleAssignments.Contributor, existingAssignments.Contributor, roleIds.contributorRoleId, tygerContributorRoleValue, &isChanged); err != nil {
		return nil, fmt.Errorf("failed to process owner contributor assignments: %w", err)
	}

	if !isChanged {
		log.Info().Msg("No changes in role assignments detected")
	}

	return desiredAccessControlConfig.RoleAssignments, nil
}

func processRoleAssignmentChanges(ctx context.Context, cred azcore.TokenCredential, serverSp *aadServicePrincipal, desiredAssignments, existingAssignments []TygerRbacRoleAssignment, roleId, roleName string, isChanged *bool) error {
	desiredMap := make(map[string]TygerRbacRoleAssignment)
	for _, assignment := range desiredAssignments {
		desiredMap[assignment.Principal.ObjectId] = assignment
	}

	existingMap := make(map[string]TygerRbacRoleAssignment)
	for _, assignment := range existingAssignments {
		existingMap[assignment.Principal.ObjectId] = assignment
	}

	for _, assignment := range desiredAssignments {
		if _, ok := existingMap[assignment.Principal.ObjectId]; !ok {
			log.Info().Msgf("Assigning %s role to %s", roleName, assignment.String())
			*isChanged = true
			if err := assignAppRole(ctx, cred, serverSp.Id, roleId, assignment.Principal); err != nil {
				return fmt.Errorf("failed to assign role: %w", err)
			}
		}
	}

	for _, assignment := range existingAssignments {
		if _, ok := desiredMap[assignment.Principal.ObjectId]; !ok {
			log.Info().Msgf("Removing %s role from %s", roleName, assignment.String())
			*isChanged = true
			if err := removeAppRoleAssignment(ctx, cred, *assignment.Details); err != nil {
				return fmt.Errorf("failed to remove role: %w", err)
			}
		}
	}

	return nil
}

func normalizePrincipal(ctx context.Context, cred azcore.TokenCredential, principal Principal) (Principal, error) {
	switch principal.Kind {
	case PrincipalKindUser:
		if principal.ObjectId == "" {
			if principal.UserPrincipalName == "" {
				return Principal{}, fmt.Errorf("at least one of objectId or userPrincipalName must be set for a user ")
			}

			objectId, err := GetObjectIdByUserPrincipalName(ctx, cred, principal.UserPrincipalName)
			if err != nil {
				return Principal{}, err
			}
			principal.ObjectId = objectId
		} else {
			upn, err := GetUserPrincipalName(ctx, cred, principal.ObjectId)
			if err != nil {
				return Principal{}, err
			}

			if principal.UserPrincipalName != "" && principal.UserPrincipalName != upn {
				return Principal{}, fmt.Errorf("userPrincipalName '%s' should be '%s' for user with object ID %s", principal.UserPrincipalName, upn, principal.ObjectId)
			}

			principal.UserPrincipalName = upn
		}
		principal.DisplayName = ""
		return principal, nil
	case PrincipalKindGroup:
		if principal.UserPrincipalName != "" {
			return Principal{}, fmt.Errorf("userPrincipalName should not be set for a group")
		}

		if principal.ObjectId == "" {
			objectId, err := GetObjectIdByGroupDisplayName(ctx, cred, principal.DisplayName)
			if err != nil {
				if errors.Is(err, errNotFound) {
					return Principal{}, fmt.Errorf("group with display name '%s' not found", principal.DisplayName)
				}
				if errors.Is(err, errMultipleFound) {
					return Principal{}, fmt.Errorf("multiple groups with display name '%s' found", principal.DisplayName)
				}
				return Principal{}, fmt.Errorf("failed to get object ID for group with display name '%s': %w", principal.DisplayName, err)
			}

			principal.ObjectId = objectId
		} else {
			displayName, err := GetGroupDisplayName(ctx, cred, principal.ObjectId)
			if err != nil {
				return Principal{}, err
			}

			if principal.DisplayName != "" && principal.DisplayName != displayName {
				return Principal{}, fmt.Errorf("displayName '%s' should be '%s' for group with object ID %s", principal.DisplayName, displayName, principal.ObjectId)
			}

			principal.DisplayName = displayName
		}
	case PrincipalKindServicePrincipal:
		if principal.UserPrincipalName != "" {
			return Principal{}, fmt.Errorf("userPrincipalName should not be set for a service principal")
		}
		if principal.ObjectId == "" {
			objectId, err := GetObjectIdByServicePrincipalDisplayName(ctx, cred, principal.DisplayName)
			if err != nil {
				if errors.Is(err, errNotFound) {
					return Principal{}, fmt.Errorf("service principal with display name '%s' not found", principal.DisplayName)
				}
				if errors.Is(err, errMultipleFound) {
					return Principal{}, fmt.Errorf("multiple service principals with display name '%s' found", principal.DisplayName)
				}
				return Principal{}, fmt.Errorf("failed to get object ID for service principal with display name '%s': %w", principal.DisplayName, err)
			}

			principal.ObjectId = objectId
		} else {
			displayName, err := GetServicePrincipalDisplayName(ctx, cred, principal.ObjectId)
			if err != nil {
				return Principal{}, err
			}

			if principal.DisplayName != "" && principal.DisplayName != displayName {
				return Principal{}, fmt.Errorf("displayName '%s' should be '%s' for service principal with object ID %s", principal.DisplayName, displayName, principal.ObjectId)
			}

			principal.DisplayName = displayName
		}
	default:
		return Principal{}, fmt.Errorf("unknown principal kind '%s'", principal.Kind)
	}

	return principal, nil
}

func PrettyPrintAccessControlConfig(accessControlConfig *AccessControlConfig, writer io.Writer) error {
	return PrettyPrintStandaloneAccessControlConfig(&StandaloneAccessControlConfig{AccessControlConfig: accessControlConfig}, writer)
}

func PrettyPrintStandaloneAccessControlConfig(accessControlConfig *StandaloneAccessControlConfig, writer io.Writer) error {
	if accessControlConfig == nil {
		accessControlConfig = &StandaloneAccessControlConfig{}
	}

	if accessControlConfig.AccessControlConfig == nil {
		accessControlConfig.AccessControlConfig = &AccessControlConfig{}
	}

	if accessControlConfig.RoleAssignments == nil {
		accessControlConfig.RoleAssignments = &TygerRbacRoleAssignments{}
	}

	t := template.Must(template.New("config").Funcs(templatefunctions.GetFuncMap()).Parse(prettyPrintRbacTemplate))
	return t.Execute(writer, accessControlConfig)
}
