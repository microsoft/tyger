// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"text/template"

	_ "embed"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Masterminds/sprig/v3"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"gopkg.in/yaml.v3"
)

const permissionScopeValue = "Read.Write"
const tygerOwnerRoleValue = "owner"
const tygerContributorRoleValue = "contributor"

//go:embed auth-pretty.tpl
var prettyPrintRbacTemplate string

type TygerAuthSpec struct {
	AuthConfig                 `yaml:",inline"`
	ServiceManagementReference string                    `json:"serviceManagementReference" yaml:"serviceManagementReference"`
	RoleAssignments            *TygerRbacRoleAssignments `yaml:"roleAssignments"`
}

type TygerRbacRoleAssignment struct {
	Principal `yaml:",inline"`
	Details   *aadAppRoleAssignment `yaml:"-"`
}

func (a *TygerRbacRoleAssignment) String() string {
	switch a.Principal.Kind {
	case PrincipalKindUser:
		return fmt.Sprintf("user '%s'", a.Principal.UserPrincipalName)
	case PrincipalKindGroup:
		return fmt.Sprintf("group '%s'", a.Principal.DisplayName)
	case PrincipalKindServicePrincipal:
		return fmt.Sprintf("service principal '%s'", a.Principal.DisplayName)
	default:
		panic(fmt.Sprintf("unknown principal kind '%s'", a.Principal.Kind))
	}
}

type TygerRbacRoleAssignments struct {
	Owner       []TygerRbacRoleAssignment `json:"owner" yaml:"owner"`
	Contributor []TygerRbacRoleAssignment `json:"contributor" yaml:"contributor"`
}

func GetAuthSpec(ctx context.Context, config *AuthConfig, cred azcore.TokenCredential) (*TygerAuthSpec, error) {
	serverApp, err := GetAppByAppIdOrUri(ctx, cred, config.ApiAppId, config.ApiAppUri)
	if err != nil {
		if err != errNotFound {
			return nil, fmt.Errorf("failed to get api app: %w", err)
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
			return nil, fmt.Errorf("failed to get cli app: %w", err)
		}
	}

	if cliApp != nil {
		config.CliAppId = cliApp.AppId
		if config.CliAppUri == "" {
			config.CliAppUri = cliApp.IdentifierUris[0]
		}
	}

	if serverApp == nil {
		return &TygerAuthSpec{
			AuthConfig: *config,
		}, nil
	}

	roleAssignments, err := getRbacAssignments(ctx, cred, config)
	if err != nil {
		return nil, fmt.Errorf("failed to get role assignments: %w", err)
	}

	return &TygerAuthSpec{
		AuthConfig:      *config,
		RoleAssignments: roleAssignments,
	}, nil
}

func ApplyAuthSpec(ctx context.Context, authSpec *TygerAuthSpec, cred azcore.TokenCredential) (*TygerAuthSpec, error) {
	if err := installIdentities(ctx, authSpec, cred); err != nil {
		return nil, err
	}

	normalizedAssignments, err := ApplyRbacAssignments(ctx, cred, authSpec.RoleAssignments, &authSpec.AuthConfig)
	if err != nil {
		return nil, err
	}

	authSpec.RoleAssignments = normalizedAssignments
	return authSpec, nil
}

