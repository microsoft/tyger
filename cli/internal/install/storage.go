package install

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob"
	"github.com/Azure/azure-sdk-for-go/sdk/storage/azblob/bloberror"
	"github.com/rs/zerolog/log"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
)

func CreateStorageAccount(ctx context.Context,
	storageAccountConfig *StorageAccountConfig,
	restConfigPromise *Promise[*rest.Config],
	namespaceCreatedPromise *Promise[any],
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

	log.Info().Msgf("Storage account '%s' ready", storageAccountConfig.Name)

	keysResponse, err := storageClient.ListKeys(ctx, config.Cloud.ResourceGroup, storageAccountConfig.Name, nil)
	if err != nil {
		log.Fatal().Err(err).Msg("failed to get storage account key")
	}
	components := []string{
		"DefaultEndpointsProtocol=https",
		fmt.Sprintf("BlobEndpoint=%s", *res.Properties.PrimaryEndpoints.Blob),
		fmt.Sprintf("AccountName=%s", *res.Name),
		fmt.Sprintf("AccountKey=%s", *keysResponse.Keys[0].Value),
	}

	connectionString := strings.Join(components, ";")

	for _, containerName := range containers {
		blobClient, err := azblob.NewClientFromConnectionString(connectionString, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create blob client: %w", err)
		}

		if _, err := blobClient.CreateContainer(ctx, containerName, nil); err != nil {
			if !bloberror.HasCode(err, bloberror.ContainerAlreadyExists) {
				return nil, fmt.Errorf("failed to create container '%s': %w", containerName, err)
			}
		} else {
			log.Info().Msgf("Created container '%s' on storage account '%s'", containerName, storageAccountConfig.Name)
		}
	}

	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	if _, err := namespaceCreatedPromise.Await(); err != nil {
		return nil, errDependencyFailed
	}

	clientset := kubernetes.NewForConfigOrDie(restConfig)

	secrets := clientset.CoreV1().Secrets("tyger")
	secret := corev1.Secret{
		ObjectMeta: metav1.ObjectMeta{
			Name: *res.Name,
		},
		Type: "Opaque",
		Data: map[string][]byte{
			"connectionString": []byte(connectionString),
		},
	}

	_, err = secrets.Create(ctx, &secret, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			if _, err := secrets.Update(ctx, &secret, metav1.UpdateOptions{}); err != nil {
				return nil, fmt.Errorf("failed to update secret: %w", err)
			}
		} else {
			return nil, fmt.Errorf("failed to create secret: %w", err)
		}
	}

	return nil, nil
}
