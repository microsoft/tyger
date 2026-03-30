// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v7"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v7"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/privatedns/armprivatedns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/rs/zerolog/log"
)

func (inst *Installer) createPrivateEndpointsForStorageAccount(ctx context.Context, targetResource *armstorage.Account) error {
	return inst.forEachVnet(ctx, func(ctx context.Context, vnet *armnetwork.VirtualNetwork, subnet *armnetwork.Subnet, configSubnet *SubnetReference) error {
		return inst.createPrivateEndpoints(ctx, fmt.Sprintf("storage-%s-pe", *targetResource.Name), *targetResource.ID, []*string{Ptr("blob")}, fmt.Sprintf("%s.privatelink.blob.core.windows.net", *targetResource.Name), vnet, subnet, configSubnet)
	})
}

func (inst *Installer) createPrivateEndpointsForPostgresFlexibleServer(ctx context.Context, targetResource *armpostgresqlflexibleservers.Server) error {
	return inst.forEachVnet(ctx, func(ctx context.Context, vnet *armnetwork.VirtualNetwork, subnet *armnetwork.Subnet, configSubnet *SubnetReference) error {
		return inst.createPrivateEndpoints(ctx, fmt.Sprintf("postgres-%s-pe", *targetResource.Name), *targetResource.ID, []*string{Ptr("postgresqlServer")}, fmt.Sprintf("%s.privatelink.postgres.database.azure.com", *targetResource.Name), vnet, subnet, configSubnet)
	})
}

func (inst *Installer) createPrivateEndpointsForTraefik(ctx context.Context, cluster *armcontainerservice.ManagedCluster) error {
	plServicesClient, err := armnetwork.NewPrivateLinkServicesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create private link services client: %w", err)
	}

	return inst.forEachVnet(ctx, func(ctx context.Context, vnet *armnetwork.VirtualNetwork, subnet *armnetwork.Subnet, configSubnet *SubnetReference) error {
		if configSubnet.VNetResourceId == *vnet.ID {
			// no need to create private endpoint for Traefik if the VNet is the same as the one used for the cluster
			return nil
		}

		plService, err := plServicesClient.Get(ctx, *cluster.Properties.NodeResourceGroup, TraefikPrivateLinkServiceName, nil)
		if err != nil {
			return fmt.Errorf("failed to get private link service for Traefik: %w", err)
		}

		return inst.createPrivateEndpoints(ctx, "traefik-pe", *plService.ID, []*string{}, "", vnet, subnet, configSubnet)
	})
}

