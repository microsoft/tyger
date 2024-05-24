// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
	"k8s.io/client-go/rest"
)

func (inst *Installer) CreateStorageAccount(ctx context.Context,
	storageAccountConfig *StorageAccountConfig,
	restConfigPromise *install.Promise[*rest.Config],
	managedIdentityPromise *install.Promise[*armmsi.Identity],
) (any, error) {

	storageClient, err := armstorage.NewAccountsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create storage client")
	}

	var tags map[string]*string
	if resp, err := storageClient.GetProperties(ctx, inst.Config.Cloud.ResourceGroup, storageAccountConfig.Name, nil); err == nil {
		if existingTag, ok := resp.Tags[TagKey]; ok {
			if *existingTag != inst.Config.EnvironmentName {
				return nil, fmt.Errorf("storage account '%s' is already in use by enrironment '%s'", storageAccountConfig.Name, *existingTag)
			}
			tags = resp.Tags
		}
	}

	if tags == nil {
		tags = make(map[string]*string)
	}
	tags[TagKey] = &inst.Config.EnvironmentName

	parameters := armstorage.AccountCreateParameters{
		Tags:     tags,
		Location: &storageAccountConfig.Location,
		Kind:     Ptr(armstorage.KindStorageV2),
		SKU:      &armstorage.SKU{Name: (*armstorage.SKUName)(&storageAccountConfig.Sku)},
		Properties: &armstorage.AccountPropertiesCreateParameters{
			AllowSharedKeyAccess:   Ptr(false),
			EnableHTTPSTrafficOnly: Ptr(true),
			MinimumTLSVersion:      Ptr(armstorage.MinimumTLSVersionTLS12),
		},
	}

	log.Info().Msgf("Creating or updating storage account '%s'", storageAccountConfig.Name)
	poller, err := storageClient.BeginCreate(ctx, inst.Config.Cloud.ResourceGroup, storageAccountConfig.Name, parameters, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create storage account")
	}

	res, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create storage account")
	}

	managedIdentity, err := managedIdentityPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	log.Info().Msgf("Assigning RBAC role to storage account '%s'", storageAccountConfig.Name)

	if err := assignRbacRole(ctx, inst.Config.Cloud.Compute.GetManagementPrincipalIds(), true, *res.ID, "Reader", inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
		return nil, fmt.Errorf("failed to assign storage RBAC role: %w", err)
	}

	if err := assignRbacRole(ctx, []string{*managedIdentity.Properties.PrincipalID}, true, *res.ID, "Storage Blob Data Contributor", inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
		return nil, fmt.Errorf("failed to assign storage RBAC role: %w", err)
	}

	return nil, nil
}
