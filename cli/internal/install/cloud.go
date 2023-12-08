package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/fatih/color"
	"github.com/rs/zerolog/log"
)

const (
	TagKey = "tyger-environment"
)

var (
	ErrAlreadyLoggedError = errors.New("already logged error")
	errDependencyFailed   = errors.New("dependency failed")
)

func InstallCloud(ctx context.Context) (err error) {
	config := GetConfigFromContext(ctx)

	if err := ensureResourceGroupCreated(ctx); err != nil {
		logError(err, "")
		return ErrAlreadyLoggedError
	}

	if err := preflightCheck(ctx); err != nil {
		if err != ErrAlreadyLoggedError {
			logError(err, "")
			return ErrAlreadyLoggedError
		}
		return err
	}

	allPromises := createPromises(ctx, config)
	for _, p := range allPromises {
		if promiseErr := p.AwaitErr(); promiseErr != nil && promiseErr != errDependencyFailed {
			logError(promiseErr, "")
			err = ErrAlreadyLoggedError
		}
	}

	return err
}

func UninstallCloud(ctx context.Context) (err error) {
	config := GetConfigFromContext(ctx)
	for _, c := range config.Cloud.Compute.Clusters {
		if err := onDeleteCluster(ctx, c); err != nil {
			return err
		}
	}

	cred := GetAzureCredentialFromContext(ctx)

	// See if all the resources in the resource group are from this environment

	resourcesClient, err := armresources.NewClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create resources client: %w", err)
	}

	pager := resourcesClient.NewListByResourceGroupPager(config.Cloud.ResourceGroup, nil)
	resourcesFromThisEnvironment := make([]*armresources.GenericResourceExpanded, 0)
	resourceGroupContainsOtherResources := false
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) && respErr.ErrorCode == "ResourceGroupNotFound" {
				log.Debug().Msgf("Resource group '%s' not found", config.Cloud.ResourceGroup)
				return nil
			}
			return fmt.Errorf("failed to list resources: %w", err)
		}
		for _, res := range page.ResourceListResult.Value {
			if envName, ok := res.Tags[TagKey]; ok && *envName == config.EnvironmentName {
				resourcesFromThisEnvironment = append(resourcesFromThisEnvironment, res)
			} else {
				resourceGroupContainsOtherResources = true
			}
		}
	}

	if !resourceGroupContainsOtherResources {
		log.Info().Msgf("Deleting resource group '%s'", config.Cloud.ResourceGroup)
		c, err := armresources.NewResourceGroupsClient(config.Cloud.SubscriptionID, cred, nil)
		if err != nil {
			return fmt.Errorf("failed to create resource groups client: %w", err)
		}
		poller, err := c.BeginDelete(ctx, config.Cloud.ResourceGroup, nil)
		if err != nil {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) && respErr.ErrorCode == "AuthorizationFailed" {
				log.Info().Msg("Insufficient permisssions to delete resource group. Deleting resources individually instead")
				goto deleteOneByOne
			}
			return fmt.Errorf("failed to delete resource group: %w", err)
		}

		_, err = poller.PollUntilDone(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to delete resource group: %w", err)
		}

		return nil
	}

	log.Info().Msgf("Resource group '%s' contains resources that are not part of this environment", config.Cloud.ResourceGroup)

deleteOneByOne:

	providersClient, err := armresources.NewProvidersClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create providers client: %w", err)
	}

	pg := PromiseGroup{}
	for _, res := range resourcesFromThisEnvironment {
		resourceId := *res.ID
		NewPromise(ctx, &pg, func(ctx context.Context) (any, error) {
			log.Info().Msgf("Deleting resource '%s'", resourceId)

			apiVersion, err := GetDefaultApiVersionForResource(ctx, resourceId, providersClient)
			if err != nil {
				return nil, err
			}

			poller, err := resourcesClient.BeginDeleteByID(ctx, resourceId, apiVersion, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to delete resource '%s': %w", resourceId, err)
			}

			if _, err := poller.PollUntilDone(ctx, nil); err != nil {
				return nil, fmt.Errorf("failed to delete resource '%s': %w", resourceId, err)
			}
			log.Info().Msgf("Deleted resource '%s'", resourceId)
			return nil, nil
		})
	}

	for _, p := range pg {
		if promiseErr := p.AwaitErr(); promiseErr != nil && promiseErr != errDependencyFailed {
			logError(promiseErr, "")
			err = ErrAlreadyLoggedError
		}
	}

	return err
}

