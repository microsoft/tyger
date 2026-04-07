// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"encoding/json"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v7"
	"github.com/stretchr/testify/assert"
)

// baseCluster returns a minimal ManagedCluster that clusterNeedsUpdating considers up-to-date
// when compared against itself (via a deep copy).
func baseCluster() armcontainerservice.ManagedCluster {
	return armcontainerservice.ManagedCluster{
		Tags: map[string]*string{
			"env": Ptr("test"),
		},
		Properties: &armcontainerservice.ManagedClusterProperties{
			ProvisioningState: Ptr("Succeeded"),
			KubernetesVersion: Ptr(DefaultKubernetesVersion),
			AutoUpgradeProfile: &armcontainerservice.ManagedClusterAutoUpgradeProfile{
				NodeOSUpgradeChannel: Ptr(armcontainerservice.NodeOSUpgradeChannelNodeImage),
				UpgradeChannel:       Ptr(armcontainerservice.UpgradeChannelPatch),
			},
			OidcIssuerProfile: &armcontainerservice.ManagedClusterOIDCIssuerProfile{
				Enabled: Ptr(true),
			},
			SecurityProfile: &armcontainerservice.ManagedClusterSecurityProfile{
				WorkloadIdentity: &armcontainerservice.ManagedClusterSecurityProfileWorkloadIdentity{
					Enabled: Ptr(true),
				},
			},
			NetworkProfile: &armcontainerservice.NetworkProfile{
				OutboundType: Ptr(armcontainerservice.OutboundTypeLoadBalancer),
				LoadBalancerProfile: &armcontainerservice.ManagedClusterLoadBalancerProfile{
					OutboundIPs: &armcontainerservice.ManagedClusterLoadBalancerProfileOutboundIPs{
						PublicIPs: []*armcontainerservice.ResourceReference{
							{ID: Ptr("/subscriptions/sub/resourceGroups/rg/providers/Microsoft.Network/publicIPAddresses/ip1")},
						},
					},
				},
			},
			AgentPoolProfiles: []*armcontainerservice.ManagedClusterAgentPoolProfile{
				{
					Name:                Ptr("system"),
					VMSize:              Ptr("Standard_DS2_v2"),
					EnableAutoScaling:   Ptr(true),
					MinCount:            Ptr[int32](1),
					MaxCount:            Ptr[int32](3),
					OrchestratorVersion: Ptr(DefaultKubernetesVersion),
					OSSKU:               Ptr(armcontainerservice.OSSKUAzureLinux),
					Tags:                map[string]*string{"env": Ptr("test")},
				},
			},
		},
		SKU: &armcontainerservice.ManagedClusterSKU{
			Tier: Ptr(armcontainerservice.ManagedClusterSKUTierStandard),
		},
	}
}

// clone returns a deep copy via JSON round-trip.
func clone(c armcontainerservice.ManagedCluster) armcontainerservice.ManagedCluster {
	data, err := json.Marshal(c)
	if err != nil {
		panic(err)
	}
	var cp armcontainerservice.ManagedCluster
	if err := json.Unmarshal(data, &cp); err != nil {
		panic(err)
	}
	return cp
}

