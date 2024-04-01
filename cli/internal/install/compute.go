// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/operationalinsights/armoperationalinsights"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/rs/zerolog/log"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const DefaultKubernetesVersion = "1.27" // LTS

func createCluster(ctx context.Context, clusterConfig *ClusterConfig) (*armcontainerservice.ManagedCluster, error) {
	config := GetCloudEnvironmentConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	clustersClient, err := armcontainerservice.NewManagedClustersClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create clusters client: %w", err)
	}

	var poller *runtime.Poller[armcontainerservice.ManagedClustersClientCreateOrUpdateResponse]
	var tags map[string]*string

	var clusterAlreadyExists bool
	existingCluster, err := clustersClient.Get(ctx, config.Cloud.ResourceGroup, clusterConfig.Name, nil)
	if err == nil {
		clusterAlreadyExists = true
		if existingTag, ok := existingCluster.Tags[TagKey]; ok {
			if *existingTag != config.EnvironmentName {
				return nil, fmt.Errorf("cluster '%s' is already in use by enrironment '%s'", *existingCluster.Name, *existingTag)
			}
			tags = existingCluster.Tags
		}
	} else {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
			clusterAlreadyExists = false
		} else {
			return nil, fmt.Errorf("failed to get cluster: %w", err)
		}
	}

	if tags == nil {
		tags = make(map[string]*string)
	}
	tags[TagKey] = &config.EnvironmentName

	cluster := armcontainerservice.ManagedCluster{
		Tags:     tags,
		Location: Ptr(clusterConfig.Location),
		Identity: &armcontainerservice.ManagedClusterIdentity{
			Type: Ptr(armcontainerservice.ResourceIdentityTypeSystemAssigned),
		},
		Properties: &armcontainerservice.ManagedClusterProperties{
			DNSPrefix:         Ptr(getClusterDnsPrefix(config.EnvironmentName, clusterConfig.Name, config.Cloud.SubscriptionID)),
			KubernetesVersion: &clusterConfig.KubernetesVersion,
			EnableRBAC:        Ptr(true),
			AADProfile: &armcontainerservice.ManagedClusterAADProfile{
				Managed:         Ptr(true),
				EnableAzureRBAC: Ptr(false),
			},
			OidcIssuerProfile: &armcontainerservice.ManagedClusterOIDCIssuerProfile{
				Enabled: Ptr(true),
			},
			SecurityProfile: &armcontainerservice.ManagedClusterSecurityProfile{
				WorkloadIdentity: &armcontainerservice.ManagedClusterSecurityProfileWorkloadIdentity{
					Enabled: Ptr(true),
				},
			},
		},
	}

	if workspace := config.Cloud.LogAnalyticsWorkspace; workspace != nil {
		oic, err := armoperationalinsights.NewWorkspacesClient(config.Cloud.SubscriptionID, cred, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create operational insights client: %w", err)
		}

		resp, err := oic.Get(ctx, workspace.ResourceGroup, workspace.Name, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to get Log Analytics workspace: %w", err)
		}

		if cluster.Properties.AddonProfiles == nil {
			cluster.Properties.AddonProfiles = make(map[string]*armcontainerservice.ManagedClusterAddonProfile)
		}
		cluster.Properties.AddonProfiles["omsagent"] = &armcontainerservice.ManagedClusterAddonProfile{
			Enabled: Ptr(true),
			Config: map[string]*string{
				"logAnalyticsWorkspaceResourceID": resp.ID,
			},
		}

	}

	cluster.Properties.AgentPoolProfiles = []*armcontainerservice.ManagedClusterAgentPoolProfile{
		{
			Name:              Ptr("system"),
			Mode:              Ptr(armcontainerservice.AgentPoolModeSystem),
			VMSize:            Ptr("Standard_DS2_v2"),
			EnableAutoScaling: Ptr(true),
			Count:             Ptr(int32(1)),
			MinCount:          Ptr(int32(1)),
			MaxCount:          Ptr(int32(3)),
			OSType:            Ptr(armcontainerservice.OSTypeLinux),
			OSSKU:             Ptr(armcontainerservice.OSSKUAzureLinux),
		},
	}

	for _, np := range clusterConfig.UserNodePools {
		profile := armcontainerservice.ManagedClusterAgentPoolProfile{
			Name:              &np.Name,
			Mode:              Ptr(armcontainerservice.AgentPoolModeUser),
			VMSize:            &np.VMSize,
			EnableAutoScaling: Ptr(true),
			Count:             &np.MinCount,
			MinCount:          &np.MinCount,
			MaxCount:          &np.MaxCount,
			OSType:            Ptr(armcontainerservice.OSTypeLinux),
			OSSKU:             Ptr(armcontainerservice.OSSKUAzureLinux),
			NodeLabels: map[string]*string{
				"tyger": Ptr("run"),
			},
			NodeTaints: []*string{
				Ptr("tyger=run:NoSchedule"),
			},
		}

		if clusterAlreadyExists {
			for _, existingNp := range existingCluster.Properties.AgentPoolProfiles {
				if *existingNp.Name == np.Name {
					profile.Count = existingNp.Count
					break
				}
			}
		}

		if strings.Contains(strings.ToLower(np.VMSize), "standard_n") {
			profile.NodeTaints = append(profile.NodeTaints, Ptr("sku=gpu:NoSchedule"))
		}

		cluster.Properties.AgentPoolProfiles = append(cluster.Properties.AgentPoolProfiles, &profile)
	}

	var needsUpdate bool
	var onlyScaleDown bool
	if clusterAlreadyExists {
		needsUpdate, onlyScaleDown = clusterNeedsUpdating(cluster, existingCluster.ManagedCluster)
	} else {
		needsUpdate = true
	}

	if needsUpdate {
		if clusterAlreadyExists {
			log.Info().Msgf("Updating cluster '%s'", clusterConfig.Name)
		} else {
			log.Info().Msgf("Creating cluster '%s'", clusterConfig.Name)
		}

		poller, err = clustersClient.BeginCreateOrUpdate(ctx, config.Cloud.ResourceGroup, clusterConfig.Name, cluster, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create cluster: %w", err)
		}

		if onlyScaleDown {
			log.Info().Msgf("Cluster '%s' only needs to scale down", clusterConfig.Name)
			// we won't wait for the operation to complete
			poller = nil
		}
	} else {
		log.Info().Msgf("Cluster '%s' already exists and appears to be up to date", clusterConfig.Name)
	}

	var kubeletObjectId string
	for ; ; time.Sleep(10 * time.Second) {
		getResp, err := clustersClient.Get(ctx, config.Cloud.ResourceGroup, clusterConfig.Name, nil)
		if err != nil {
			return nil, err
		}

		if getResp.Properties.IdentityProfile != nil {
			if kubeletIdentity := getResp.Properties.IdentityProfile["kubeletidentity"]; kubeletIdentity != nil {
				kubeletObjectId = *kubeletIdentity.ObjectID
				break
			}
		}
	}

	for _, containerRegistry := range config.Cloud.Compute.PrivateContainerRegistries {
		log.Info().Msgf("Attaching ACR '%s' to cluster '%s'", containerRegistry, clusterConfig.Name)
		containerRegistryId, err := getContainerRegistryId(ctx, containerRegistry, config.Cloud.SubscriptionID, cred)
		if err != nil {
			return nil, err
		}

		if err := attachAcr(ctx, kubeletObjectId, containerRegistryId, config.Cloud.SubscriptionID, cred); err != nil {
			return nil, fmt.Errorf("failed to attach ACR: %w", err)
		}
	}

	if poller != nil {
		if !poller.Done() {
			log.Info().Msgf("Waiting for cluster '%s' to be ready", clusterConfig.Name)
		}
		r, err := poller.PollUntilDone(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create cluster '%s': %w", clusterConfig.Name, err)
		}
		log.Info().Msgf("Cluster '%s' ready", clusterConfig.Name)

		return &r.ManagedCluster, nil
	}

	return &existingCluster.ManagedCluster, nil
}

