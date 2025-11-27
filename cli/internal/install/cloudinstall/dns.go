// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v7"
	"k8s.io/utils/ptr"
)

func (inst *Installer) assignDnsRecord(ctx context.Context, org *OrganizationConfig) (any, error) {
	if inst.Config.Cloud.PrivateNetworking {
		apiHostSubnetReference := inst.Config.Cloud.Compute.GetApiHostCluster().ExistingSubnet
		return nil, inst.forEachVnet(ctx, func(ctx context.Context, vnet *armnetwork.VirtualNetwork, subnet *armnetwork.Subnet, configSubnet *SubnetReference) error {
			var ipAddress string
			if apiHostSubnetReference.VNetResourceId == configSubnet.VNetResourceId {
				cluster, err := inst.getCluster(ctx, inst.Config.Cloud.Compute.GetApiHostCluster())
				if err != nil {
					return fmt.Errorf("failed to get cluster: %w", err)
				}

				plServicesClient, err := armnetwork.NewPrivateLinkServicesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
				if err != nil {
					return fmt.Errorf("failed to create private link services client: %w", err)
				}

				traefikPlService, err := plServicesClient.Get(ctx, *cluster.Properties.NodeResourceGroup, TraefikPrivateLinkServiceName, nil)
				if err != nil {
					return fmt.Errorf("failed to get private link service for Traefik: %w", err)
				}

				lbFeId := traefikPlService.Properties.LoadBalancerFrontendIPConfigurations[0].ID
				lbClient, err := armnetwork.NewLoadBalancersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
				if err != nil {
					return fmt.Errorf("failed to create load balancers client: %w", err)
				}

				pager := lbClient.NewListPager(*cluster.Properties.NodeResourceGroup, nil)
				for pager.More() {
					page, err := pager.NextPage(ctx)
					if err != nil {
						return fmt.Errorf("failed to list load balancers: %w", err)
					}

					for _, lb := range page.Value {
						for _, fe := range lb.Properties.FrontendIPConfigurations {
							if *fe.ID == *lbFeId {
								ipAddress = *fe.Properties.PrivateIPAddress
								break
							}
						}
					}
				}

				if ipAddress == "" {
					return fmt.Errorf("failed to find private IP address for Traefik load balancer frontend")
				}
			} else {
				interfacesClient, err := armnetwork.NewInterfacesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
				if err != nil {
					return fmt.Errorf("failed to create network interfaces client: %w", err)
				}

				nic, err := interfacesClient.Get(ctx, configSubnet.PrivateLinkResourceGroup, "traefik-pe-nic", nil)
				if err != nil {
					return fmt.Errorf("failed to get network interface: %w", err)
				}

				ipAddress = *nic.Properties.IPConfigurations[0].Properties.PrivateIPAddress
			}

			return inst.createPrivateDnsZone(ctx, org.Api.DomainName, ipAddress, configSubnet)
		})
	}

	if inst.Config.Cloud.DnsZone == nil {
		return nil, nil
	}

	recordSetsClient, err := armdns.NewRecordSetsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create DNS record sets client: %w", err)
	}

	relativeRecordName := strings.Split(org.Api.DomainName, ".")[0]

	_, err = recordSetsClient.CreateOrUpdate(ctx, inst.Config.Cloud.DnsZone.ResourceGroup, inst.Config.Cloud.DnsZone.Name, relativeRecordName, armdns.RecordTypeCNAME,
		armdns.RecordSet{
			Properties: &armdns.RecordSetProperties{
				CnameRecord: &armdns.CnameRecord{
					Cname: ptr.To(fmt.Sprintf("%s%s", inst.Config.Cloud.Compute.DnsLabel, GetDomainNameSuffix(inst.Config.Cloud.Compute.GetApiHostCluster().Location))),
				},
				TTL: ptr.To[int64](3600),
			}}, nil)

	if err != nil {
		return nil, fmt.Errorf("failed to create DNS record set: %w", err)
	}

	return nil, nil
}

func (inst *Installer) deleteDnsRecord(ctx context.Context, org *OrganizationConfig) (any, error) {
	if inst.Config.Cloud.DnsZone == nil {
		return nil, nil
	}

	recordSetsClient, err := armdns.NewRecordSetsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create DNS record sets client: %w", err)
	}

	relativeRecordName := strings.Split(org.Api.DomainName, ".")[0]

	_, err = recordSetsClient.Delete(ctx, inst.Config.Cloud.DnsZone.ResourceGroup, inst.Config.Cloud.DnsZone.Name, relativeRecordName, armdns.RecordTypeCNAME, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to delete DNS record set: %w", err)
	}

	return nil, nil
}