func TestClusterNeedsUpdating(t *testing.T) {
	t.Run("identical clusters", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)

		hasChanges, onlyScaleDown := clusterNeedsUpdating(desired, existing)
		assert.False(t, hasChanges)
		assert.True(t, onlyScaleDown)
	})

	t.Run("provisioning not succeeded", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.ProvisioningState = Ptr("Creating")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("kubernetes version changed", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.KubernetesVersion = Ptr("1.32")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("node OS upgrade channel changed", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.AutoUpgradeProfile.NodeOSUpgradeChannel = Ptr(armcontainerservice.NodeOSUpgradeChannelNone)

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("upgrade channel changed", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.AutoUpgradeProfile.UpgradeChannel = Ptr(armcontainerservice.UpgradeChannelStable)

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("upgrade channel nil on existing", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.AutoUpgradeProfile.UpgradeChannel = nil

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("tag added", func(t *testing.T) {
		desired := baseCluster()
		desired.Tags["new"] = Ptr("tag")
		existing := clone(baseCluster())

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("tag value changed", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Tags["env"] = Ptr("prod")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("SKU tier changed", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.SKU.Tier = Ptr(armcontainerservice.ManagedClusterSKUTierFree)

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("agent pool count changed", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.AgentPoolProfiles = append(desired.Properties.AgentPoolProfiles, &armcontainerservice.ManagedClusterAgentPoolProfile{
			Name:                Ptr("user1"),
			VMSize:              Ptr("Standard_DS2_v2"),
			EnableAutoScaling:   Ptr(true),
			MinCount:            Ptr[int32](0),
			MaxCount:            Ptr[int32](5),
			OrchestratorVersion: Ptr(DefaultKubernetesVersion),
			OSSKU:               Ptr(armcontainerservice.OSSKUAzureLinux),
			Tags:                map[string]*string{"env": Ptr("test")},
		})
		existing := clone(baseCluster())

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("API server access profile added", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.APIServerAccessProfile = &armcontainerservice.ManagedClusterAPIServerAccessProfile{
			EnablePrivateCluster: Ptr(true),
		}
		existing := clone(baseCluster())

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("private cluster changed", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.APIServerAccessProfile = &armcontainerservice.ManagedClusterAPIServerAccessProfile{
			EnablePrivateCluster:           Ptr(true),
			EnablePrivateClusterPublicFQDN: Ptr(false),
			PrivateDNSZone:                 Ptr("zone1"),
		}
		existing := clone(desired)
		existing.Properties.APIServerAccessProfile.EnablePrivateCluster = Ptr(false)

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("private DNS zone changed", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.APIServerAccessProfile = &armcontainerservice.ManagedClusterAPIServerAccessProfile{
			EnablePrivateCluster: Ptr(true),
			PrivateDNSZone:       Ptr("zone-new"),
		}
		existing := clone(desired)
		existing.Properties.APIServerAccessProfile.PrivateDNSZone = Ptr("zone-old")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("node pool VM size changed", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.AgentPoolProfiles[0].VMSize = Ptr("Standard_DS3_v2")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("node pool scale down", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.AgentPoolProfiles[0].MinCount = Ptr[int32](2)
		existing.Properties.AgentPoolProfiles[0].MaxCount = Ptr[int32](5)

		hasChanges, onlyScaleDown := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
		assert.True(t, onlyScaleDown)
	})

	t.Run("node pool scale up", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.AgentPoolProfiles[0].MinCount = Ptr[int32](3)
		desired.Properties.AgentPoolProfiles[0].MaxCount = Ptr[int32](10)
		existing := clone(baseCluster())

		hasChanges, onlyScaleDown := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
		assert.False(t, onlyScaleDown)
	})

	t.Run("node pool orchestrator version changed", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.AgentPoolProfiles[0].OrchestratorVersion = Ptr("1.32")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("node pool OSSKU changed", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.AgentPoolProfiles[0].OSSKU = Ptr(armcontainerservice.OSSKUUbuntu)

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("node pool tag changed", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.AgentPoolProfiles[0].Tags["env"] = Ptr("prod")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("node pool subnet changed", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.AgentPoolProfiles[0].VnetSubnetID = Ptr("/subnets/subnet1")
		existing := clone(desired)
		existing.Properties.AgentPoolProfiles[0].VnetSubnetID = Ptr("/subnets/subnet2")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("node pool subnet added where none", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.AgentPoolProfiles[0].VnetSubnetID = Ptr("/subnets/subnet1")
		existing := clone(baseCluster())

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("node pool auto scaling disabled on existing", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.AgentPoolProfiles[0].EnableAutoScaling = nil

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("node pool not found", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.AgentPoolProfiles[0].Name = Ptr("renamed")
		existing := clone(baseCluster())

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("addon profile added", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.AddonProfiles = map[string]*armcontainerservice.ManagedClusterAddonProfile{
			"omsagent": {Enabled: Ptr(true), Config: map[string]*string{"key": Ptr("val")}},
		}
		existing := clone(baseCluster())

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("addon profile disabled", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.AddonProfiles = map[string]*armcontainerservice.ManagedClusterAddonProfile{
			"omsagent": {Enabled: Ptr(true), Config: map[string]*string{}},
		}
		existing := clone(desired)
		existing.Properties.AddonProfiles["omsagent"].Enabled = Ptr(false)

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("addon profile config changed", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.AddonProfiles = map[string]*armcontainerservice.ManagedClusterAddonProfile{
			"omsagent": {Enabled: Ptr(true), Config: map[string]*string{"key": Ptr("new")}},
		}
		existing := clone(desired)
		existing.Properties.AddonProfiles["omsagent"].Config["key"] = Ptr("old")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("OIDC disabled on existing", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.OidcIssuerProfile.Enabled = Ptr(false)

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("workload identity disabled on existing", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.SecurityProfile.WorkloadIdentity.Enabled = Ptr(false)

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("pod CIDR changed", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.NetworkProfile.PodCidr = Ptr("10.244.0.0/16")
		existing := clone(desired)
		existing.Properties.NetworkProfile.PodCidr = Ptr("10.245.0.0/16")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("pod CIDR nil on desired", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.NetworkProfile.PodCidr = Ptr("10.244.0.0/16")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.False(t, hasChanges)
	})

	t.Run("service CIDR changed", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.NetworkProfile.ServiceCidr = Ptr("10.0.0.0/16")
		existing := clone(desired)
		existing.Properties.NetworkProfile.ServiceCidr = Ptr("10.1.0.0/16")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("DNS service IP changed", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.NetworkProfile.DNSServiceIP = Ptr("10.0.0.10")
		existing := clone(desired)
		existing.Properties.NetworkProfile.DNSServiceIP = Ptr("10.0.0.20")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("outbound type changed", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.NetworkProfile.OutboundType = Ptr(armcontainerservice.OutboundTypeUserDefinedRouting)
		desired.Properties.NetworkProfile.LoadBalancerProfile = nil
		existing := clone(baseCluster())

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("outbound type nil on desired", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.NetworkProfile.OutboundType = nil
		existing := clone(baseCluster())

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.False(t, hasChanges)
	})

	t.Run("load balancer outbound IP changed", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.NetworkProfile.LoadBalancerProfile.OutboundIPs.PublicIPs[0].ID = Ptr("/publicIPAddresses/ip2")

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("load balancer outbound IP added", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.NetworkProfile.LoadBalancerProfile.OutboundIPs = nil

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
	})

	t.Run("load balancer profile nil on desired", func(t *testing.T) {
		desired := baseCluster()
		desired.Properties.NetworkProfile.LoadBalancerProfile = nil
		existing := clone(baseCluster())

		hasChanges, _ := clusterNeedsUpdating(desired, existing)
		assert.False(t, hasChanges)
	})

	t.Run("mixed scale up and down", func(t *testing.T) {
		desired := baseCluster()
		existing := clone(desired)
		existing.Properties.AgentPoolProfiles[0].MinCount = Ptr[int32](2) // desired 1 < existing 2 → scale down
		existing.Properties.AgentPoolProfiles[0].MaxCount = Ptr[int32](2) // desired 3 > existing 2 → scale up

		hasChanges, onlyScaleDown := clusterNeedsUpdating(desired, existing)
		assert.True(t, hasChanges)
		assert.False(t, onlyScaleDown)
	})
}
