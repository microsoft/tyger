package cloudinstall

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v7"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/rs/zerolog/log"
)

func (inst *Installer) createPrivateEndpointsForStorageAccount(ctx context.Context, targetResource *armstorage.Account) error {
	return inst.createPrivateEndpoints(ctx, fmt.Sprintf("storage-%s-pe", *targetResource.Name), *targetResource.ID, []*string{Ptr("blob")}, fmt.Sprintf("%s.privatelink.blob.core.windows.net", *targetResource.Name))
}

func (inst *Installer) createPrivateEndpointsForPostgresFlexibleServer(ctx context.Context, targetResource *armpostgresqlflexibleservers.Server) error {
	return inst.createPrivateEndpoints(ctx, fmt.Sprintf("postgres-%s-pe", *targetResource.Name), *targetResource.ID, []*string{Ptr("postgresqlServer")}, fmt.Sprintf("%s.privatelink.postgres.database.azure.com", *targetResource.Name))
}

func (inst *Installer) createPrivateEndpointsForTraefik(ctx context.Context, cluster *armcontainerservice.ManagedCluster) error {

	plServicesClient, err := armnetwork.NewPrivateLinkServicesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create private link services client: %w", err)
	}

	plService, err := plServicesClient.Get(ctx, *cluster.Properties.NodeResourceGroup, TraefikPrivateLinkServiceName, nil)
	if err != nil {
		return fmt.Errorf("failed to get private link service for Traefik: %w", err)
	}

	return inst.createPrivateEndpoints(ctx, "traefik-pe", *plService.ID, []*string{}, "")
}

func (inst *Installer) createPrivateEndpoints(ctx context.Context, privateEndpointName string, targetResourceId string, groupIds []*string, privateDnsZoneName string) error {
	vnetClient, err := armnetwork.NewVirtualNetworksClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create virtual networks client: %w", err)
	}

	visitedVnets := make(map[string]any)
	for _, clusterConfig := range inst.Config.Cloud.Compute.Clusters {
		vnetResult, err := vnetClient.Get(ctx, clusterConfig.ExistingSubnet.ResourceGroup, clusterConfig.ExistingSubnet.VNetName, nil)
		if err != nil {
			var respErr *azcore.ResponseError
			if errors.As(err, &respErr) && respErr.StatusCode == http.StatusNotFound {
				return fmt.Errorf("VNet '%s' not found in resource group '%s'", clusterConfig.ExistingSubnet.VNetName, clusterConfig.ExistingSubnet.ResourceGroup)
			}

			return fmt.Errorf("failed to get VNet: %w", err)
		}

		if _, ok := visitedVnets[*vnetResult.ID]; ok {
			continue
		}

		visitedVnets[*vnetResult.ID] = nil

		var subnetId string
		if subnetIndex := slices.IndexFunc(vnetResult.Properties.Subnets, func(subnet *armnetwork.Subnet) bool {
			return subnet.Name != nil && *subnet.Name == clusterConfig.ExistingSubnet.SubnetName
		}); subnetIndex < 0 {
			return fmt.Errorf("subnet '%s' not found in VNet '%s'", clusterConfig.ExistingSubnet.SubnetName, clusterConfig.ExistingSubnet.VNetName)
		} else {
			subnetId = *vnetResult.Properties.Subnets[subnetIndex].ID
		}

		nicName := fmt.Sprintf("%s-nic", privateEndpointName)

		privateEndpoint := armnetwork.PrivateEndpoint{
			Location: vnetResult.Location,
			Properties: &armnetwork.PrivateEndpointProperties{
				Subnet: &armnetwork.Subnet{
					ID: &subnetId,
				},
				CustomNetworkInterfaceName: &nicName,
				PrivateLinkServiceConnections: []*armnetwork.PrivateLinkServiceConnection{
					{
						Name: &privateEndpointName,
						Properties: &armnetwork.PrivateLinkServiceConnectionProperties{
							PrivateLinkServiceID: &targetResourceId,
							GroupIDs:             groupIds,
						},
					},
				},
			},
			Tags: map[string]*string{
				TagKey: &inst.Config.EnvironmentName,
			},
		}

		log.Ctx(ctx).Info().Msgf("Creating or updating private endpoint '%s' for storage account '%s' for subnet '%s' in vnet '%s'", privateEndpointName, path.Base(targetResourceId), clusterConfig.ExistingSubnet.SubnetName, clusterConfig.ExistingSubnet.VNetName)

		privateEndpointClient, err := armnetwork.NewPrivateEndpointsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
		if err != nil {
			return fmt.Errorf("failed to create private endpoint client: %w", err)
		}

		poller, err := privateEndpointClient.BeginCreateOrUpdate(ctx, clusterConfig.ExistingSubnet.PrivateLinkResourceGroup, privateEndpointName, privateEndpoint, nil)
		if err != nil {
			return fmt.Errorf("failed to create private endpoint: %w", err)
		}

		_, err = poller.PollUntilDone(ctx, nil)
		if err != nil {
			return fmt.Errorf("failed to create private endpoint: %w", err)
		}

		if privateDnsZoneName != "" {
			if err := inst.createPrivateDnsZone(ctx, nicName, privateDnsZoneName, clusterConfig.ExistingSubnet); err != nil {
				return fmt.Errorf("failed to create private DNS zone: %w", err)
			}
		}
	}

	return nil
}

