package install

import (
	"context"
	"fmt"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/rs/zerolog/log"
	"k8s.io/client-go/rest"
)

func CreateStorageAccount(ctx context.Context,
	storageAccountConfig *StorageAccountConfig,
	restConfigPromise *Promise[*rest.Config],
	managedIdentityPromise *Promise[*armmsi.Identity],
	containers ...string,
) (any, error) {
	config := GetConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	storageClient, err := armstorage.NewAccountsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create storage client")
	}

	var tags map[string]*string
	if resp, err := storageClient.GetProperties(ctx, config.Cloud.ResourceGroup, storageAccountConfig.Name, nil); err == nil {
		if existingTag, ok := resp.Tags[TagKey]; ok {
			if *existingTag != config.EnvironmentName {
				return nil, fmt.Errorf("storage account '%s' is already in use by enrironment '%s'", storageAccountConfig.Name, *existingTag)
			}
			tags = resp.Tags
		}
	}

	if tags == nil {
		tags = make(map[string]*string)
	}
	tags[TagKey] = &config.EnvironmentName

	parameters := armstorage.AccountCreateParameters{
		Tags:       tags,
		Location:   &storageAccountConfig.Location,
		Kind:       Ptr(armstorage.KindStorageV2),
		SKU:        &armstorage.SKU{Name: (*armstorage.SKUName)(&storageAccountConfig.Sku)},
		Properties: &armstorage.AccountPropertiesCreateParameters{},
	}

	log.Info().Msgf("Creating or updating storage account '%s'", storageAccountConfig.Name)
	poller, err := storageClient.BeginCreate(ctx, config.Cloud.ResourceGroup, storageAccountConfig.Name, parameters, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create storage account")
	}

	res, err := poller.PollUntilDone(ctx, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to create storage account")
	}

	managedIdentity, err := managedIdentityPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	log.Info().Msgf("Assigning RBAC role to storage account '%s'", storageAccountConfig.Name)

	if err := assignRbacRole(ctx, *managedIdentity.Properties.PrincipalID, *res.ID, "Storage Blob Data Contributor", config.Cloud.SubscriptionID, cred); err != nil {
		return nil, fmt.Errorf("failed to assign storage RBAC role: %w", err)
	}

	return nil, nil
}
