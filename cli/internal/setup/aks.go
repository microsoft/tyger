package setup

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

func createCluster(ctx context.Context, clusterConfig *ClusterConfig) (any, error) {
	config := GetConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)
	options := GetSetupOptionsFromContext(ctx)

	clustersClient, err := armcontainerservice.NewManagedClustersClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create clusters client: %w", err)
	}

	var poller *runtime.Poller[armcontainerservice.ManagedClustersClientCreateOrUpdateResponse]

	if !options.SkipClusterSetup {
		mc := armcontainerservice.ManagedCluster{
			Location: Ptr(clusterConfig.Location),
			Identity: &armcontainerservice.ManagedClusterIdentity{
				Type: Ptr(armcontainerservice.ResourceIdentityTypeSystemAssigned),
			},
			Properties: &armcontainerservice.ManagedClusterProperties{
				DNSPrefix:                Ptr(getClusterDnsPrefix(config.EnvironmentName, clusterConfig.Name, config.SubscriptionID)),
				CurrentKubernetesVersion: Ptr("1.25.6"),
				EnableRBAC:               Ptr(true),
				AADProfile: &armcontainerservice.ManagedClusterAADProfile{
					Managed:         Ptr(true),
					EnableAzureRBAC: Ptr(false),
				},
			},
		}

		mc.Properties.AgentPoolProfiles = []*armcontainerservice.ManagedClusterAgentPoolProfile{
			{
				Name:              Ptr("system"),
				Mode:              Ptr(armcontainerservice.AgentPoolModeSystem),
				VMSize:            Ptr("Standard_DS2_v2"),
				EnableAutoScaling: Ptr(true),
				Count:             Ptr(int32(1)),
				MinCount:          Ptr(int32(1)),
				MaxCount:          Ptr(int32(3)),
				OSType:            Ptr(armcontainerservice.OSTypeLinux),
				OSSKU:             Ptr(armcontainerservice.OSSKU("AzureLinux")),
			},
		}

		for _, np := range clusterConfig.UserNodePools {
			profile := armcontainerservice.ManagedClusterAgentPoolProfile{
				Name:              &np.Name,
				Mode:              Ptr(armcontainerservice.AgentPoolModeUser),
				VMSize:            &np.VMSize,
				EnableAutoScaling: Ptr(true),
				Count:             &np.Count,
				MinCount:          &np.MinCount,
				MaxCount:          &np.MaxCount,
				OSType:            Ptr(armcontainerservice.OSTypeLinux),
				OSSKU:             Ptr(armcontainerservice.OSSKU("AzureLinux")),
				NodeLabels: map[string]*string{
					"tyger": Ptr("run"),
				},
				NodeTaints: []*string{
					Ptr("tyger=run:NoSchedule"),
				},
			}

			if strings.Contains(strings.ToLower(np.VMSize), "standard_n") {
				profile.NodeTaints = append(profile.NodeTaints, Ptr("sku=gpu:NoSchedule"))
			}

			mc.Properties.AgentPoolProfiles = append(mc.Properties.AgentPoolProfiles, &profile)
		}

		log.Info().Msgf("Creating or updating cluster '%s'", clusterConfig.Name)
		poller, err = clustersClient.BeginCreateOrUpdate(ctx, config.EnvironmentName, clusterConfig.Name, mc, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create cluster: %w", err)
		}
	}

	if !options.SkipAttachAcr {
		var kubeletObjectId string
		for ; ; time.Sleep(10 * time.Second) {
			getResp, err := clustersClient.Get(ctx, config.EnvironmentName, clusterConfig.Name, nil)
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

		for _, containerRegistry := range config.AttachedContainerRegistries {
			log.Info().Msgf("attaching ACR '%s' to cluster '%s'", containerRegistry, clusterConfig.Name)
			containerRegistryId, err := getContainerRegistryId(ctx, containerRegistry, config.SubscriptionID, cred)
			if err != nil {
				return nil, err
			}

			if err := attachAcr(ctx, kubeletObjectId, containerRegistryId, config.SubscriptionID, cred); err != nil {
				return nil, fmt.Errorf("failed to attach ACR: %w", err)
			}
		}
	}

	if poller != nil {
		if !poller.Done() {
			log.Info().Msgf("Waiting for cluster '%s' to be ready", clusterConfig.Name)
		}
		_, err := poller.PollUntilDone(ctx, nil)
		if err != nil {
			return nil, fmt.Errorf("failed to create cluster '%s': %w", clusterConfig.Name, err)
		}
		log.Info().Msgf("Cluster '%s' ready", clusterConfig.Name)
	}

	return nil, nil
}

func getClusterDnsPrefix(environmentName, clusterName, subId string) string {
	return fmt.Sprintf("%s-tyger-%s", regexp.MustCompile("[^a-zA-Z0-9-]").ReplaceAllString(environmentName+"-"+clusterName, ""), subId[0:8])
}

func attachAcr(ctx context.Context, kubeletObjectId, id, subscriptionId string, credential azcore.TokenCredential) error {
	roleDefClient, err := armauthorization.NewRoleDefinitionsClient(credential, nil)
	if err != nil {
		return err
	}

	pager := roleDefClient.NewListPager(id, &armauthorization.RoleDefinitionsClientListOptions{Filter: Ptr("rolename eq 'acrpull'")})

	var acrPullRoleId string
	for pager.More() && acrPullRoleId == "" {
		page, err := pager.NextPage(context.TODO())
		if err != nil {
			return err
		}

		for _, rd := range page.Value {
			if *rd.Properties.RoleName != "AcrPull" {
				panic(fmt.Sprintf("unexpected role name '%s'", *rd.Name))
			}
			acrPullRoleId = *rd.ID
			break
		}
	}

	if acrPullRoleId == "" {
		return fmt.Errorf("unable to find 'AcrPull' role")
	}

	roleAssignmentClient, err := armauthorization.NewRoleAssignmentsClient(subscriptionId, credential, nil)
	if err != nil {
		return err
	}

	for i := 0; ; i++ {
		_, err = roleAssignmentClient.Create(
			ctx,
			id,
			uuid.New().String(),
			armauthorization.RoleAssignmentCreateParameters{
				Properties: &armauthorization.RoleAssignmentProperties{
					RoleDefinitionID: Ptr(acrPullRoleId),
					PrincipalID:      Ptr(kubeletObjectId),
				},
			}, nil)
		if err != nil {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) {
				switch respErr.ErrorCode {
				case "RoleAssignmentExists":
					return nil
				case "PrincipalNotFound":
					if i > 60 {
						break
					}
					time.Sleep(10 * time.Second)
					continue
				}
			}
		}

		return err
	}
}

func getContainerRegistryId(ctx context.Context, name string, subscriptionId string, credential azcore.TokenCredential) (string, error) {
	resourceClient, err := armresources.NewClient(subscriptionId, credential, nil)
	if err != nil {
		return "", err
	}
	pager := resourceClient.NewListPager(&armresources.ClientListOptions{
		Filter: Ptr(fmt.Sprintf("resourceType eq 'Microsoft.ContainerRegistry/registries' and name eq '%s'", "eminence")),
	})

	for pager.More() {
		p, err := pager.NextPage(context.TODO())
		if err != nil {
			return "", err
		}
		for _, s := range p.Value {
			return *s.ID, nil
		}
	}

	return "", fmt.Errorf("container registry '%s' not found in subscription", name)
}
