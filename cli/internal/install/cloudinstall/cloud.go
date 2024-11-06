// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/fatih/color"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
)

const (
	TagKey = "tyger-environment"
)

func (inst *Installer) InstallCloud(ctx context.Context) (err error) {
	if err := inst.ensureResourceGroupCreated(ctx); err != nil {
		logError(err, "")
		return install.ErrAlreadyLoggedError
	}

	if err := inst.preflightCheck(ctx); err != nil {
		if !errors.Is(err, install.ErrAlreadyLoggedError) {
			logError(err, "")
			return install.ErrAlreadyLoggedError
		}
		return err
	}

	allPromises := inst.createPromises(ctx)
	for _, p := range allPromises {
		if promiseErr := p.AwaitErr(); promiseErr != nil && promiseErr != install.ErrDependencyFailed {
			logError(promiseErr, "")
			err = install.ErrAlreadyLoggedError
		}
	}

	return err
}

func (inst *Installer) UninstallCloud(ctx context.Context) (err error) {
	for _, c := range inst.Config.Cloud.Compute.Clusters {
		if err := inst.onDeleteCluster(ctx, c); err != nil {
			return err
		}
	}

	// See if all the resources in the resource group are from this environment

	resourcesClient, err := armresources.NewClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create resources client: %w", err)
	}

	pager := resourcesClient.NewListByResourceGroupPager(inst.Config.Cloud.ResourceGroup, nil)
	resourcesFromThisEnvironment := make([]*armresources.GenericResourceExpanded, 0)
	resourceGroupContainsOtherResources := false
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) && respErr.ErrorCode == "ResourceGroupNotFound" {
				log.Debug().Msgf("Resource group '%s' not found", inst.Config.Cloud.ResourceGroup)
				return nil
			}
			return fmt.Errorf("failed to list resources: %w", err)
		}
		for _, res := range page.ResourceListResult.Value {
			if envName, ok := res.Tags[TagKey]; ok && *envName == inst.Config.EnvironmentName {
				resourcesFromThisEnvironment = append(resourcesFromThisEnvironment, res)
			} else {
				resourceGroupContainsOtherResources = true
			}
		}
	}

	if !resourceGroupContainsOtherResources {
		log.Info().Msgf("Deleting resource group '%s'", inst.Config.Cloud.ResourceGroup)
		c, err := armresources.NewResourceGroupsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
		if err != nil {
			return fmt.Errorf("failed to create resource groups client: %w", err)
		}
		poller, err := c.BeginDelete(ctx, inst.Config.Cloud.ResourceGroup, nil)
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

	log.Info().Msgf("Resource group '%s' contains resources that are not part of this environment", inst.Config.Cloud.ResourceGroup)

deleteOneByOne:

	providersClient, err := armresources.NewProvidersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create providers client: %w", err)
	}

	pg := install.PromiseGroup{}
	for _, res := range resourcesFromThisEnvironment {
		resourceId := *res.ID
		install.NewPromise(ctx, &pg, func(ctx context.Context) (any, error) {
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
		if promiseErr := p.AwaitErr(); promiseErr != nil && promiseErr != install.ErrDependencyFailed {
			logError(promiseErr, "")
			err = install.ErrAlreadyLoggedError
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

	var apiVersion string
	for _, t := range provider.Provider.ResourceTypes {
		if *t.ResourceType == resourceType {
			if t.DefaultAPIVersion != nil {
				apiVersion = *t.DefaultAPIVersion
			} else {
				// take the most recent non-preview API version
				slices.SortFunc(t.APIVersions, func(a, b *string) int {
					return -strings.Compare(*a, *b)
				})

				for _, v := range t.APIVersions {
					if !strings.Contains(*v, "preview") {
						apiVersion = *v
						break
					}
				}
			}
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

func (inst *Installer) ensureResourceGroupCreated(ctx context.Context) error {
	c, err := armresources.NewResourceGroupsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create resource groups client: %w", err)
	}

	resp, err := c.CheckExistence(ctx, inst.Config.Cloud.ResourceGroup, nil)
	if err != nil {
		return fmt.Errorf("failed to check resource group existence: %w", err)
	}

	if resp.Success {
		return nil
	}

	log.Debug().Msgf("Creating resource group '%s'", inst.Config.Cloud.ResourceGroup)
	_, err = c.CreateOrUpdate(ctx, inst.Config.Cloud.ResourceGroup,
		armresources.ResourceGroup{
			Location: Ptr(inst.Config.Cloud.DefaultLocation),
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

func (inst *Installer) createPromises(ctx context.Context) install.PromiseGroup {
	group := &install.PromiseGroup{}

	var createApiHostClusterPromise *install.Promise[*armcontainerservice.ManagedCluster]

	tygerServerManagedIdentityPromise := install.NewPromise(ctx, group, inst.createTygerServerManagedIdentity)
	migrationRunnerManagedIdentityPromise := install.NewPromise(ctx, group, inst.createMigrationRunnerManagedIdentity)

	customIdentityPromises := make([]*install.Promise[*armmsi.Identity], 0)

	for _, identityName := range inst.Config.Cloud.Compute.Identities {
		identityName := identityName
		customIdentityPromises = append(customIdentityPromises, install.NewPromise(ctx, group, func(ctx context.Context) (*armmsi.Identity, error) {
			return inst.createManagedIdentity(ctx, identityName)
		}))
	}

	install.NewPromise(ctx, group, inst.deleteUnusedIdentities)

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.createDatabase(ctx, tygerServerManagedIdentityPromise, migrationRunnerManagedIdentityPromise)
	})

	for _, clusterConfig := range inst.Config.Cloud.Compute.Clusters {
		createClusterPromise := install.NewPromise(
			ctx,
			group,
			func(ctx context.Context) (*armcontainerservice.ManagedCluster, error) {
				return inst.createCluster(ctx, clusterConfig)
			})
		if clusterConfig.ApiHost {
			createApiHostClusterPromise = createClusterPromise
			install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
				return inst.createFederatedIdentityCredential(ctx, tygerServerManagedIdentityPromise, createClusterPromise)
			})
			install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
				return inst.createFederatedIdentityCredential(ctx, migrationRunnerManagedIdentityPromise, createClusterPromise)
			})
		}

		for _, identityPromise := range customIdentityPromises {
			install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
				return inst.createFederatedIdentityCredential(ctx, identityPromise, createClusterPromise)
			})
		}
	}

	getAdminCredsPromise := install.NewPromiseAfter(ctx, group, inst.getAdminRESTConfig, createApiHostClusterPromise)

	createTygerNamespacePromise := install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return createTygerNamespace(ctx, getAdminCredsPromise)
	})

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.createTygerClusterRBAC(ctx, getAdminCredsPromise, createTygerNamespacePromise)
	})

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.CreateStorageAccount(ctx, inst.Config.Cloud.Storage.Logs, getAdminCredsPromise, tygerServerManagedIdentityPromise)
	})

	for _, buf := range inst.Config.Cloud.Storage.Buffers {
		install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
			return inst.CreateStorageAccount(ctx, buf, getAdminCredsPromise, tygerServerManagedIdentityPromise)
		})
	}

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.installTraefik(ctx, getAdminCredsPromise)
	})

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.installCertManager(ctx, getAdminCredsPromise)
	})

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.installNvidiaDevicePlugin(ctx, getAdminCredsPromise)
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