func GetDefaultApiVersionForResource(ctx context.Context, resourceId string, providersClient *armresources.ProvidersClient) (string, error) {
	providerNamespace, resourceType, err := getProviderNamespaceAndResourceType(resourceId)
	if err != nil {
		return "", fmt.Errorf("failed to get provider namespace and resource type from resource ID '%s': %w", resourceId, err)
	}
	provider, err := providersClient.Get(ctx, providerNamespace, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get resource provider for namespace '%s': %w", providerNamespace, err)
	}
	if err != nil {
		return "", fmt.Errorf("failed to get provider namespace from resource ID '%s': %w", resourceId, err)
	}
	var apiVersion string
	for _, t := range provider.Provider.ResourceTypes {
		if *t.ResourceType == resourceType {
			apiVersion = *t.DefaultAPIVersion
			break
		}
	}
	if apiVersion == "" {
		return "", fmt.Errorf("failed to find API version for resource type '%s'", resourceType)
	}
	return apiVersion, nil
}

func getProviderNamespaceAndResourceType(resourceID string) (string, string, error) {
	parts := strings.Split(resourceID, "/")
	for i, part := range parts {
		if part == "providers" && len(parts) > i+1 {
			namespace := parts[i+1]
			resourceType := strings.Join(parts[i+2:len(parts)-1], "/")
			return namespace, resourceType, nil
		}
	}
	return "", "", fmt.Errorf("provider namespace not found in resource ID")
}

func ensureResourceGroupCreated(ctx context.Context) error {
	config := GetConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	c, err := armresources.NewResourceGroupsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create resource groups client: %w", err)
	}

	resp, err := c.CheckExistence(ctx, config.Cloud.ResourceGroup, nil)
	if err != nil {
		return fmt.Errorf("failed to check resource group existence: %w", err)
	}

	if resp.Success {
		return nil
	}

	log.Debug().Msgf("Creating resource group '%s'", config.Cloud.ResourceGroup)
	_, err = c.CreateOrUpdate(ctx, config.Cloud.ResourceGroup,
		armresources.ResourceGroup{
			Location: Ptr(config.Cloud.DefaultLocation),
		}, nil)

	if err != nil {
		return fmt.Errorf("failed to create resource group: %w", err)
	}

	return nil
}

func logError(err error, msg string) {
	errorString := err.Error()
	if strings.Contains(errorString, "\n") {
		if msg == "" {
			msg = "Encountered error:"
		}

		log.Error().Msg(msg)
		color.New(color.FgRed).FprintfFunc()(os.Stderr, "Error: %s", err.Error())
	} else {
		log.Error().Err(err).Msg(msg)
	}
}

func createPromises(ctx context.Context, config *EnvironmentConfig) PromiseGroup {
	group := &PromiseGroup{}

	var createApiHostClusterPromise *Promise[*armcontainerservice.ManagedCluster]

	managedIdentityPromise := NewPromise(ctx, group, createTygerManagedIdentity)

	for _, clusterConfig := range config.Cloud.Compute.Clusters {
		createClusterPromise := NewPromise(
			ctx,
			group,
			func(ctx context.Context) (*armcontainerservice.ManagedCluster, error) {
				return createCluster(ctx, clusterConfig)
			})
		if clusterConfig.ApiHost {
			createApiHostClusterPromise = createClusterPromise
			NewPromise(ctx, group, func(ctx context.Context) (any, error) {
				return createFederatedIdentityCredential(ctx, managedIdentityPromise, createClusterPromise)
			})
		}
	}

	getAdminCredsPromise := NewPromiseAfter(ctx, group, getAdminRESTConfig, createApiHostClusterPromise)

	createTygerNamespacePromise := NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return createTygerNamespace(ctx, getAdminCredsPromise)
	})

	NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return createTygerClusterRBAC(ctx, getAdminCredsPromise, createTygerNamespacePromise)
	})

	NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return CreateStorageAccount(ctx, config.Cloud.Storage.Logs, getAdminCredsPromise, managedIdentityPromise)
	})

	for _, buf := range config.Cloud.Storage.Buffers {
		NewPromise(ctx, group, func(ctx context.Context) (any, error) {
			return CreateStorageAccount(ctx, buf, getAdminCredsPromise, managedIdentityPromise)
		})
	}

	NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return installTraefik(ctx, getAdminCredsPromise)
	})

	NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return installCertManager(ctx, getAdminCredsPromise)
	})

	NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return installNvidiaDevicePlugin(ctx, getAdminCredsPromise)
	})

	return *group
}

func GetSubscriptionId(ctx context.Context, subName string, cred azcore.TokenCredential) (string, error) {
	lowerSubName := strings.ToLower(subName)
	c, err := armsubscriptions.NewClient(cred, nil)
	if err != nil {
		return "", err
	}

	pager := c.NewListPager(nil)
	for subId := ""; pager.More() && subId == ""; {
		p, err := pager.NextPage(ctx)
		if err != nil {
			return "", err
		}
		for _, s := range p.Value {
			if strings.ToLower(*s.DisplayName) == lowerSubName {
				return *s.SubscriptionID, nil
			}
		}
	}

	return "", fmt.Errorf("subscription with name '%s' not found", subName)
}

func Ptr[T any](t T) *T {
	return &t
}
