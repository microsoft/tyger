// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"errors"
	"fmt"
	"net/http"
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

func (inst *Installer) ApplySingleOrgFilter(specifiedOrg string) error {
	if specifiedOrg == "" {
		switch len(inst.Config.Organizations) {
		case 0:
			return fmt.Errorf("no organizations found in configuration")
		case 1:
			return nil
		default:
			return fmt.Errorf("since the configuration contains multiple organizations, and this command can only apply to a single organization, please specify an organization using the --org flag")
		}
	}

	for _, configuredOrg := range inst.Config.Organizations {
		if strings.EqualFold(specifiedOrg, configuredOrg.Name) {
			inst.Config.Organizations = []*OrganizationConfig{configuredOrg}
			return nil
		}
	}

	return fmt.Errorf("organization '%s' not found in configuration", specifiedOrg)
}

func (inst *Installer) ApplyMultiOrgFilter(specifiedOrgs []string) error {
	orgSpecified := false
	if len(specifiedOrgs) > 0 {
		for _, org := range specifiedOrgs {
			if org != "" {
				orgSpecified = true
				break
			}
		}
	}

	if !orgSpecified {
		return nil
	}

	orgMap := make(map[string]*OrganizationConfig)
	for _, org := range specifiedOrgs {
		if org == "" {
			continue
		}

		found := false
		for _, configuredOrg := range inst.Config.Organizations {
			if strings.EqualFold(org, configuredOrg.Name) {
				orgMap[configuredOrg.Name] = configuredOrg
				found = true
				break
			}
		}

		if !found {
			return fmt.Errorf("organization '%s' not found in configuration", org)
		}
	}

	filteredOrgs := make([]*OrganizationConfig, 0, len(orgMap))
	for _, v := range orgMap {
		filteredOrgs = append(filteredOrgs, v)
	}

	if len(filteredOrgs) == 0 && len(inst.Config.Organizations) == 1 {
		filteredOrgs = append(filteredOrgs, inst.Config.Organizations[0])
	}

	inst.Config.Organizations = filteredOrgs

	return nil
}

func (inst *Installer) InstallCloud(ctx context.Context, skipShared bool) (err error) {
	if !skipShared {
		if err := inst.ensureResourceGroupCreated(ctx, inst.Config.Cloud.ResourceGroup); err != nil {
			logError(ctx, err, "")
			return install.ErrAlreadyLoggedError
		}

		if inst.Config.Cloud.PrivateNetworking {
			for _, cluster := range inst.Config.Cloud.Compute.Clusters {
				if cluster.ExistingSubnet != nil {
					if err := inst.ensureResourceGroupCreated(ctx, cluster.ExistingSubnet.PrivateLinkResourceGroup); err != nil {
						logError(ctx, err, "")
						return install.ErrAlreadyLoggedError
					}
				}
			}
		}

		if err := inst.preflightCheck(ctx); err != nil {
			if !errors.Is(err, install.ErrAlreadyLoggedError) {
				logError(ctx, err, "")
				return install.ErrAlreadyLoggedError
			}
			return err
		}

		sharedPromises := inst.createSharedPromises(ctx)
		for _, p := range sharedPromises {
			if promiseErr := p.AwaitErr(); promiseErr != nil && promiseErr != install.ErrDependencyFailed {
				logError(ctx, promiseErr, "")
				err = install.ErrAlreadyLoggedError
			}
		}
	}

	if err != nil {
		return err
	}

	err = inst.Config.ForEachOrgInParallel(ctx, func(ctx context.Context, org *OrganizationConfig) error {
		return inst.ensureResourceGroupCreated(ctx, org.Cloud.ResourceGroup)
	})
	if err != nil {
		return err
	}

	orgPromises := make([]install.PromiseGroup, 0)
	for _, org := range inst.Config.Organizations {
		ctx := log.Ctx(ctx).With().Str("organization", org.Name).Logger().WithContext(ctx)
		if err := inst.ensureResourceGroupCreated(ctx, org.Cloud.ResourceGroup); err != nil {
			logError(ctx, err, "")
			return install.ErrAlreadyLoggedError
		}

		orgPromises = append(orgPromises, inst.createOrgPromises(ctx, org))
	}

	for _, orgPromise := range orgPromises {
		for _, p := range orgPromise {
			if promiseErr := p.AwaitErr(); promiseErr != nil && promiseErr != install.ErrDependencyFailed {
				logError(ctx, promiseErr, "")
				err = install.ErrAlreadyLoggedError
			}
		}
	}

	return err
}