func clusterNeedsUpdating(cluster, existingCluster armcontainerservice.ManagedCluster) (hasChanges bool, onlyScaleDown bool) {
	if *existingCluster.Properties.ProvisioningState != "Succeeded" {
		return true, false
	}

	onlyScaleDown = true
	if *cluster.Properties.KubernetesVersion != *existingCluster.Properties.KubernetesVersion {
		return true, false
	}

	if len(cluster.Tags) != len(existingCluster.Tags) {
		return true, false
	}

	for k, v := range cluster.Tags {
		existingV, ok := existingCluster.Tags[k]
		if !ok {
			return true, false
		}
		if *v != *existingV {
			return true, false
		}
	}

	if len(cluster.Properties.AgentPoolProfiles) != len(existingCluster.Properties.AgentPoolProfiles) {
		return true, false
	}

	for _, np := range cluster.Properties.AgentPoolProfiles {
		found := false
		for _, existingNp := range existingCluster.Properties.AgentPoolProfiles {
			if *np.Name == *existingNp.Name {
				found = true
				if *np.VMSize != *existingNp.VMSize {
					return true, false
				}
				if *np.MinCount != *existingNp.MinCount {
					hasChanges = true
					if *np.MinCount > *existingNp.MinCount {
						onlyScaleDown = false
					}
				}
				if *np.MaxCount != *existingNp.MaxCount {
					hasChanges = true
					if *np.MaxCount > *existingNp.MaxCount {
						onlyScaleDown = false
					}
				}
				break
			}
		}
		if !found {
			return true, false
		}
	}

	if len(cluster.Properties.AddonProfiles) != len(existingCluster.Properties.AddonProfiles) {
		return true, false
	}

	for k, v := range cluster.Properties.AddonProfiles {
		existingV, ok := existingCluster.Properties.AddonProfiles[k]
		if !ok {
			return true, false
		}
		if *v.Enabled != *existingV.Enabled {
			return true, false
		}
		if len(v.Config) != len(existingV.Config) {
			return true, false
		}
		for k2, v2 := range v.Config {
			existingV2, ok := existingV.Config[k2]
			if !ok {
				return true, false
			}
			if *v2 != *existingV2 {
				return true, false
			}
		}
	}

	if existingCluster.Properties.OidcIssuerProfile == nil || existingCluster.Properties.OidcIssuerProfile.Enabled == nil || !*existingCluster.Properties.OidcIssuerProfile.Enabled {
		return true, false
	}

	if existingCluster.Properties.SecurityProfile == nil || existingCluster.Properties.SecurityProfile.WorkloadIdentity == nil || !*existingCluster.Properties.SecurityProfile.WorkloadIdentity.Enabled {
		return true, false
	}

	return hasChanges, onlyScaleDown
}