func (inst *Installer) createPrivateDnsZone(ctx context.Context, nicName string, domainName string, subnet *SubnetReference) error {
	privateDNSZoneClient, err := armprivatedns.NewPrivateZonesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create private DNS zone client: %w", err)
	}

	log.Ctx(ctx).Info().Msgf("Creating or updating private DNS zone '%s'", domainName)

	dnsZonePoller, err := privateDNSZoneClient.BeginCreateOrUpdate(ctx, subnet.PrivateLinkResourceGroup, domainName, armprivatedns.PrivateZone{
		Location: Ptr("global"),
		Tags: map[string]*string{
			TagKey: &inst.Config.EnvironmentName,
		},
	}, nil)

	if err != nil {
		return fmt.Errorf("failed to create private DNS zone: %w", err)
	}

	_, err = dnsZonePoller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to create private DNS zone: %w", err)
	}

	vnetClient, err := armnetwork.NewVirtualNetworksClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create virtual networks client: %w", err)
	}

	vnetResult, err := vnetClient.Get(ctx, subnet.ResourceGroup, subnet.VNetName, nil)
	if err != nil {
		return fmt.Errorf("failed to get VNet: %w", err)
	}

	virtualNetworkLinksClient, err := armprivatedns.NewVirtualNetworkLinksClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create virtual network links client: %w", err)
	}

	linkPoller, err := virtualNetworkLinksClient.BeginCreateOrUpdate(ctx, subnet.PrivateLinkResourceGroup, domainName, fmt.Sprintf("%s-%s", subnet.ResourceGroup, subnet.VNetName),
		armprivatedns.VirtualNetworkLink{
			Location: Ptr("global"),
			Properties: &armprivatedns.VirtualNetworkLinkProperties{
				RegistrationEnabled: Ptr(false),
				VirtualNetwork: &armprivatedns.SubResource{
					ID: vnetResult.ID,
				},
			},
			Tags: map[string]*string{
				TagKey: &inst.Config.EnvironmentName,
			},
		}, nil)

	if err != nil {
		return fmt.Errorf("failed to create virtual network link: %w", err)
	}

	_, err = linkPoller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to create virtual network link: %w", err)
	}

	recordSetsClient, err := armprivatedns.NewRecordSetsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create record sets client: %w", err)
	}

	interfacesClient, err := armnetwork.NewInterfacesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create network interfaces client: %w", err)
	}

	nic, err := interfacesClient.Get(ctx, subnet.PrivateLinkResourceGroup, nicName, nil)
	if err != nil {
		return fmt.Errorf("failed to get network interface '%s': %w", nicName)
	}

	_, err = recordSetsClient.CreateOrUpdate(ctx, subnet.PrivateLinkResourceGroup, domainName, armprivatedns.RecordTypeA, "@",
		armprivatedns.RecordSet{Properties: &armprivatedns.RecordSetProperties{
			ARecords: []*armprivatedns.ARecord{
				{
					IPv4Address: nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress,
				},
			},
			TTL: Ptr[int64](3600),
		}}, nil)

	if err != nil {
		return fmt.Errorf("failed to create A record in private DNS zone: %w", err)
	}

	return nil
}