func (inst *Installer) UninstallOrganization(ctx context.Context, org *OrganizationConfig) (err error) {
	adminRestConfig, err := inst.getAdminRESTConfig(ctx)
	if err != nil {
		return fmt.Errorf("failed to get admin REST config: %w", err)
	}

	if org.Cloud.KubernetesNamespace == "default" {
		// we cannot delete the default namespace, so we need to uninstall Tyger instead
		if err := inst.uninstallTygerSingleOrg(ctx, nil, org); err != nil {
			return fmt.Errorf("failed to uninstall Tyger: %w", err)
		}
	} else {
		if err := deleteKubernetesNamespace(ctx, adminRestConfig, org.Cloud.KubernetesNamespace); err != nil {
			return fmt.Errorf("failed to delete kubernetes namespace: %w", err)
		}
	}

	if _, err := inst.deleteDatabase(ctx, org); err != nil {
		return fmt.Errorf("failed to delete database: %w", err)
	}

	if _, err := inst.deleteDnsRecord(ctx, org); err != nil {
		return fmt.Errorf("failed to delete DNS record: %w", err)
	}

	return inst.safeDeleteResourceGroup(ctx, org.Cloud.ResourceGroup)
}

func (inst *Installer) UninstallCloud(ctx context.Context, all bool) error {
	if !all {
		return inst.Config.ForEachOrgInParallel(ctx, inst.UninstallOrganization)
	}

	err := inst.Config.ForEachOrgInParallel(ctx, func(ctx context.Context, oc *OrganizationConfig) error {
		return inst.safeDeleteResourceGroup(ctx, oc.Cloud.ResourceGroup)
	})

	if err != nil {
		return err
	}

	for _, c := range inst.Config.Cloud.Compute.Clusters {
		if err := inst.onDeleteCluster(ctx, c); err != nil {
			return err
		}
	}

	if inst.Config.Cloud.TlsCertificate != nil && inst.Config.Cloud.TlsCertificate.KeyVault != nil {
		kvIdentityResp, err := inst.getTraefikKeyVaultClientManagedIdentity(ctx)
		if err != nil {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
			} else {
				return fmt.Errorf("failed to get key vault managed identity: %w", err)
			}
		}

		if kvIdentityResp != nil {
			if err := inst.RemoveKeyVaultAccess(ctx, *kvIdentityResp.Properties.PrincipalID); err != nil {
				return fmt.Errorf("failed to remove key vault access: %w", err)
			}
		}

	}

	return inst.safeDeleteResourceGroup(ctx, inst.Config.Cloud.ResourceGroup)
}

