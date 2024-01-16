// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/rs/zerolog/log"
)

const (
	tygerServerManagedIdentityName     = "tyger-server"
	migrationRunnerManagedIdentityName = "tyger-migration-runner"
)

func createTygerServerManagedIdentity(ctx context.Context) (*armmsi.Identity, error) {
	return createManagedIdentity(ctx, tygerServerManagedIdentityName)
}

func createMigrationRunnerManagedIdentity(ctx context.Context) (*armmsi.Identity, error) {
	return createManagedIdentity(ctx, migrationRunnerManagedIdentityName)
}

func createManagedIdentity(ctx context.Context, name string) (*armmsi.Identity, error) {
	config := GetConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	log.Info().Msgf("Creating or updating managed identity '%s'", name)

	identitiesClient, err := armmsi.NewUserAssignedIdentitiesClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create managed identities client: %w", err)
	}

	resp, err := identitiesClient.CreateOrUpdate(ctx, config.Cloud.ResourceGroup, name, armmsi.Identity{
		Location: &config.Cloud.DefaultLocation,
		Tags: map[string]*string{
			TagKey: &config.EnvironmentName,
		},
	}, nil)

	if err != nil {
		return nil, fmt.Errorf("failed to create managed identity: %w", err)
	}

	return &resp.Identity, nil
}

func createFederatedIdentityCredential(
	ctx context.Context,
	managedIdentityPromise *Promise[*armmsi.Identity],
	clusterPromise *Promise[*armcontainerservice.ManagedCluster],
) (any, error) {
	config := GetConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	cluster, err := clusterPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	mi, err := managedIdentityPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	log.Info().Msg("Creating or updating federated identity credential")

	issuerUrl := *cluster.Properties.OidcIssuerProfile.IssuerURL

	client, err := armmsi.NewFederatedIdentityCredentialsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create federated identity credentials client: %w", err)
	}

	_, err = client.CreateOrUpdate(ctx, config.Cloud.ResourceGroup, *mi.Name, *mi.Name, armmsi.FederatedIdentityCredential{
		Properties: &armmsi.FederatedIdentityCredentialProperties{
			Issuer:  &issuerUrl,
			Subject: Ptr(fmt.Sprintf("system:serviceaccount:%s:%s", TygerNamespace, *mi.Name)),
			Audiences: []*string{
				Ptr("api://AzureADTokenExchange"),
			},
		},
	}, nil)

	if err != nil {
		return nil, fmt.Errorf("failed to create federated identity credential: %w", err)
	}

	return nil, nil
}
