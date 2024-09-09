// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
)

const (
	tygerServerManagedIdentityName     = "tyger-server"
	migrationRunnerManagedIdentityName = "tyger-migration-runner"
)

func isSystemManagedIdentityName(name string) bool {
	return strings.EqualFold(name, tygerServerManagedIdentityName) || strings.EqualFold(name, migrationRunnerManagedIdentityName)
}

func (inst *Installer) createTygerServerManagedIdentity(ctx context.Context) (*armmsi.Identity, error) {
	return inst.createManagedIdentity(ctx, tygerServerManagedIdentityName)
}

func (inst *Installer) createMigrationRunnerManagedIdentity(ctx context.Context) (*armmsi.Identity, error) {
	return inst.createManagedIdentity(ctx, migrationRunnerManagedIdentityName)
}

func (inst *Installer) createManagedIdentity(ctx context.Context, name string) (*armmsi.Identity, error) {
	log.Info().Msgf("Creating or updating managed identity '%s'", name)

	identitiesClient, err := armmsi.NewUserAssignedIdentitiesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create managed identities client: %w", err)
	}

	resp, err := identitiesClient.CreateOrUpdate(ctx, inst.Config.Cloud.ResourceGroup, name, armmsi.Identity{
		Location: &inst.Config.Cloud.DefaultLocation,
		Tags: map[string]*string{
			TagKey: &inst.Config.EnvironmentName,
		},
	}, nil)

	if err != nil {
		return nil, fmt.Errorf("failed to create managed identity: %w", err)
	}

	if err := assignRbacRole(ctx, inst.Config.Cloud.Compute.GetManagementPrincipalIds(), true, *resp.ID, "Reader", inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
		return nil, fmt.Errorf("failed to assign RBAC role on managed identity: %w", err)
	}

	return &resp.Identity, nil
}

func (inst *Installer) deleteUnusedIdentities(ctx context.Context) (any, error) {
	identitiesClient, err := armmsi.NewUserAssignedIdentitiesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create managed identities client: %w", err)
	}

	pager := identitiesClient.NewListByResourceGroupPager(inst.Config.Cloud.ResourceGroup, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list managed identities: %w", err)
		}

		for _, identity := range page.Value {
			if identity.Tags != nil {
				if env, ok := identity.Tags[TagKey]; ok && env != nil && *env == inst.Config.EnvironmentName {
					if isSystemManagedIdentityName(*identity.Name) || slices.Contains(inst.Config.Cloud.Compute.Identities, *identity.Name) {
						continue
					}

					log.Info().Msgf("Deleting unused managed identity '%s'", *identity.Name)
					if _, err := identitiesClient.Delete(ctx, inst.Config.Cloud.ResourceGroup, *identity.Name, nil); err != nil {
						return nil, fmt.Errorf("failed to delete managed identity: %w", err)
					}
				}
			}
		}
	}

	return nil, nil
}

func (inst *Installer) createFederatedIdentityCredential(
	ctx context.Context,
	managedIdentityPromise *install.Promise[*armmsi.Identity],
	clusterPromise *install.Promise[*armcontainerservice.ManagedCluster],
) (any, error) {
	cluster, err := clusterPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	mi, err := managedIdentityPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	log.Info().Msgf("Creating or updating federated identity credential '%s'", *mi.Name)

	desiredCredentialName := *cluster.Name

	var subject string
	if isSystemManagedIdentityName(*mi.Name) {
		subject = fmt.Sprintf("system:serviceaccount:%s:%s", TygerNamespace, *mi.Name)
	} else {
		subject = fmt.Sprintf("system:serviceaccount:%s:tyger-custom-%s-job", TygerNamespace, *mi.Name)
	}

	issuerUrl := *cluster.Properties.OidcIssuerProfile.IssuerURL

	desiredCred := armmsi.FederatedIdentityCredential{
		Properties: &armmsi.FederatedIdentityCredentialProperties{
			Issuer:  &issuerUrl,
			Subject: Ptr(subject),
			Audiences: []*string{
				Ptr("api://AzureADTokenExchange"),
			},
		},
	}

	client, err := armmsi.NewFederatedIdentityCredentialsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create federated identity credentials client: %w", err)
	}

	pager := client.NewListPager(inst.Config.Cloud.ResourceGroup, *mi.Name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list federated identity credentials: %w", err)
		}

		for _, cred := range page.Value {
			if cred.Properties.Issuer != nil && *cred.Properties.Issuer == *desiredCred.Properties.Issuer &&
				cred.Properties.Subject != nil && *cred.Properties.Subject == *desiredCred.Properties.Subject {

				log.Debug().Msgf("Federated identity credential already exists for '%s'", *mi.Name)
				return nil, nil
			}
		}
	}

	_, err = client.CreateOrUpdate(ctx, inst.Config.Cloud.ResourceGroup, *mi.Name, desiredCredentialName, desiredCred, nil)

	if err != nil {
		return nil, fmt.Errorf("failed to create federated identity credential: %w", err)
	}

	return nil, nil
}
