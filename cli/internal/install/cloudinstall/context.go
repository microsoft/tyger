// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/microsoft/tyger/cli/internal/install"
)

type cloudConfigContextKeyType int

const (
	azureCredentialKey cloudConfigContextKeyType = 1
)

func GetCloudEnvironmentConfigFromContext(ctx context.Context) *CloudEnvironmentConfig {
	return install.GetEnvironmentConfigFromContext(ctx).(*CloudEnvironmentConfig)
}

func GetAzureCredentialFromContext(ctx context.Context) azcore.TokenCredential {
	return ctx.Value(azureCredentialKey).(azcore.TokenCredential)
}

func SetAzureCredentialOnContext(ctx context.Context, cred azcore.TokenCredential) context.Context {
	return context.WithValue(ctx, azureCredentialKey, cred)
}