func (inst *Installer) safeDeleteResourceGroup(ctx context.Context, resourceGroup string) error {
	// See if all the resources in the resource group are from this environment

	resourcesClient, err := armresources.NewClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create resources client: %w", err)
	}

	pager := resourcesClient.NewListByResourceGroupPager(resourceGroup, nil)
	resourcesFromThisEnvironment := make([]*armresources.GenericResourceExpanded, 0)
	resourceGroupContainsOtherResources := false
	for pager.More() {
		page, err := pager.NextPage(ctx)
		if err != nil {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) && respErr.ErrorCode == "ResourceGroupNotFound" {
				log.Debug().Msgf("Resource group '%s' not found", resourceGroup)
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
		log.Ctx(ctx).Info().Msgf("Deleting resource group '%s'", resourceGroup)
		c, err := armresources.NewResourceGroupsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
		if err != nil {
			return fmt.Errorf("failed to create resource groups client: %w", err)
		}
		poller, err := c.BeginDelete(ctx, resourceGroup, nil)
		if err != nil {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) && respErr.ErrorCode == "AuthorizationFailed" {
				log.Ctx(ctx).Info().Msg("Insufficient permisssions to delete resource group. Deleting resources individually instead")
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

	log.Ctx(ctx).Info().Msgf("Resource group '%s' contains resources that are not part of this environment", resourceGroup)

deleteOneByOne:

	providersClient, err := armresources.NewProvidersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create providers client: %w", err)
	}

	pg := install.PromiseGroup{}
	for _, res := range resourcesFromThisEnvironment {
		resourceId := *res.ID
		install.NewPromise(ctx, &pg, func(ctx context.Context) (any, error) {
			log.Ctx(ctx).Info().Msgf("Deleting resource '%s'", resourceId)

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
			log.Ctx(ctx).Info().Msgf("Deleted resource '%s'", resourceId)
			return nil, nil
		})
	}

	for _, p := range pg {
		if promiseErr := p.AwaitErr(); promiseErr != nil && promiseErr != install.ErrDependencyFailed {
			logError(ctx, promiseErr, "")
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

func (inst *Installer) ensureResourceGroupCreated(ctx context.Context, name string) error {
	c, err := armresources.NewResourceGroupsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create resource groups client: %w", err)
	}

	resp, err := c.CheckExistence(ctx, name, nil)
	if err != nil {
		return fmt.Errorf("failed to check resource group existence: %w", err)
	}

	if resp.Success {
		return nil
	}

	log.Debug().Msgf("Creating resource group '%s'", name)
	_, err = c.CreateOrUpdate(ctx, name,
		armresources.ResourceGroup{
			Location: Ptr(inst.Config.Cloud.DefaultLocation),
		}, nil)

	if err != nil {
		return fmt.Errorf("failed to create resource group: %w", err)
	}

	return nil
}

func logError(ctx context.Context, err error, msg string) {
	errorString := err.Error()
	if strings.Contains(errorString, "\n") {
		if msg == "" {
			msg = "Encountered error:"
		}

		log.Ctx(ctx).Error().Msg(msg)
		color.New(color.FgRed).FprintfFunc()(os.Stderr, "Error: %s", err.Error())
	} else {
		log.Ctx(ctx).Error().Err(err).Msg(msg)
	}
}

func (inst *Installer) createSharedPromises(ctx context.Context) install.PromiseGroup {
	group := &install.PromiseGroup{}

	var createApiHostClusterPromise *install.Promise[*armcontainerservice.ManagedCluster]

	var traefikKeyVaultClientManagedIdentityPromise *install.Promise[*armmsi.Identity]
	if inst.Config.Cloud.TlsCertificate != nil {
		traefikKeyVaultClientManagedIdentityPromise = install.NewPromise(ctx, group, inst.createTraefikKeyVaultClientManagedIdentity)
	}

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.createDatabaseServer(ctx)
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
		}
	}

	if traefikKeyVaultClientManagedIdentityPromise != nil {
		install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
			return inst.createFederatedIdentityCredential(ctx, traefikKeyVaultClientManagedIdentityPromise, createApiHostClusterPromise, TraefikNamespace)
		})

		install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
			kvClient, err := traefikKeyVaultClientManagedIdentityPromise.Await()
			if err != nil {
				return nil, install.ErrDependencyFailed
			}

			return nil, inst.GrantAccessToKeyVault(ctx, *kvClient.Properties.PrincipalID)
		})
	}

	getAdminCredsPromise := install.NewPromiseAfter(ctx, group, inst.getAdminRESTConfig, createApiHostClusterPromise)

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.createClusterRBAC(ctx, getAdminCredsPromise)
	})

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		if _, err := createKubernetesNamespace(ctx, getAdminCredsPromise, TraefikNamespace); err != nil {
			return nil, fmt.Errorf("failed to create traefik namespace: %w", err)
		}

		if traefikKeyVaultClientManagedIdentityPromise != nil {
			if err := inst.addSecretProviderClass(ctx, TraefikNamespace, traefikKeyVaultClientManagedIdentityPromise, getAdminCredsPromise); err != nil {
				return nil, err
			}
		}

		if _, err := inst.installTraefik(ctx, getAdminCredsPromise, traefikKeyVaultClientManagedIdentityPromise); err != nil {
			return nil, fmt.Errorf("failed to install Traefik: %w", err)
		}

		if inst.Config.Cloud.PrivateNetworking {
			cluster, err := createApiHostClusterPromise.Await()
			if err != nil {
				return nil, install.ErrDependencyFailed
			}
			if err := inst.createPrivateEndpointsForTraefik(ctx, cluster); err != nil {
				return nil, fmt.Errorf("failed to create private endpoints for Traefik: %w", err)
			}
		}

		return nil, nil
	})

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.installCertManager(ctx, getAdminCredsPromise)
	})

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.installNvidiaDevicePlugin(ctx, getAdminCredsPromise)
	})

	return *group
}