func installIdentities(ctx context.Context, authSpec *TygerAuthSpec, cred azcore.TokenCredential) error {
	serverApp, err := createOrUpdateServerApp(ctx, authSpec, cred)
	if err != nil {
		return err
	}

	authSpec.ApiAppId = serverApp.AppId
	if authSpec.ApiAppUri == "" {
		authSpec.ApiAppUri = serverApp.IdentifierUris[0]
	}

	if _, err := GetServicePrincipalByAppId(ctx, cred, serverApp.AppId); err != nil {
		if err != errNotFound {
			return fmt.Errorf("failed to get service principal for API app: %w", err)
		}
		if _, err := CreateServicePrincipal(ctx, cred, serverApp.AppId); err != nil {
			return fmt.Errorf("failed to create service principal for API app: %w", err)
		}
	}

	cliApp, err := createOrUpdateCliApp(ctx, authSpec, serverApp, cred)
	if err != nil {
		return err
	}

	authSpec.CliAppId = cliApp.AppId
	if authSpec.CliAppUri == "" {
		authSpec.CliAppUri = cliApp.IdentifierUris[0]
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

func createOrUpdateServerApp(ctx context.Context, authSpec *TygerAuthSpec, cred azcore.TokenCredential) (*aadApp, error) {
	if authSpec.ApiAppId == "" && authSpec.ApiAppUri == "" {
		return nil, errors.New("`apiAppUri` must be set")
	}

	app, err := GetAppByAppIdOrUri(ctx, cred, authSpec.ApiAppId, authSpec.ApiAppUri)
	if err != nil {
		if err == errNotFound {
			app = &aadApp{
				IdentifierUris: []string{authSpec.ApiAppUri},
				Api:            &aadAppApi{},
			}
		} else {
			return nil, fmt.Errorf("error getting existing server app: %w", err)
		}
	}

	app.DisplayName = valueOrDefault(app.DisplayName, "Tyger API")
	app.SignInAudience = valueOrDefault(app.SignInAudience, "AzureADMyOrg")
	if app.Api.RequestedAccessTokenVersion == 0 {
		app.Api.RequestedAccessTokenVersion = 2
	}
	app.ServiceManagementReference = authSpec.ServiceManagementReference

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
		log.Ctx(ctx).Info().Msgf("Creating app %s", authSpec.ApiAppUri)
		err = executeGraphCall(ctx, cred, http.MethodPost, "https://graph.microsoft.com/beta/applications", app, &app)
	} else {
		log.Ctx(ctx).Info().Msgf("Updating app %s", authSpec.ApiAppUri)
		err = executeGraphCall(ctx, cred, http.MethodPatch, fmt.Sprintf("https://graph.microsoft.com/beta/applications/%s", app.Id), app, nil)
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

func createOrUpdateCliApp(ctx context.Context, authSpec *TygerAuthSpec, serverApp *aadApp, cred azcore.TokenCredential) (*aadApp, error) {
	if authSpec.CliAppId == "" && authSpec.CliAppUri == "" {
		return nil, errors.New("`cliAppUri` must be set")
	}

	app, err := GetAppByAppIdOrUri(ctx, cred, authSpec.CliAppId, authSpec.CliAppUri)
	if err != nil {
		if err == errNotFound {
			app = &aadApp{
				IdentifierUris:         []string{authSpec.CliAppUri},
				RequiredResourceAccess: []*aadAppRequiredResourceAccess{},
			}
		} else {
			return nil, fmt.Errorf("error getting existing CLI app: %w", err)
		}
	}

	app.DisplayName = valueOrDefault(app.DisplayName, "Tyger CLI")
	app.SignInAudience = valueOrDefault(app.SignInAudience, "AzureADMyOrg")
	app.IsFallbackPublicClient = true
	app.PublicClient = &aadAppPublicClient{
		RedirectUris: []string{"http://localhost"},
	}

	app.ServiceManagementReference = authSpec.ServiceManagementReference

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
		log.Ctx(ctx).Info().Msgf("Creating app %s", authSpec.CliAppUri)
		err = executeGraphCall(ctx, cred, http.MethodPost, "https://graph.microsoft.com/beta/applications", app, &app)
	} else {
		log.Ctx(ctx).Info().Msgf("Updating app %s", authSpec.CliAppUri)
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

func getRbacAssignments(ctx context.Context, cred azcore.TokenCredential, authConfig *AuthConfig) (*TygerRbacRoleAssignments, error) {
	serverSp, err := GetServicePrincipalByUri(ctx, cred, authConfig.ApiAppUri)
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
		return roleIds, errors.New("run `tyger auth apply` first to create the app roles")
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

func ApplyRbacAssignments(ctx context.Context, cred azcore.TokenCredential, desiredRbacConfig *TygerRbacRoleAssignments, authConfig *AuthConfig) (*TygerRbacRoleAssignments, error) {
	if desiredRbacConfig == nil {
		desiredRbacConfig = &TygerRbacRoleAssignments{}
	}

	if desiredRbacConfig.Owner == nil {
		desiredRbacConfig.Owner = []TygerRbacRoleAssignment{}
	}
	if desiredRbacConfig.Contributor == nil {
		desiredRbacConfig.Contributor = []TygerRbacRoleAssignment{}
	}

	log.Info().Msgf("Validating requested assignments")
	for i, assignment := range desiredRbacConfig.Owner {
		normalizedPrincipal, err := normalizePrincipal(ctx, cred, assignment.Principal)
		if err != nil {
			return nil, err
		}
		desiredRbacConfig.Owner[i].Principal = normalizedPrincipal
	}

	for i, assignment := range desiredRbacConfig.Contributor {
		normalizedPrincipal, err := normalizePrincipal(ctx, cred, assignment.Principal)
		if err != nil {
			return nil, err
		}
		desiredRbacConfig.Contributor[i].Principal = normalizedPrincipal
	}

	log.Info().Msgf("Getting existing assignments")
	serverSp, err := GetServicePrincipalByUri(ctx, cred, authConfig.ApiAppUri)
	if err != nil {
		if err == errNotFound {
			return nil, errors.New("run `tyger auth apply` first to create the service principals")
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

	if err := processRoleAssignmenChanges(ctx, cred, serverSp, desiredRbacConfig.Owner, existingAssignments.Owner, roleIds.ownerRoleId, tygerOwnerRoleValue); err != nil {
		return nil, fmt.Errorf("failed to process owner role assignments: %w", err)
	}

	if err := processRoleAssignmenChanges(ctx, cred, serverSp, desiredRbacConfig.Contributor, existingAssignments.Contributor, roleIds.contributorRoleId, tygerContributorRoleValue); err != nil {
		return nil, fmt.Errorf("failed to process owner contributor assignments: %w", err)
	}

	return desiredRbacConfig, nil
}

func processRoleAssignmenChanges(ctx context.Context, cred azcore.TokenCredential, serverSp *aadServicePrincipal, desiredAssignments, existingAssignments []TygerRbacRoleAssignment, roleId, roleName string) error {
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
			if err := assignAppRole(ctx, cred, serverSp.Id, roleId, assignment.Principal); err != nil {
				return fmt.Errorf("failed to assign role: %w", err)
			}
		}
	}

	for _, assignment := range existingAssignments {
		if _, ok := desiredMap[assignment.Principal.ObjectId]; !ok {
			log.Info().Msgf("Removing %s role from %s", roleName, assignment.String())
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

func PrettyPrintAuthSpec(config *TygerAuthSpec, writer io.Writer) error {
	if config == nil {
		config = &TygerAuthSpec{}
	}

	if config.RoleAssignments == nil {
		config.RoleAssignments = &TygerRbacRoleAssignments{}
	}

	funcMap := sprig.FuncMap()
	funcMap["toYAML"] = func(v any) string {
		buf := &bytes.Buffer{}
		enc := yaml.NewEncoder(buf)
		enc.SetIndent(2)
		err := enc.Encode(v)
		if err != nil {
			panic(err)
		}

		return buf.String()
	}

	funcMap["indentAfterFirst"] = func(spaces int, v string) string {
		pad := strings.Repeat(" ", spaces)
		return strings.Replace(v, "\n", "\n"+pad, -1)
	}

	funcMap["deref"] = deref

	t := template.Must(template.New("config").Funcs(funcMap).Parse(prettyPrintRbacTemplate))
	return t.Execute(writer, config)
}
