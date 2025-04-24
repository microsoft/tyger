package cloudinstall

import (
	"context"
	"fmt"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/dns/armdns"
	"k8s.io/utils/ptr"
)

func (inst *Installer) assignDnsRecord(ctx context.Context, org *OrganizationConfig) (any, error) {
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
