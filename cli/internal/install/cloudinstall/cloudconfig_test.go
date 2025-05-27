// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestRenderConfig(t *testing.T) {
	values := ConfigTemplateValues{
		EnvironmentName: "abc",
		ResourceGroup:   "def",
		TenantId:        "tenant",
		SubscriptionId:  "sub",
		DefaultLocation: "westus",
		ManagementPrincipal: Principal{
			ObjectId:          uuid.New().String(),
			Kind:              PrincipalKindUser,
			UserPrincipalName: "my@example.com",
		},
		TygerPrincipal: Principal{
			ObjectId:          uuid.New().String(),
			Kind:              PrincipalKindUser,
			UserPrincipalName: "my@example.com",
		},
		BufferStorageAccountName: "acc1",
		LogsStorageAccountName:   "acc2",
		DomainName:               "me.westus.cloudapp.azure.com",
		DatabaseServerName:       "dbserver",
		ApiTenantId:              "tenant2",
	}

	var buf bytes.Buffer
	require.NoError(t, RenderConfig(values, &buf))

	config := &CloudEnvironmentConfig{}

	require.NoError(t, yaml.UnmarshalStrict(buf.Bytes(), &config))

	errorBuf := bytes.Buffer{}
	ctx := zerolog.New(&errorBuf).WithContext(context.Background())
	config.QuickValidateConfig(ctx)

	errorLines := strings.Split(errorBuf.String(), "\n")
	require.NotEmpty(t, errorLines, "Expected error buffer to contain lines")
	for _, line := range errorLines {
		if strings.TrimSpace(line) != "" {
			// it is expected that the validation will fail because some of the access control fields are not set
			require.Contains(t, line, "tyger access-control apply")
		}
	}

	require.Equal(t, values.EnvironmentName, config.EnvironmentName)
	require.Equal(t, values.ResourceGroup, config.Cloud.ResourceGroup)
	require.Equal(t, values.TenantId, config.Cloud.TenantID)
	require.Equal(t, values.SubscriptionId, config.Cloud.SubscriptionID)
	require.Equal(t, values.DefaultLocation, config.Cloud.DefaultLocation)
	require.Equal(t, values.ManagementPrincipal.Kind, config.Cloud.Compute.ManagementPrincipals[0].Kind)
	require.Equal(t, values.ManagementPrincipal.ObjectId, config.Cloud.Compute.ManagementPrincipals[0].ObjectId)
	require.Equal(t, values.ManagementPrincipal.UserPrincipalName, config.Cloud.Compute.ManagementPrincipals[0].UserPrincipalName)
	require.Equal(t, values.BufferStorageAccountName, config.Organizations[0].Cloud.Storage.Buffers[0].Name)
	require.Equal(t, values.LogsStorageAccountName, config.Organizations[0].Cloud.Storage.Logs.Name)
	require.Equal(t, values.DomainName, config.Organizations[0].Api.DomainName)
	require.Equal(t, values.ApiTenantId, config.Organizations[0].Api.AccessControl.TenantID)
}
