// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"encoding/json"
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

const DefaultKubernetesVersion = "1.30" // LTS

func (inst *Installer) createCluster(ctx context.Context, clusterConfig *ClusterConfig) (*armcontainerservice.ManagedCluster, error) {
	clustersClient, err := armcontainerservice.NewManagedClustersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create clusters client: %w", err)
	}

	var poller *runtime.Poller[armcontainerservice.ManagedClustersClientCreateOrUpdateResponse]
	var tags map[string]*string

	var clusterAlreadyExists bool
	existingCluster, err := clustersClient.Get(ctx, inst.Config.Cloud.ResourceGroup, clusterConfig.Name, nil)
	if err == nil {
		clusterAlreadyExists = true
		if existingTag, ok := existingCluster.Tags[TagKey]; ok {
			if *existingTag != inst.Config.EnvironmentName {
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
	tags[TagKey] = &inst.Config.EnvironmentName

	if clusterAlreadyExists {
		if *existingCluster.Properties.KubernetesVersion != clusterConfig.KubernetesVersion {
			existingCluster.Properties.KubernetesVersion = &clusterConfig.KubernetesVersion
			log.Ctx(ctx).Info().Msgf("Updating Kubernetes version to %s", clusterConfig.KubernetesVersion)
			resp, err := clustersClient.BeginCreateOrUpdate(ctx, inst.Config.Cloud.ResourceGroup, clusterConfig.Name, existingCluster.ManagedCluster, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to update cluster: %w", err)
			}
			if _, err := resp.PollUntilDone(ctx, nil); err != nil {
				return nil, fmt.Errorf("failed to update cluster: %w", err)
			}
			existingCluster, err = clustersClient.Get(ctx, inst.Config.Cloud.ResourceGroup, clusterConfig.Name, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to get cluster: %w", err)
			}
		}
	}

	cluster := armcontainerservice.ManagedCluster{
		Tags:     tags,
		Location: Ptr(clusterConfig.Location),
		Identity: &armcontainerservice.ManagedClusterIdentity{
			Type: Ptr(armcontainerservice.ResourceIdentityTypeSystemAssigned),
		},
		Properties: &armcontainerservice.ManagedClusterProperties{
			DNSPrefix:         Ptr(getClusterDnsPrefix(inst.Config.EnvironmentName, clusterConfig.Name, inst.Config.Cloud.SubscriptionID)),
			KubernetesVersion: &clusterConfig.KubernetesVersion,
			AutoUpgradeProfile: &armcontainerservice.ManagedClusterAutoUpgradeProfile{
				NodeOSUpgradeChannel: Ptr(armcontainerservice.NodeOSUpgradeChannelNodeImage),
				UpgradeChannel:       Ptr(armcontainerservice.UpgradeChannelPatch),
			},
			EnableRBAC: Ptr(true),
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
		SKU: &armcontainerservice.ManagedClusterSKU{
			Name: Ptr(armcontainerservice.ManagedClusterSKUNameBase),
			Tier: Ptr(armcontainerservice.ManagedClusterSKUTier(clusterConfig.Sku)),
		},
	}

	if workspace := inst.Config.Cloud.LogAnalyticsWorkspace; workspace != nil {
		oic, err := armoperationalinsights.NewWorkspacesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
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
			Name:                &clusterConfig.SystemNodePool.Name,
			Mode:                Ptr(armcontainerservice.AgentPoolModeSystem),
			OrchestratorVersion: &clusterConfig.KubernetesVersion,
			VMSize:              &clusterConfig.SystemNodePool.VMSize,
			EnableAutoScaling:   Ptr(true),
			Count:               &clusterConfig.SystemNodePool.MinCount,
			MinCount:            &clusterConfig.SystemNodePool.MinCount,
			MaxCount:            &clusterConfig.SystemNodePool.MaxCount,
			OSType:              Ptr(armcontainerservice.OSTypeLinux),
			OSSKU:               Ptr(armcontainerservice.OSSKUAzureLinux),
			Tags:                tags,
		},
	}

	for _, np := range clusterConfig.UserNodePools {
		profile := armcontainerservice.ManagedClusterAgentPoolProfile{
			Name:                &np.Name,
			Mode:                Ptr(armcontainerservice.AgentPoolModeUser),
			OrchestratorVersion: &clusterConfig.KubernetesVersion,
			VMSize:              &np.VMSize,
			EnableAutoScaling:   Ptr(true),
			Count:               &np.MinCount,
			MinCount:            &np.MinCount,
			MaxCount:            &np.MaxCount,
			OSType:              Ptr(armcontainerservice.OSTypeLinux),
			OSSKU:               Ptr(armcontainerservice.OSSKUAzureLinux),
			NodeLabels: map[string]*string{
				"tyger": Ptr("run"),
			},
			NodeTaints: []*string{
				Ptr("tyger=run:NoSchedule"),
			},
			Tags: tags,
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

	if clusterAlreadyExists {
		// Check for node pools that need to be added or removed, which
		// need to be handled separately from other cluster property updates.

		agentPoolsClient, err := armcontainerservice.NewAgentPoolsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create agent pools client: %w", err)
		}

		agentPoolCreatePollers := make([]*runtime.Poller[armcontainerservice.AgentPoolsClientCreateOrUpdateResponse], 0)

		for _, newNodePool := range cluster.Properties.AgentPoolProfiles {
			found := false
			for _, existingNodePool := range existingCluster.ManagedCluster.Properties.AgentPoolProfiles {
				if *newNodePool.Name == *existingNodePool.Name {
					found = true
					if *newNodePool.VMSize != *existingNodePool.VMSize {
						return nil, fmt.Errorf("create a new node pool instead of changing the VM size of node pool '%s'", *newNodePool.Name)
					}

					if *newNodePool.Mode != *existingNodePool.Mode {
						return nil, fmt.Errorf("cannot change existing node pool '%s' from user to system (or vice-versa)", *newNodePool.Name)
					}

					break
				}
			}
			if !found {
				log.Info().Msgf("Adding node pool '%s' to cluster '%s'", *newNodePool.Name, clusterConfig.Name)
				newNodePoolJson, err := json.Marshal(newNodePool)
				if err != nil {
					panic(fmt.Errorf("failed to marshal node pool: %w", err))
				}

				properties := armcontainerservice.ManagedClusterAgentPoolProfileProperties{}
				decoder := json.NewDecoder(strings.NewReader(string(newNodePoolJson)))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(&properties); err != nil {
					panic(fmt.Errorf("failed to decode node pool: %w", err))
				}

				options := armcontainerservice.AgentPool{Properties: &properties}
				p, err := agentPoolsClient.BeginCreateOrUpdate(ctx, inst.Config.Cloud.ResourceGroup, clusterConfig.Name, *newNodePool.Name, options, nil)
				if err != nil {
					return nil, fmt.Errorf("failed to create node pool: %w", err)
				}
				agentPoolCreatePollers = append(agentPoolCreatePollers, p)
			}
		}

		for _, p := range agentPoolCreatePollers {
			if _, err := p.PollUntilDone(ctx, nil); err != nil {
				return nil, fmt.Errorf("failed to create node pool: %w", err)
			}
		}

		agentPoolDeletePollers := make([]*runtime.Poller[armcontainerservice.AgentPoolsClientDeleteResponse], 0)

		for _, existingNodePool := range existingCluster.ManagedCluster.Properties.AgentPoolProfiles {
			found := false
			for _, newPool := range cluster.Properties.AgentPoolProfiles {
				if *newPool.Name == *existingNodePool.Name {
					found = true
					break
				}
			}
			if !found {
				log.Info().Msgf("Deleting node pool '%s' from cluster '%s'", *existingNodePool.Name, clusterConfig.Name)
				p, err := agentPoolsClient.BeginDelete(ctx, inst.Config.Cloud.ResourceGroup, clusterConfig.Name, *existingNodePool.Name, nil)
				if err != nil {
					return nil, fmt.Errorf("failed to delete node pool: %w", err)
				}
				agentPoolDeletePollers = append(agentPoolDeletePollers, p)
			}
		}

		for _, deletePoller := range agentPoolDeletePollers {
			if _, err := deletePoller.PollUntilDone(ctx, nil); err != nil {
				return nil, fmt.Errorf("failed to delete node pool: %w", err)
			}
		}

		if len(agentPoolDeletePollers) > 0 || len(agentPoolCreatePollers) > 0 {
			// refresh the existingCluster variable
			existingCluster, err = clustersClient.Get(ctx, inst.Config.Cloud.ResourceGroup, clusterConfig.Name, nil)
			if err != nil {
				return nil, fmt.Errorf("failed to get cluster: %w", err)
			}
		}
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

		poller, err = clustersClient.BeginCreateOrUpdate(ctx, inst.Config.Cloud.ResourceGroup, clusterConfig.Name, cluster, nil)
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
		getResp, err := clustersClient.Get(ctx, inst.Config.Cloud.ResourceGroup, clusterConfig.Name, nil)
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

	for _, containerRegistry := range inst.Config.Cloud.Compute.PrivateContainerRegistries {
		log.Info().Msgf("Attaching ACR '%s' to cluster '%s'", containerRegistry, clusterConfig.Name)
		containerRegistryId, err := getContainerRegistryId(ctx, containerRegistry, inst.Config.Cloud.SubscriptionID, inst.Credential)
		if err != nil {
			return nil, err
		}

		if err := attachAcr(ctx, kubeletObjectId, containerRegistryId, inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
			return nil, fmt.Errorf("failed to attach ACR: %w", err)
		}
	}

	var createdCluster armcontainerservice.ManagedCluster
	if poller != nil {
		if !poller.Done() {
			log.Info().Msgf("Waiting for cluster '%s' to be ready", clusterConfig.Name)
		}
		r, err := poller.PollUntilDone(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create cluster '%s': %w", clusterConfig.Name, err)
		}
		log.Info().Msgf("Cluster '%s' ready", clusterConfig.Name)

		createdCluster = r.ManagedCluster
	} else {
		createdCluster = existingCluster.ManagedCluster
	}

	if err := assignRbacRole(ctx, inst.Config.Cloud.Compute.GetManagementPrincipalIds(), true, *createdCluster.ID, "Azure Kubernetes Service Cluster User Role", inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
		return nil, fmt.Errorf("failed to assign RBAC role on cluster: %w", err)
	}

	return &createdCluster, nil
}

func clusterNeedsUpdating(cluster, existingCluster armcontainerservice.ManagedCluster) (hasChanges bool, onlyScaleDown bool) {
	if *existingCluster.Properties.ProvisioningState != "Succeeded" {
		return true, false
	}

	onlyScaleDown = true
	if *cluster.Properties.KubernetesVersion != *existingCluster.Properties.KubernetesVersion {
		return true, false
	}

	if existingCluster.Properties.AutoUpgradeProfile == nil || existingCluster.Properties.AutoUpgradeProfile.NodeOSUpgradeChannel == nil || *cluster.Properties.AutoUpgradeProfile.NodeOSUpgradeChannel != *existingCluster.Properties.AutoUpgradeProfile.NodeOSUpgradeChannel {
		return true, false
	}

	if existingCluster.Properties.AutoUpgradeProfile.UpgradeChannel == nil || *cluster.Properties.AutoUpgradeProfile.UpgradeChannel != *existingCluster.Properties.AutoUpgradeProfile.UpgradeChannel {
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

	if *cluster.SKU.Tier != *existingCluster.SKU.Tier {
		return true, false
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
				if existingNp.EnableAutoScaling == nil || *np.EnableAutoScaling != *existingNp.EnableAutoScaling {
					return true, false
				} else {
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
				}
				if *np.OrchestratorVersion != *existingNp.OrchestratorVersion {
					return true, false
				}

				if len(np.Tags) != len(existingNp.Tags) {
					return true, false
				}

				for k, v := range np.Tags {
					existingV, ok := existingNp.Tags[k]
					if !ok {
						return true, false
					}
					if *v != *existingV {
						return true, false
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
	return assignRbacRole(ctx, []string{kubeletObjectId}, false, containerRegistryId, "AcrPull", subscriptionId, credential)
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

func (inst *Installer) onDeleteCluster(ctx context.Context, clusterConfig *ClusterConfig) error {
	clustersClient, err := armcontainerservice.NewManagedClustersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create clusters client: %w", err)
	}

	clusterResponse, err := clustersClient.Get(ctx, inst.Config.Cloud.ResourceGroup, clusterConfig.Name, nil)
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

	for _, containerRegistry := range inst.Config.Cloud.Compute.PrivateContainerRegistries {
		log.Info().Msgf("Detaching ACR '%s' from cluster '%s'", containerRegistry, clusterConfig.Name)
		containerRegistryId, err := getContainerRegistryId(ctx, containerRegistry, inst.Config.Cloud.SubscriptionID, inst.Credential)
		if err != nil {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
				continue
			}
			return err
		}

		if err := detachAcr(ctx, kubeletObjectId, containerRegistryId, inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
			return fmt.Errorf("failed to detatch ACR: %w", err)
		}
	}

	return nil
}

func (inst *Installer) getAdminRESTConfig(ctx context.Context) (*rest.Config, error) {
	clustersClient, err := armcontainerservice.NewManagedClustersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create clusters client: %w", err)
	}

	credResp, err := clustersClient.ListClusterAdminCredentials(ctx, inst.Config.Cloud.ResourceGroup, inst.Config.Cloud.Compute.GetApiHostCluster().Name, nil)
	if err != nil {
		return nil, err
	}

	return clientcmd.RESTConfigFromKubeConfig(credResp.Kubeconfigs[0].Value)
}

func (inst *Installer) GetUserRESTConfig(ctx context.Context) (*rest.Config, error) {
	if inst.cachedRESTConfig != nil {
		return inst.cachedRESTConfig, nil
	}

	clustersClient, err := armcontainerservice.NewManagedClustersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create clusters client: %w", err)
	}

	credResp, err := clustersClient.ListClusterUserCredentials(ctx, inst.Config.Cloud.ResourceGroup, inst.Config.Cloud.Compute.GetApiHostCluster().Name, nil)
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
	cliAccessToken, err := inst.Credential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{serverId}})
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

	config, err := clientcmd.RESTConfigFromKubeConfig(bytes)
	if err == nil {
		inst.cachedRESTConfig = config
	}

	return config, err
}
