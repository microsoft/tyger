// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v7"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
)

const (
	tygerServerManagedIdentityName           = "tyger-server"
	migrationRunnerManagedIdentityName       = "tyger-migration-runner"
	traefikKeyVaultClientManagedIdentityName = "traefik"
)

func isSystemManagedIdentityName(name string) bool {
	return strings.EqualFold(name, tygerServerManagedIdentityName) || strings.EqualFold(name, migrationRunnerManagedIdentityName) || strings.EqualFold(name, traefikKeyVaultClientManagedIdentityName)
}

func (inst *Installer) createTygerServerManagedIdentity(ctx context.Context, org *OrganizationConfig) (*armmsi.Identity, error) {
	return inst.createManagedIdentity(ctx, tygerServerManagedIdentityName, org.Cloud.ResourceGroup)
}

func (inst *Installer) createMigrationRunnerManagedIdentity(ctx context.Context, org *OrganizationConfig) (*armmsi.Identity, error) {
	return inst.createManagedIdentity(ctx, migrationRunnerManagedIdentityName, org.Cloud.ResourceGroup)
}

func (inst *Installer) createTraefikKeyVaultClientManagedIdentity(ctx context.Context) (*armmsi.Identity, error) {
	return inst.createManagedIdentity(ctx, traefikKeyVaultClientManagedIdentityName, inst.Config.Cloud.ResourceGroup)
}

func (inst *Installer) getTraefikKeyVaultClientManagedIdentity(ctx context.Context) (*armmsi.Identity, error) {
	identitiesClient, err := armmsi.NewUserAssignedIdentitiesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create managed identities client: %w", err)
	}

	resp, err := identitiesClient.Get(ctx, inst.Config.Cloud.ResourceGroup, traefikKeyVaultClientManagedIdentityName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get managed identity: %w", err)
	}

	return &resp.Identity, nil
}

func (inst *Installer) createManagedIdentity(ctx context.Context, name string, resourceGroup string) (*armmsi.Identity, error) {
	log.Ctx(ctx).Info().Msgf("Creating or updating managed identity '%s'", name)

	identitiesClient, err := armmsi.NewUserAssignedIdentitiesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create managed identities client: %w", err)
	}

	var tags map[string]*string
	existing, err := identitiesClient.Get(ctx, resourceGroup, name, nil)
	if err != nil {
		var azcoreErr *azcore.ResponseError
		if !errors.As(err, &azcoreErr) || azcoreErr.ErrorCode != "ResourceNotFound" {
			return nil, fmt.Errorf("failed to get managed identity: %w", err)
		}
	} else {
		tags = existing.Tags
	}

	if tags == nil {
		tags = make(map[string]*string)
	}

	tags[TagKey] = &inst.Config.EnvironmentName

	resp, err := identitiesClient.CreateOrUpdate(ctx, resourceGroup, name, armmsi.Identity{
		Location: &inst.Config.Cloud.DefaultLocation,
		Tags:     tags,
	}, nil)

	if err != nil {
		return nil, fmt.Errorf("failed to create managed identity: %w", err)
	}

	if err := assignRbacRole(ctx, inst.Config.Cloud.Compute.GetManagementPrincipalIds(), true, *resp.ID, "Reader", inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
		return nil, fmt.Errorf("failed to assign RBAC role on managed identity: %w", err)
	}

	return &resp.Identity, nil
}

func (inst *Installer) deleteUnusedIdentities(ctx context.Context, org *OrganizationConfig) (any, error) {
	identitiesClient, err := armmsi.NewUserAssignedIdentitiesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create managed identities client: %w", err)
	}

	pager := identitiesClient.NewListByResourceGroupPager(org.Cloud.ResourceGroup, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			return nil, fmt.Errorf("failed to list managed identities: %w", err)
		}

		for _, identity := range page.Value {
			if identity.Tags != nil {
				if env, ok := identity.Tags[TagKey]; ok && env != nil && *env == inst.Config.EnvironmentName {
					if isSystemManagedIdentityName(*identity.Name) || slices.Contains(org.Cloud.Identities, *identity.Name) {
						continue
					}

					log.Ctx(ctx).Info().Msgf("Deleting unused managed identity '%s'", *identity.Name)
					if _, err := identitiesClient.Delete(ctx, org.Cloud.ResourceGroup, *identity.Name, nil); err != nil {
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
	namespace string,
) (any, error) {
	cluster, err := clusterPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	mi, err := managedIdentityPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	log.Ctx(ctx).Info().Msgf("Creating or updating federated identity credential '%s'", *mi.Name)

	desiredCredentialName := *cluster.Name

	var subject string
	if isSystemManagedIdentityName(*mi.Name) {
		subject = fmt.Sprintf("system:serviceaccount:%s:%s", namespace, *mi.Name)
	} else {
		subject = fmt.Sprintf("system:serviceaccount:%s:tyger-custom-%s-job", namespace, *mi.Name)
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

	resourceGroup := getResourceGroupFromID(*mi.ID)
	pager := client.NewListPager(resourceGroup, *mi.Name, nil)
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

	_, err = client.CreateOrUpdate(ctx, resourceGroup, *mi.Name, desiredCredentialName, desiredCred, nil)

	if err != nil {
		return nil, fmt.Errorf("failed to create federated identity credential: %w", err)
	}

	return nil, nil
}
