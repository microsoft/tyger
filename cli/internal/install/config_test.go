package install

import (
	"bytes"
	"testing"

	"github.com/google/uuid"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestRenderConfig(t *testing.T) {
	values := ConfigTemplateValues{
		EnvironmentName:          "abc",
		ResourceGroup:            "def",
		TenantId:                 "tenant",
		SubscriptionId:           "sub",
		DefaultLocation:          "westus",
		PrincipalId:              uuid.New().String(),
		PrincipalKind:            PrincipalKindUser,
		BufferStorageAccountName: "acc1",
		LogsStorageAccountName:   "acc2",
		DomainName:               "dom.ain",
		ApiTenantId:              "tenant2",
	}

	var buf bytes.Buffer

	require.NoError(t, RenderConfig(values, &buf))

	config := EnvironmentConfig{}
	require.NoError(t, yaml.UnmarshalStrict(buf.Bytes(), &config))

	require.Equal(t, values.EnvironmentName, config.EnvironmentName)
	require.Equal(t, values.ResourceGroup, config.Cloud.ResourceGroup)
	require.Equal(t, values.TenantId, config.Cloud.TenantID)
	require.Equal(t, values.SubscriptionId, config.Cloud.SubscriptionID)
	require.Equal(t, values.DefaultLocation, config.Cloud.DefaultLocation)
	require.Equal(t, values.PrincipalKind, config.Cloud.Compute.ManagementPrincipals[0].Kind)
	require.Equal(t, values.BufferStorageAccountName, config.Cloud.Storage.Buffers[0].Name)
	require.Equal(t, values.LogsStorageAccountName, config.Cloud.Storage.Logs.Name)
	require.Equal(t, values.DomainName, config.Api.DomainName)
	require.Equal(t, values.ApiTenantId, config.Api.Auth.TenantID)

	values.PrincipalKind = PrincipalKindServicePrincipal

	buf.Reset()
	require.NoError(t, RenderConfig(values, &buf))
	config = EnvironmentConfig{}
	require.NoError(t, yaml.UnmarshalStrict(buf.Bytes(), &config))

	require.Equal(t, values.PrincipalId, config.Cloud.Compute.ManagementPrincipals[0].Id)

}