func getClusterDnsPrefix(environmentName, clusterName, subId string) string {
	return fmt.Sprintf("%s-tyger-%s", regexp.MustCompile("[^a-zA-Z0-9-]").ReplaceAllString(environmentName+"-"+clusterName, ""), subId[0:8])
}

func attachAcr(ctx context.Context, kubeletObjectId, containerRegistryId, subscriptionId string, credential azcore.TokenCredential) error {
	return assignRbacRole(ctx, kubeletObjectId, containerRegistryId, "AcrPull", subscriptionId, credential)
}

func detachAcr(ctx context.Context, kubeletObjectId, containerRegistryId, subscriptionId string, credential azcore.TokenCredential) error {
	return removeRbacRoleAssignments(ctx, kubeletObjectId, containerRegistryId, subscriptionId, credential)
}

func getContainerRegistryId(ctx context.Context, name string, subscriptionId string, credential azcore.TokenCredential) (string, error) {
	resourceClient, err := armresources.NewClient(subscriptionId, credential, nil)
	if err != nil {
		return "", err
	}
	pager := resourceClient.NewListPager(&armresources.ClientListOptions{
		Filter: Ptr(fmt.Sprintf("resourceType eq 'Microsoft.ContainerRegistry/registries' and name eq '%s'", name)),
	})

	for pager.More() {
		p, err := pager.NextPage(ctx)
		if err != nil {
			return "", err
		}
		for _, s := range p.Value {
			return *s.ID, nil
		}
	}

	return "", fmt.Errorf("container registry '%s' not found in subscription", name)
}