func (inst *Installer) createPrivateEndpoints(ctx context.Context, privateEndpointName string, targetResourceId string, groupIds []*string, privateDnsZoneName string, vnet *armnetwork.VirtualNetwork, subnet *armnetwork.Subnet, configSubnet *SubnetReference) error {
	nicName := fmt.Sprintf("%s-nic", privateEndpointName)

	privateEndpoint := armnetwork.PrivateEndpoint{
		Location: vnet.Location,
		Properties: &armnetwork.PrivateEndpointProperties{
			Subnet: &armnetwork.Subnet{
				ID: subnet.ID,
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

	for k, v := range inst.Config.Cloud.ResourceTags {
		privateEndpoint.Tags[k] = &v
	}

	log.Ctx(ctx).Info().Msgf("Creating or updating private endpoint '%s' for storage account '%s' for subnet '%s' in vnet '%s'", privateEndpointName, path.Base(targetResourceId), configSubnet.SubnetName, configSubnet.VNetName)

	privateEndpointClient, err := armnetwork.NewPrivateEndpointsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create private endpoint client: %w", err)
	}

	poller, err := privateEndpointClient.BeginCreateOrUpdate(ctx, configSubnet.PrivateLinkResourceGroup, privateEndpointName, privateEndpoint, nil)
	if err != nil {
		return fmt.Errorf("failed to create private endpoint: %w", err)
	}

	_, err = poller.PollUntilDone(ctx, nil)
	if err != nil {
		return fmt.Errorf("failed to create private endpoint: %w", err)
	}

	if privateDnsZoneName != "" {
		interfacesClient, err := armnetwork.NewInterfacesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
		if err != nil {
			return fmt.Errorf("failed to create network interfaces client: %w", err)
		}

		nic, err := interfacesClient.Get(ctx, configSubnet.PrivateLinkResourceGroup, nicName, nil)
		if err != nil {
			return fmt.Errorf("failed to get network interface '%s': %w", nicName, err)
		}

		if err := inst.createPrivateDnsZoneWithRecord(ctx, privateDnsZoneName, *nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress, configSubnet); err != nil {
			return fmt.Errorf("failed to create private DNS zone: %w", err)
		}
	}

	return nil
}

// createAksPrivateDnsZone creates the <envName><hash>.privatelink.<location>.azmk8s.io DNS zone,
// links it to the spoke VNet and any additional VNets, removes stale links,
// and returns the zone's resource ID. The hash is derived from the subscription,
// resource group, and cluster name to ensure uniqueness.
func (inst *Installer) createAksPrivateDnsZone(ctx context.Context, location string, clusterName string, subnet *SubnetReference) (string, error) {
	h := sha256.Sum256([]byte(inst.Config.Cloud.SubscriptionID + "/" + inst.Config.Cloud.ResourceGroup + "/" + clusterName))
	zoneName := fmt.Sprintf("%s%s.privatelink.%s.azmk8s.io", inst.Config.EnvironmentName, hex.EncodeToString(h[:4]), location)
	return inst.createPrivateDnsZone(ctx, zoneName, subnet)
}

func (inst *Installer) createPrivateDnsZoneWithRecord(ctx context.Context, domainName string, ipAddress string, subnet *SubnetReference) error {
	_, err := inst.createPrivateDnsZone(ctx, domainName, subnet)
	if err != nil {
		return err
	}

	recordSetsClient, err := armprivatedns.NewRecordSetsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create record sets client: %w", err)
	}

	_, err = recordSetsClient.CreateOrUpdate(ctx, subnet.PrivateLinkResourceGroup, domainName, armprivatedns.RecordTypeA, "@",
		armprivatedns.RecordSet{Properties: &armprivatedns.RecordSetProperties{
			ARecords: []*armprivatedns.ARecord{
				{
					IPv4Address: &ipAddress,
				},
			},
			TTL: Ptr[int64](3600),
		}}, nil)

	if err != nil {
		return fmt.Errorf("failed to create A record in private DNS zone: %w", err)
	}

	return nil
}

// createPrivateDnsZone creates a private DNS zone, links it to the spoke VNet
// and any additional VNets, removes stale links, and returns the zone's resource ID.
func (inst *Installer) createPrivateDnsZone(ctx context.Context, zoneName string, subnet *SubnetReference) (string, error) {
	privateDNSZoneClient, err := armprivatedns.NewPrivateZonesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create private DNS zone client: %w", err)
	}

	tags := map[string]*string{
		TagKey: &inst.Config.EnvironmentName,
	}
	for k, v := range inst.Config.Cloud.ResourceTags {
		tags[k] = &v
	}

	log.Ctx(ctx).Info().Msgf("Creating or updating private DNS zone '%s'", zoneName)

	dnsZonePoller, err := privateDNSZoneClient.BeginCreateOrUpdate(ctx, subnet.PrivateLinkResourceGroup, zoneName, armprivatedns.PrivateZone{
		Location: Ptr("global"),
		Tags:     tags,
	}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create private DNS zone '%s': %w", zoneName, err)
	}

	zoneResp, err := dnsZonePoller.PollUntilDone(ctx, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create private DNS zone '%s': %w", zoneName, err)
	}

	vnetClient, err := armnetwork.NewVirtualNetworksClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create virtual networks client: %w", err)
	}

	vnetResult, err := vnetClient.Get(ctx, subnet.ResourceGroup, subnet.VNetName, nil)
	if err != nil {
		return "", fmt.Errorf("failed to get VNet: %w", err)
	}

	virtualNetworkLinksClient, err := armprivatedns.NewVirtualNetworkLinksClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create virtual network links client: %w", err)
	}

	linkPoller, err := virtualNetworkLinksClient.BeginCreateOrUpdate(ctx, subnet.PrivateLinkResourceGroup, zoneName, fmt.Sprintf("%s-%s", subnet.ResourceGroup, subnet.VNetName),
		armprivatedns.VirtualNetworkLink{
			Location: Ptr("global"),
			Properties: &armprivatedns.VirtualNetworkLinkProperties{
				RegistrationEnabled: Ptr(false),
				VirtualNetwork: &armprivatedns.SubResource{
					ID: vnetResult.ID,
				},
			},
			Tags: tags,
		}, nil)
	if err != nil {
		return "", fmt.Errorf("failed to create virtual network link: %w", err)
	}

	if _, err = linkPoller.PollUntilDone(ctx, nil); err != nil {
		return "", fmt.Errorf("failed to create virtual network link: %w", err)
	}

	expectedVnetIDs := map[string]bool{
		strings.ToLower(*vnetResult.ID): true,
	}

	for _, additionalVnet := range inst.Config.Cloud.AdditionalDnsVnetLinks {
		subId := additionalVnet.SubscriptionID
		if subId == "" {
			subId = inst.Config.Cloud.SubscriptionID
		}

		additionalVnetClient, err := armnetwork.NewVirtualNetworksClient(subId, inst.Credential, nil)
		if err != nil {
			return "", fmt.Errorf("failed to create VNet client for subscription '%s': %w", subId, err)
		}

		additionalVnetResult, err := additionalVnetClient.Get(ctx, additionalVnet.ResourceGroup, additionalVnet.VNetName, nil)
		if err != nil {
			return "", fmt.Errorf("failed to get additional VNet '%s/%s' in subscription '%s': %w",
				additionalVnet.ResourceGroup, additionalVnet.VNetName, subId, err)
		}

		additionalVnetID := strings.ToLower(*additionalVnetResult.ID)
		if expectedVnetIDs[additionalVnetID] {
			return "", fmt.Errorf("duplicate VNet in additionalDnsVnetLinks: '%s/%s' in subscription '%s'",
				additionalVnet.ResourceGroup, additionalVnet.VNetName, subId)
		}
		expectedVnetIDs[additionalVnetID] = true

		linkName := fmt.Sprintf("%s-%s", additionalVnet.ResourceGroup, additionalVnet.VNetName)
		log.Ctx(ctx).Info().Msgf("Creating or updating virtual network link '%s' for DNS zone '%s'", linkName, zoneName)

		additionalLinkPoller, err := virtualNetworkLinksClient.BeginCreateOrUpdate(ctx,
			subnet.PrivateLinkResourceGroup, zoneName, linkName,
			armprivatedns.VirtualNetworkLink{
				Location: Ptr("global"),
				Properties: &armprivatedns.VirtualNetworkLinkProperties{
					RegistrationEnabled: Ptr(false),
					VirtualNetwork: &armprivatedns.SubResource{
						ID: additionalVnetResult.ID,
					},
				},
				Tags: tags,
			}, nil)
		if err != nil {
			return "", fmt.Errorf("failed to create virtual network link '%s': %w", linkName, err)
		}

		if _, err = additionalLinkPoller.PollUntilDone(ctx, nil); err != nil {
			return "", fmt.Errorf("failed to create virtual network link '%s': %w", linkName, err)
		}
	}

	// Remove stale VNet links whose destination VNet ID is not in the expected set
	linkPager := virtualNetworkLinksClient.NewListPager(subnet.PrivateLinkResourceGroup, zoneName, nil)
	for linkPager.More() {
		page, err := linkPager.NextPage(ctx)
		if err != nil {
			return "", fmt.Errorf("failed to list virtual network links for zone '%s': %w", zoneName, err)
		}
		for _, link := range page.Value {
			if link.Properties != nil && link.Properties.VirtualNetwork != nil && link.Properties.VirtualNetwork.ID != nil {
				if !expectedVnetIDs[strings.ToLower(*link.Properties.VirtualNetwork.ID)] {
					log.Ctx(ctx).Info().Msgf("Removing stale virtual network link '%s' (VNet '%s') from DNS zone '%s'", *link.Name, *link.Properties.VirtualNetwork.ID, zoneName)
					deletePoller, err := virtualNetworkLinksClient.BeginDelete(ctx, subnet.PrivateLinkResourceGroup, zoneName, *link.Name, nil)
					if err != nil {
						return "", fmt.Errorf("failed to delete stale virtual network link '%s': %w", *link.Name, err)
					}
					if _, err := deletePoller.PollUntilDone(ctx, nil); err != nil {
						return "", fmt.Errorf("failed to delete stale virtual network link '%s': %w", *link.Name, err)
					}
				}
			}
		}
	}

	return *zoneResp.ID, nil
}

