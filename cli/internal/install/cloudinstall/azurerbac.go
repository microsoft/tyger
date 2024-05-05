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

func assignRbacRole(ctx context.Context, principalId, scope, roleName, subscriptionId string, credential azcore.TokenCredential) error {
	roleId, err := getRbacRole(ctx, credential, scope, roleName)
	if err != nil {
		return fmt.Errorf("failed to get %s role: %w", roleName, err)
	}

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
			if *ra.Properties.RoleDefinitionID == roleId && *ra.Properties.PrincipalID == principalId {
				return nil
			}
		}
	}

	for i := 0; ; i++ {
		_, err = roleAssignmentClient.Create(
			ctx,
			scope,
			uuid.New().String(),
			armauthorization.RoleAssignmentCreateParameters{
				Properties: &armauthorization.RoleAssignmentProperties{
					RoleDefinitionID: Ptr(roleId),
					PrincipalID:      Ptr(principalId),
				},
			}, nil)
		if err != nil {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) {
				switch respErr.ErrorCode {
				case "RoleAssignmentExists":
					return nil
				case "PrincipalNotFound":
					if i > 60 {
						break
					}
					time.Sleep(10 * time.Second)
					continue
				}
			}
		}

		return err
	}
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
