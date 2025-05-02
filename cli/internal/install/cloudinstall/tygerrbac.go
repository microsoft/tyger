package cloudinstall

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/rs/zerolog/log"
)

type TygerRbacConfig struct {
	ServerUrl       string                    `json:"serverUrl" yaml:"serverUrl"`
	RoleAssignments *TygerRbacRoleAssignments `json:"roleAssignments" yaml:"roleAssignments"`
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

func GetRbacAssignments(ctx context.Context, cred azcore.TokenCredential, serverUrl string, authConfig *AuthConfig, populateServicePrincipalName bool) (*TygerRbacConfig, error) {
	serverSp, err := GetServicePrincipalByUri(ctx, cred, authConfig.ApiAppUri)
	if err != nil {
		if err == errNotFound {
			return nil, errors.New("run `tyger identities install` first to create the service principals")
		}

		return nil, err
	}

	roleAssignments, err := getAssignments(ctx, cred, serverSp, populateServicePrincipalName)
	if err != nil {
		return nil, fmt.Errorf("failed to get role assignments: %w", err)
	}

	return &TygerRbacConfig{
		ServerUrl:       serverUrl,
		RoleAssignments: roleAssignments,
	}, nil
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
		return roleIds, errors.New("run `tyger identities install` first to create the app roles")
	}

	return roleIds, nil
}

func getAssignments(ctx context.Context, cred azcore.TokenCredential, serverSp *aadServicePrincipal, populateServicePrincipalName bool) (*TygerRbacRoleAssignments, error) {
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

			if principal.Kind == PrincipalKindUser && populateServicePrincipalName {
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

func ApplyRbacAssignments(ctx context.Context, cred azcore.TokenCredential, desiredRbacConfig *TygerRbacConfig, authConfig *AuthConfig) (*TygerRbacConfig, error) {
	if desiredRbacConfig.RoleAssignments == nil {
		desiredRbacConfig.RoleAssignments = &TygerRbacRoleAssignments{}
	}

	if desiredRbacConfig.RoleAssignments.Owner == nil {
		desiredRbacConfig.RoleAssignments.Owner = []TygerRbacRoleAssignment{}
	}
	if desiredRbacConfig.RoleAssignments.Contributor == nil {
		desiredRbacConfig.RoleAssignments.Contributor = []TygerRbacRoleAssignment{}
	}

	log.Info().Msgf("Validating requested assignments")
	for i, assignment := range desiredRbacConfig.RoleAssignments.Owner {
		normalizedPrincipal, err := normalizePrincipal(ctx, cred, assignment.Principal)
		if err != nil {
			return nil, err
		}
		desiredRbacConfig.RoleAssignments.Owner[i].Principal = normalizedPrincipal
	}

	for i, assignment := range desiredRbacConfig.RoleAssignments.Contributor {
		normalizedPrincipal, err := normalizePrincipal(ctx, cred, assignment.Principal)
		if err != nil {
			return nil, err
		}
		desiredRbacConfig.RoleAssignments.Contributor[i].Principal = normalizedPrincipal
	}

	log.Info().Msgf("Getting existing assignments")
	serverSp, err := GetServicePrincipalByUri(ctx, cred, authConfig.ApiAppUri)
	if err != nil {
		if err == errNotFound {
			return nil, errors.New("run `tyger identities install` first to create the service principals")
		}

		return nil, err
	}

	roleIds, err := getRoleIds(serverSp)
	if err != nil {
		return nil, err
	}

	existingAssignments, err := getAssignments(ctx, cred, serverSp, false)
	if err != nil {
		return nil, fmt.Errorf("failed to get existing assignments: %w", err)
	}

	if err := processRoleAssignmenChanges(ctx, cred, serverSp, desiredRbacConfig.RoleAssignments.Owner, existingAssignments.Owner, roleIds.ownerRoleId, tygerOwnerRoleValue); err != nil {
		return nil, fmt.Errorf("failed to process owner role assignments: %w", err)
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