func (inst *Installer) forEachVnet(ctx context.Context, action func(ctx context.Context, vnet *armnetwork.VirtualNetwork, subnet *armnetwork.Subnet, configSubnet *SubnetReference) error) error {
	vnetClient, err := armnetwork.NewVirtualNetworksClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create virtual networks client: %w", err)
	}

	visitedVnets := make(map[string]any)
	for _, clusterConfig := range inst.Config.Cloud.Compute.Clusters {
		vnetResult, err := vnetClient.Get(ctx, clusterConfig.ExistingSubnet.ResourceGroup, clusterConfig.ExistingSubnet.VNetName, nil)
		if err != nil {
			if isNotFoundError(err) {
				return fmt.Errorf("VNet '%s' not found in resource group '%s'", clusterConfig.ExistingSubnet.VNetName, clusterConfig.ExistingSubnet.ResourceGroup)
			}

			return fmt.Errorf("failed to get VNet: %w", err)
		}

		if _, ok := visitedVnets[*vnetResult.ID]; ok {
			continue
		}

		visitedVnets[*vnetResult.ID] = nil

		var subnet *armnetwork.Subnet
		if subnetIndex := slices.IndexFunc(vnetResult.Properties.Subnets, func(subnet *armnetwork.Subnet) bool {
			return subnet.Name != nil && *subnet.Name == clusterConfig.ExistingSubnet.SubnetName
		}); subnetIndex < 0 {
			return fmt.Errorf("subnet '%s' not found in VNet '%s'", clusterConfig.ExistingSubnet.SubnetName, clusterConfig.ExistingSubnet.VNetName)
		} else {
			subnet = vnetResult.Properties.Subnets[subnetIndex]
		}

		if err := action(ctx, &vnetResult.VirtualNetwork, subnet, clusterConfig.ExistingSubnet); err != nil {
			return err
		}
	}

	return nil
}

