package install

import (
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization/v2"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"github.com/stretchr/testify/assert"
)

func init() {
	log.Logger = log.Logger.Level(zerolog.Disabled)
}

func TestCheckAccessExactScopeExactPermission(t *testing.T) {
	scope := "subscriptions/bb9421df-fcf4-4e1f-b89f-6b20e94f301c/resourceGroups/test/providers/Microsoft.Storage/storageAccounts/jstestwestus2buf"

	role := armauthorization.RoleDefinition{
		ID: Ptr("abc"),
		Properties: &armauthorization.RoleDefinitionProperties{
			Permissions: []*armauthorization.Permission{
				{
					Actions: []*string{
						Ptr("Microsoft.Storage/storageAccounts/write"),
					},
				},
			},
		},
	}

	roleAssignment := armauthorization.RoleAssignment{
		Properties: &armauthorization.RoleAssignmentProperties{
			Scope:            &scope,
			RoleDefinitionID: role.ID,
		},
	}
	assert.NoError(t, checkAccess(scope, "Microsoft.Storage/storageAccounts/write", []armauthorization.RoleAssignment{roleAssignment}, map[string]armauthorization.RoleDefinition{*role.ID: role}))
}

func TestCheckAccessExactScopeWrongPermission(t *testing.T) {
	scope := "subscriptions/bb9421df-fcf4-4e1f-b89f-6b20e94f301c/resourceGroups/test/providers/Microsoft.Storage/storageAccounts/jstestwestus2buf"

	role := armauthorization.RoleDefinition{
		ID: Ptr("abc"),
		Properties: &armauthorization.RoleDefinitionProperties{
			Permissions: []*armauthorization.Permission{
				{
					Actions: []*string{
						Ptr("Microsoft.Storage/storageAccounts/write"),
					},
				},
			},
		},
	}

	roleAssignment := armauthorization.RoleAssignment{
		Properties: &armauthorization.RoleAssignmentProperties{
			Scope:            &scope,
			RoleDefinitionID: role.ID,
		},
	}
	assert.Error(t, checkAccess(scope, "Microsoft.Storage/storageAccounts/read", []armauthorization.RoleAssignment{roleAssignment}, map[string]armauthorization.RoleDefinition{*role.ID: role}))
}

func TestCheckAccessWrongScopeExactPermission(t *testing.T) {
	scope := "subscriptions/bb9421df-fcf4-4e1f-b89f-6b20e94f301c/resourceGroups/test/providers/Microsoft.Storage/storageAccounts/jstestwestus2buf"

	role := armauthorization.RoleDefinition{
		ID: Ptr("abc"),
		Properties: &armauthorization.RoleDefinitionProperties{
			Permissions: []*armauthorization.Permission{
				{
					Actions: []*string{
						Ptr("Microsoft.Storage/storageAccounts/write"),
					},
				},
			},
		},
	}

	roleAssignment := armauthorization.RoleAssignment{
		Properties: &armauthorization.RoleAssignmentProperties{
			Scope:            Ptr("subscriptions/bb9421df-fcf4-4e1f-b89f-6b20e94f301c/resourceGroups/wrong"),
			RoleDefinitionID: role.ID,
		},
	}
	assert.Error(t, checkAccess(scope, "Microsoft.Storage/storageAccounts/write", []armauthorization.RoleAssignment{roleAssignment}, map[string]armauthorization.RoleDefinition{*role.ID: role}))
}

func TestCheckAccessInheritedExactPermission(t *testing.T) {
	scope := "subscriptions/bb9421df-fcf4-4e1f-b89f-6b20e94f301c/resourceGroups/test/providers/Microsoft.Storage/storageAccounts/jstestwestus2buf"

	role := armauthorization.RoleDefinition{
		ID: Ptr("abc"),
		Properties: &armauthorization.RoleDefinitionProperties{
			Permissions: []*armauthorization.Permission{
				{
					Actions: []*string{
						Ptr("Microsoft.Storage/storageAccounts/write"),
					},
				},
			},
		},
	}

	roleAssignment := armauthorization.RoleAssignment{
		Properties: &armauthorization.RoleAssignmentProperties{
			Scope:            Ptr("subscriptions/bb9421df-fcf4-4e1f-b89f-6b20e94f301c/resourceGroups/test"),
			RoleDefinitionID: role.ID,
		},
	}
	assert.NoError(t, checkAccess(scope, "Microsoft.Storage/storageAccounts/write", []armauthorization.RoleAssignment{roleAssignment}, map[string]armauthorization.RoleDefinition{*role.ID: role}))
}

func TestCheckAccessInheritedWildcard(t *testing.T) {
	scope := "subscriptions/bb9421df-fcf4-4e1f-b89f-6b20e94f301c/resourceGroups/test/providers/Microsoft.Storage/storageAccounts/jstestwestus2buf"

	role := armauthorization.RoleDefinition{
		ID: Ptr("abc"),
		Properties: &armauthorization.RoleDefinitionProperties{
			Permissions: []*armauthorization.Permission{
				{
					Actions: []*string{
						Ptr("Microsoft.Storage/storageAccounts/*"),
					},
				},
			},
		},
	}

	roleAssignment := armauthorization.RoleAssignment{
		Properties: &armauthorization.RoleAssignmentProperties{
			Scope:            Ptr("subscriptions/bb9421df-fcf4-4e1f-b89f-6b20e94f301c/resourceGroups/test"),
			RoleDefinitionID: role.ID,
		},
	}
	assert.NoError(t, checkAccess(scope, "Microsoft.Storage/storageAccounts/write", []armauthorization.RoleAssignment{roleAssignment}, map[string]armauthorization.RoleDefinition{*role.ID: role}))
}

func TestCheckAccessInheritedWildcardWithNotAction(t *testing.T) {
	scope := "subscriptions/bb9421df-fcf4-4e1f-b89f-6b20e94f301c/resourceGroups/test/providers/Microsoft.Storage/storageAccounts/jstestwestus2buf"

	role := armauthorization.RoleDefinition{
		ID: Ptr("abc"),
		Properties: &armauthorization.RoleDefinitionProperties{
			Permissions: []*armauthorization.Permission{
				{
					Actions: []*string{
						Ptr("Microsoft.Storage/storageAccounts/*"),
					},
					NotActions: []*string{
						Ptr("Microsoft.Storage/storageAccounts/write"),
					},
				},
			},
		},
	}

	roleAssignment := armauthorization.RoleAssignment{
		Properties: &armauthorization.RoleAssignmentProperties{
			Scope:            Ptr("subscriptions/bb9421df-fcf4-4e1f-b89f-6b20e94f301c/resourceGroups/test"),
			RoleDefinitionID: role.ID,
		},
	}
	assert.Error(t, checkAccess(scope, "Microsoft.Storage/storageAccounts/write", []armauthorization.RoleAssignment{roleAssignment}, map[string]armauthorization.RoleDefinition{*role.ID: role}))
}