func onDeleteCluster(ctx context.Context, clusterConfig *ClusterConfig) error {
	config := GetCloudEnvironmentConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	clustersClient, err := armcontainerservice.NewManagedClustersClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create clusters client: %w", err)
	}

	clusterResponse, err := clustersClient.Get(ctx, config.Cloud.ResourceGroup, clusterConfig.Name, nil)
	if err != nil {
		var respErr *azcore.ResponseError
		if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
			return nil
		}
		return err
	}

	if clusterResponse.Properties.IdentityProfile == nil {
		return nil
	}

	kubeletIdentity := clusterResponse.Properties.IdentityProfile["kubeletidentity"]
	if kubeletIdentity == nil {
		return nil
	}

	kubeletObjectId := *kubeletIdentity.ObjectID

	for _, containerRegistry := range config.Cloud.Compute.PrivateContainerRegistries {
		log.Info().Msgf("Detaching ACR '%s' from cluster '%s'", containerRegistry, clusterConfig.Name)
		containerRegistryId, err := getContainerRegistryId(ctx, containerRegistry, config.Cloud.SubscriptionID, cred)
		if err != nil {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
				continue
			}
			return err
		}

		if err := detachAcr(ctx, kubeletObjectId, containerRegistryId, config.Cloud.SubscriptionID, cred); err != nil {
			return fmt.Errorf("failed to detatch ACR: %w", err)
		}
	}

	return nil
}

func getAdminRESTConfig(ctx context.Context) (*rest.Config, error) {
	config := GetCloudEnvironmentConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	clustersClient, err := armcontainerservice.NewManagedClustersClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create clusters client: %w", err)
	}

	credResp, err := clustersClient.ListClusterAdminCredentials(ctx, config.Cloud.ResourceGroup, config.Cloud.Compute.GetApiHostCluster().Name, nil)
	if err != nil {
		return nil, err
	}

	return clientcmd.RESTConfigFromKubeConfig(credResp.Kubeconfigs[0].Value)
}

func GetUserRESTConfig(ctx context.Context) (*rest.Config, error) {
	config := GetCloudEnvironmentConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	clustersClient, err := armcontainerservice.NewManagedClustersClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create clusters client: %w", err)
	}

	credResp, err := clustersClient.ListClusterUserCredentials(ctx, config.Cloud.ResourceGroup, config.Cloud.Compute.GetApiHostCluster().Name, nil)
	if err != nil {
		return nil, err
	}

	kubeConfig, err := clientcmd.Load(credResp.Kubeconfigs[0].Value)
	if err != nil {
		return nil, err
	}

	// get a token and update the kubeconfig so that kubelogin does not need to be installed
	authInfo := kubeConfig.AuthInfos[kubeConfig.Contexts[kubeConfig.CurrentContext].AuthInfo]
	var serverId string
	for i, v := range authInfo.Exec.Args {
		if v == "--server-id" {
			serverId = authInfo.Exec.Args[i+1]
			break
		}
	}

	if serverId == "" {
		panic("Unable to understand kubeconfig command line args")
	}

	// Use the token provider to get a new token
	cliAccessToken, err := cred.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{serverId}})
	if err != nil {
		return nil, fmt.Errorf("failed to get token: %w", err)
	}
	if cliAccessToken.Token == "" {
		return nil, errors.New("did not receive a token")
	}

	authInfo.Token = cliAccessToken.Token
	authInfo.Exec = nil

	bytes, err := clientcmd.Write(*kubeConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to write kubeconfig: %w", err)
	}

	return clientcmd.RESTConfigFromKubeConfig(bytes)
}