func (inst *Installer) deleteOrgPrivateLinkResources(ctx context.Context, org *OrganizationConfig) error {
	// Collect all per-org storage account names
	var storageAccountNames []string
	if org.Cloud.Storage != nil {
		if org.Cloud.Storage.Logs != nil {
			storageAccountNames = append(storageAccountNames, org.Cloud.Storage.Logs.Name)
		}
		for _, buf := range org.Cloud.Storage.Buffers {
			storageAccountNames = append(storageAccountNames, buf.Name)
		}
	}

	// Collect all per-org private DNS zone names
	var dnsZoneNames []string
	for _, name := range storageAccountNames {
		dnsZoneNames = append(dnsZoneNames, fmt.Sprintf("%s.privatelink.blob.core.windows.net", name))
	}
	if org.Api != nil && org.Api.DomainName != "" {
		dnsZoneNames = append(dnsZoneNames, org.Api.DomainName)
	}

	return inst.forEachVnet(ctx, func(ctx context.Context, vnet *armnetwork.VirtualNetwork, subnet *armnetwork.Subnet, configSubnet *SubnetReference) error {
		privateDnsZoneClient, err := armprivatedns.NewPrivateZonesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
		if err != nil {
			return fmt.Errorf("failed to create private DNS zone client: %w", err)
		}

		virtualNetworkLinksClient, err := armprivatedns.NewVirtualNetworkLinksClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
		if err != nil {
			return fmt.Errorf("failed to create virtual network links client: %w", err)
		}

		// Delete per-org private DNS zones: first remove VNet links, then delete the zone
		for _, zoneName := range dnsZoneNames {
			// Delete all VNet links in this zone first
			linkPager := virtualNetworkLinksClient.NewListPager(configSubnet.PrivateLinkResourceGroup, zoneName, nil)
			for linkPager.More() {
				page, err := linkPager.NextPage(ctx)
				if err != nil {
					if isNotFoundError(err) {
						break
					}
					return fmt.Errorf("failed to list virtual network links for zone '%s': %w", zoneName, err)
				}
				for _, link := range page.Value {
					log.Ctx(ctx).Info().Msgf("Deleting virtual network link '%s' from DNS zone '%s'", *link.Name, zoneName)
					linkPoller, err := virtualNetworkLinksClient.BeginDelete(ctx, configSubnet.PrivateLinkResourceGroup, zoneName, *link.Name, nil)
					if err != nil {
						if isNotFoundError(err) {
							continue
						}
						return fmt.Errorf("failed to delete virtual network link '%s': %w", *link.Name, err)
					}
					if _, err := linkPoller.PollUntilDone(ctx, nil); err != nil {
						return fmt.Errorf("failed to delete virtual network link '%s': %w", *link.Name, err)
					}
				}
			}

			log.Ctx(ctx).Info().Msgf("Deleting private DNS zone '%s' from '%s'", zoneName, configSubnet.PrivateLinkResourceGroup)
			// Retry zone deletion on 409 Conflict — Azure may not have fully propagated VNet link deletions yet
			for attempt := 0; ; attempt++ {
				poller, err := privateDnsZoneClient.BeginDelete(ctx, configSubnet.PrivateLinkResourceGroup, zoneName, nil)
				if err != nil {
					if isNotFoundError(err) {
						break
					}
					var respErr *azcore.ResponseError
					if errors.As(err, &respErr) && respErr.StatusCode == http.StatusConflict && attempt < 5 {
						log.Ctx(ctx).Info().Msgf("DNS zone '%s' still has nested resources, retrying in 10s (attempt %d/5)", zoneName, attempt+1)
						time.Sleep(10 * time.Second)
						continue
					}
					return fmt.Errorf("failed to delete private DNS zone '%s': %w", zoneName, err)
				}
				if _, err := poller.PollUntilDone(ctx, nil); err != nil {
					return fmt.Errorf("failed to delete private DNS zone '%s': %w", zoneName, err)
				}
				break
			}
		}

		// Delete per-org storage private endpoints and NICs
		privateEndpointClient, err := armnetwork.NewPrivateEndpointsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
		if err != nil {
			return fmt.Errorf("failed to create private endpoint client: %w", err)
		}

		for _, name := range storageAccountNames {
			peName := fmt.Sprintf("storage-%s-pe", name)
			log.Ctx(ctx).Info().Msgf("Deleting private endpoint '%s' from '%s'", peName, configSubnet.PrivateLinkResourceGroup)
			pePoller, err := privateEndpointClient.BeginDelete(ctx, configSubnet.PrivateLinkResourceGroup, peName, nil)
			if err != nil {
				if isNotFoundError(err) {
					continue
				}
				return fmt.Errorf("failed to delete private endpoint '%s': %w", peName, err)
			}
			if _, err := pePoller.PollUntilDone(ctx, nil); err != nil {
				return fmt.Errorf("failed to delete private endpoint '%s': %w", peName, err)
			}
		}

		return nil
	})
}
