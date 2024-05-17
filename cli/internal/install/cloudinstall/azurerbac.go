// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v2"
	"github.com/google/uuid"
)

const rbacAssignmentCreatedByTyger = "Assigned by Tyger"

func getRbacRole(ctx context.Context, credential azcore.TokenCredential, scope string, roleName string) (string, error) {
	roleDefClient, err := armauthorization.NewRoleDefinitionsClient(credential, nil)
	if err != nil {
		return "", err
	}

	pager := roleDefClient.NewListPager(scope, &armauthorization.RoleDefinitionsClientListOptions{Filter: Ptr(fmt.Sprintf("rolename eq '%s'", roleName))})

	var roleId string
	for pager.More() && roleId == "" {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return "", err
		}

		for _, rd := range page.Value {
			if *rd.Properties.RoleName != roleName {
				panic(fmt.Sprintf("unexpected role name '%s'", *rd.Name))
			}
			roleId = *rd.ID
			break
		}
	}

	if roleId == "" {
		return "", fmt.Errorf("unable to find '%s' role", roleName)
	}
	return roleId, nil
}

// Assign the specified role to principalIds at the specified scope.
// Existing assignments to the same role to different principals will be removed
// if they were created by this tool and deleteOtherAssignments is true.
func assignRbacRole(ctx context.Context, principalIds []string, deleteOtherAssignments bool, scope, roleName, subscriptionId string, credential azcore.TokenCredential) error {
	roleId, err := getRbacRole(ctx, credential, scope, roleName)
	if err != nil {
		return fmt.Errorf("failed to get %s role: %w", roleName, err)
	}

	roleAssignmentClient, err := armauthorization.NewRoleAssignmentsClient(subscriptionId, credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create role assignments client: %w", err)
	}

	principalIdsToAdd := make(map[string]any)
	for _, principalId := range principalIds {
		principalIdsToAdd[principalId] = nil
	}

	roleAssignmentsToDelete := make([]*armauthorization.RoleAssignment, 0)

	pager := roleAssignmentClient.NewListForScopePager(scope, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to list role assignments: %w", err)
		}

		for _, ra := range page.RoleAssignmentListResult.Value {
			if *ra.Properties.RoleDefinitionID == roleId {
				if _, ok := principalIdsToAdd[*ra.Properties.PrincipalID]; ok {
					delete(principalIdsToAdd, *ra.Properties.PrincipalID)
				} else if ra.Properties.Description != nil && *ra.Properties.Description == rbacAssignmentCreatedByTyger {
					roleAssignmentsToDelete = append(roleAssignmentsToDelete, ra)
				}
			}
		}
	}

	for principalId := range principalIdsToAdd {
		completed := false
		for i := 0; !completed; i++ {
			_, err = roleAssignmentClient.Create(
				ctx,
				scope,
				uuid.New().String(),
				armauthorization.RoleAssignmentCreateParameters{
					Properties: &armauthorization.RoleAssignmentProperties{
						RoleDefinitionID: Ptr(roleId),
						PrincipalID:      Ptr(principalId),
						Description:      Ptr(rbacAssignmentCreatedByTyger),
					},
				}, nil)
			if err != nil {
				var respErr *azcore.ResponseError
				if errors.As(err, &respErr) {
					switch respErr.ErrorCode {
					case "RoleAssignmentExists":
						completed = true
					case "PrincipalNotFound":
						if i > 60 {
							return err
						}
						time.Sleep(10 * time.Second)
						continue
					default:
						return err
					}
				}
			}
		}
	}

	if !deleteOtherAssignments {
		return nil
	}

	for _, ra := range roleAssignmentsToDelete {
		_, err = roleAssignmentClient.DeleteByID(ctx, *ra.ID, nil)
		if err != nil {
			return fmt.Errorf("failed to delete role assignment: %w", err)
		}
	}

	return nil
}

func removeRbacRoleAssignments(ctx context.Context, principalId, scope, subscriptionId string, credential azcore.TokenCredential) error {
	roleAssignmentClient, err := armauthorization.NewRoleAssignmentsClient(subscriptionId, credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create role assignments client: %w", err)
	}

	pager := roleAssignmentClient.NewListForScopePager(scope, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return fmt.Errorf("failed to list role assignments: %w", err)
		}

		for _, ra := range page.RoleAssignmentListResult.Value {
			if *ra.Properties.PrincipalID == principalId {
				_, err = roleAssignmentClient.DeleteByID(ctx, *ra.ID, nil)
				if err != nil {
					return fmt.Errorf("failed to delete role assignment: %w", err)
				}
			}
		}
	}

	return nil
}
