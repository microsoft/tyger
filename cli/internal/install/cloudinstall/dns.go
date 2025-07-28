// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/network/armnetwork/v7"
	"k8s.io/utils/ptr"
)

func (inst *Installer) assignDnsRecord(ctx context.Context, org *OrganizationConfig) (any, error) {
	if inst.Config.Cloud.DnsZone == nil {
		return nil, nil
	}

	if inst.Config.Cloud.PrivateNetworking {
		return nil, inst.assignPrivateDnsRecord(ctx, org)
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

func (inst *Installer) assignPrivateDnsRecord(ctx context.Context, org *OrganizationConfig) error {
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

		if err := inst.createPrivateDnsZone(ctx, "traefik-pe-nic", org.Api.DomainName, clusterConfig.ExistingSubnet); err != nil {
			return fmt.Errorf("failed to create private DNS zone: %w", err)
		}
	}

	return nil
}
