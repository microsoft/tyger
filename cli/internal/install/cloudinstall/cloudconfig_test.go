// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"bytes"
	"context"
	"fmt"
	"testing"

	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/yaml"
)

func TestRenderConfig(t *testing.T) {
	values := ConfigTemplateValues{
		EnvironmentName: "abc",
		ResourceGroup:   "def",
		TenantId:        "tenant",
		SubscriptionId:  "sub",
		DefaultLocation: "westus",
		Principal: Principal{
			ObjectId:          uuid.New().String(),
			Kind:              PrincipalKindUser,
			UserPrincipalName: "my@example.com",
		},
		BufferStorageAccountName: "acc1",
		LogsStorageAccountName:   "acc2",
		DomainName:               "me.westus.cloudapp.azure.com",

		DatabaseServerName: "dbserver",
	}

	authSpec := TygerAuthSpec{
		AuthConfig: AuthConfig{
			RbacEnabled: ptr.To(true),
			TenantID:    "tenant2",
			ApiAppUri:   "api://tyger-server",
			ApiAppId:    uuid.New().String(),
			CliAppUri:   "api://tyger-cli",
			CliAppId:    uuid.New().String(),
		},
	}

	var buf bytes.Buffer
	require.NoError(t, RenderConfig(values, &authSpec, &buf))

	config := &CloudEnvironmentConfig{}

	require.NoError(t, yaml.UnmarshalStrict(buf.Bytes(), &config))
	fmt.Println(buf.String())

	errorBuf := bytes.Buffer{}
	ctx := zerolog.New(&errorBuf).WithContext(context.Background())

	require.NoError(t, config.QuickValidateConfig(ctx), errorBuf.String())

	require.Equal(t, values.EnvironmentName, config.EnvironmentName)
	require.Equal(t, values.ResourceGroup, config.Cloud.ResourceGroup)
	require.Equal(t, values.TenantId, config.Cloud.TenantID)
	require.Equal(t, values.SubscriptionId, config.Cloud.SubscriptionID)
	require.Equal(t, values.DefaultLocation, config.Cloud.DefaultLocation)
	require.Equal(t, values.Principal.Kind, config.Cloud.Compute.ManagementPrincipals[0].Kind)
	require.Equal(t, values.Principal.ObjectId, config.Cloud.Compute.ManagementPrincipals[0].ObjectId)
	require.Equal(t, values.Principal.UserPrincipalName, config.Cloud.Compute.ManagementPrincipals[0].UserPrincipalName)
	require.Equal(t, values.BufferStorageAccountName, config.Organizations[0].Cloud.Storage.Buffers[0].Name)
	require.Equal(t, values.LogsStorageAccountName, config.Organizations[0].Cloud.Storage.Logs.Name)
	require.Equal(t, values.DomainName, config.Organizations[0].Api.DomainName)
	require.Equal(t, authSpec.AuthConfig, *config.Organizations[0].Api.Auth)
}