func (inst *Installer) createOrgPromises(ctx context.Context, org *OrganizationConfig) install.PromiseGroup {
	group := &install.PromiseGroup{}

	tygerServerManagedIdentityPromise := install.NewPromise(ctx, group, func(ctx context.Context) (*armmsi.Identity, error) {
		return inst.createTygerServerManagedIdentity(ctx, org)
	})

	migrationRunnerManagedIdentityPromise := install.NewPromise(ctx, group, func(ctx context.Context) (*armmsi.Identity, error) {
		return inst.createMigrationRunnerManagedIdentity(ctx, org)
	})

	customIdentityPromises := make([]*install.Promise[*armmsi.Identity], 0)

	for _, identityName := range org.Cloud.Identities {
		identityName := identityName
		customIdentityPromises = append(customIdentityPromises, install.NewPromise(ctx, group, func(ctx context.Context) (*armmsi.Identity, error) {
			return inst.createManagedIdentity(ctx, identityName, org.Cloud.ResourceGroup)
		}))
	}

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) { return inst.deleteUnusedIdentities(ctx, org) })

	var getApiHostClusterPromise *install.Promise[*armcontainerservice.ManagedCluster]
	for _, clusterConfig := range inst.Config.Cloud.Compute.Clusters {
		getClusterPromise := install.NewPromise(
			ctx,
			group,
			func(ctx context.Context) (*armcontainerservice.ManagedCluster, error) {
				return inst.getCluster(ctx, clusterConfig)
			})
		if clusterConfig.ApiHost {
			getApiHostClusterPromise = getClusterPromise
			install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
				return inst.createFederatedIdentityCredential(ctx, tygerServerManagedIdentityPromise, getClusterPromise, org.Cloud.KubernetesNamespace)
			})
			install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
				return inst.createFederatedIdentityCredential(ctx, migrationRunnerManagedIdentityPromise, getClusterPromise, org.Cloud.KubernetesNamespace)
			})
		}

		for _, identityPromise := range customIdentityPromises {
			install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
				return inst.createFederatedIdentityCredential(ctx, identityPromise, getClusterPromise, org.Cloud.KubernetesNamespace)
			})
		}
	}

	getAdminCredsPromise := install.NewPromiseAfter(ctx, group, inst.getAdminRESTConfig, getApiHostClusterPromise)

	createNamespacePromise := install.NewPromise(ctx, group, func(ctx context.Context) (string, error) {
		return createKubernetesNamespace(ctx, getAdminCredsPromise, org.Cloud.KubernetesNamespace)
	})

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.createNamespaceRBAC(ctx, getAdminCredsPromise, createNamespacePromise)
	})

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.CreateStorageAccount(ctx, org.Cloud.ResourceGroup, org.Cloud.Storage.Logs, tygerServerManagedIdentityPromise)
	})

	for _, buf := range org.Cloud.Storage.Buffers {
		install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
			return inst.CreateStorageAccount(ctx, org.Cloud.ResourceGroup, buf, tygerServerManagedIdentityPromise)
		})
	}

	install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return inst.createDatabase(ctx, org, tygerServerManagedIdentityPromise, migrationRunnerManagedIdentityPromise)
	})

	if !strings.HasSuffix(org.Api.DomainName, BuiltInDomainNameSuffix) {
		install.NewPromise(ctx, group, func(ctx context.Context) (any, error) {
			return inst.assignDnsRecord(ctx, org)
		})
	}

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

func getResourceGroupFromID(resourceID string) string {
	// Split the resource ID into parts
	parts := strings.Split(resourceID, "/")
	for i, part := range parts {
		if strings.EqualFold(part, "resourceGroups") && i+1 < len(parts) {
			return parts[i+1]
		}
	}

	panic(fmt.Errorf("resource group not found in resource ID: %s", resourceID))
}

func Ptr[T any](t T) *T {
	return &t
}
