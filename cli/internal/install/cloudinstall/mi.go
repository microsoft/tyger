// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"errors"
	"fmt"
	"net/http"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
)

const (
	tygerServerManagedIdentityName     = "tyger-server"
	migrationRunnerManagedIdentityName = "tyger-migration-runner"
)

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

	return &resp.Identity, nil
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

	issuerUrl := *cluster.Properties.OidcIssuerProfile.IssuerURL

	desiredCred := armmsi.FederatedIdentityCredential{
		Properties: &armmsi.FederatedIdentityCredentialProperties{
			Issuer:  &issuerUrl,
			Subject: Ptr(fmt.Sprintf("system:serviceaccount:%s:%s", TygerNamespace, *mi.Name)),
			Audiences: []*string{
				Ptr("api://AzureADTokenExchange"),
			},
		},
	}

	client, err := armmsi.NewFederatedIdentityCredentialsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create federated identity credentials client: %w", err)
	}

	existingCred, err := client.Get(ctx, inst.Config.Cloud.ResourceGroup, *mi.Name, *mi.Name, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if !errors.As(err, &respErr) || respErr.StatusCode != http.StatusNotFound {
			return nil, fmt.Errorf("failed to get federated identity credential: %w", err)
		}
	} else {
		if existingCred.Properties.Issuer != nil && *existingCred.Properties.Issuer == *desiredCred.Properties.Issuer &&
			existingCred.Properties.Subject != nil && *existingCred.Properties.Subject == *desiredCred.Properties.Subject &&
			existingCred.Properties.Audiences != nil && len(existingCred.Properties.Audiences) == 1 && *existingCred.Properties.Audiences[0] == *desiredCred.Properties.Audiences[0] {

			log.Debug().Msgf("Federated identity credential already exists for '%s'", *mi.Name)
			return nil, nil
		}
	}

	_, err = client.CreateOrUpdate(ctx, inst.Config.Cloud.ResourceGroup, *mi.Name, *mi.Name, desiredCred, nil)

	if err != nil {
		return nil, fmt.Errorf("failed to create federated identity credential: %w", err)
	}

	return nil, nil
}
