// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/google/uuid"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
	"k8s.io/client-go/rest"
)

const nspApiVersion = "2024-07-01"

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

	dataContributors := []string{*managedIdentity.Properties.PrincipalID}
	if localId := inst.Config.Cloud.Compute.LocalDevelopmentIdentityId; localId != "" {
		dataContributors = append(dataContributors, localId)
	}

	if err := assignRbacRole(ctx, dataContributors, true, *res.ID, "Storage Blob Data Contributor", inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
		return nil, fmt.Errorf("failed to assign storage RBAC role: %w", err)
	}

	if err := configureNetworkSecurityPerimeterForStorageAccount(ctx, inst, res.Account); err != nil {
		return nil, fmt.Errorf("failed to configure network security perimeter for storage account: %w", err)
	}

	return nil, nil
}

func configureNetworkSecurityPerimeterForStorageAccount(ctx context.Context, inst *Installer, storageAccount armstorage.Account) error {
	desiredNsp := inst.Config.Cloud.Storage.NetworkSecurityPerimeter
	if desiredNsp == nil {
		return nil
	}

	storageNspClient, err := armstorage.NewNetworkSecurityPerimeterConfigurationsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create network security perimeter client")
	}

	genericNspClient, err := armresources.NewClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create generic resource client")
	}

	nspFound := false
	desiredNspResourceId := fmt.Sprintf("/subscriptions/%s/resourceGroups/%s/providers/Microsoft.Network/networkSecurityPerimeters/%s", inst.Config.Cloud.SubscriptionID, desiredNsp.NspResourceGroup, desiredNsp.NspName)
	pager := storageNspClient.NewListPager(inst.Config.Cloud.ResourceGroup, *storageAccount.Name, nil)
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			log.Fatal().Err(err).Msg("failed to list network security perimeters")
		}

		for _, existingConfig := range page.Value {
			if *existingConfig.Properties.NetworkSecurityPerimeter.ID == desiredNspResourceId &&
				*existingConfig.Properties.Profile.Name == desiredNsp.Profile &&
				string(*existingConfig.Properties.ResourceAssociation.AccessMode) == desiredNsp.Mode {
				nspFound = true
			} else {
				existingAssociationId := fmt.Sprintf("%s/resourceAssociations/%s", *existingConfig.Properties.NetworkSecurityPerimeter.ID, *existingConfig.Properties.ResourceAssociation.Name)
				log.Warn().Msgf("Deleting existing network security perimeter association '%s'", existingAssociationId)
				poller, err := genericNspClient.BeginDeleteByID(ctx, existingAssociationId, nspApiVersion, nil)
				if err != nil {
					return fmt.Errorf("failed to delete resource '%s': %w", existingAssociationId, err)
				}

				if _, err := poller.PollUntilDone(ctx, nil); err != nil {
					return fmt.Errorf("failed to delete resource '%s': %w", existingAssociationId, err)
				}
			}
		}
	}

	if !nspFound {
		log.Info().Msgf("Creating network security perimeter association for storage account '%s'", *storageAccount.Name)
		name := fmt.Sprintf("%s-%s", *storageAccount.Name, uuid.New().String())
		resourceId := fmt.Sprintf("%s/resourceAssociations/%s", desiredNspResourceId, name)

		resource := armresources.GenericResource{
			Properties: map[string]any{
				"privateLinkResource": map[string]any{
					"id": *storageAccount.ID,
				},
				"profile": map[string]any{
					"id": fmt.Sprintf("%s/profiles/%s", desiredNspResourceId, desiredNsp.Profile),
				},
				"accessMode": desiredNsp.Mode,
			},
		}

		poller, err := genericNspClient.BeginCreateOrUpdateByID(ctx, resourceId, nspApiVersion, resource, nil)
		if err != nil {
			return fmt.Errorf("failed to create network security perimeter association '%s': %w", resourceId, err)
		}

		if _, err := poller.PollUntilDone(ctx, nil); err != nil {
			return fmt.Errorf("failed to create network security perimeter association '%s': %w", resourceId, err)
		}
	}

	return nil
}
