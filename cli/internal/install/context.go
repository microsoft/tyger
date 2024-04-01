// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

type configContextKeyType int

const (
	configKey          configContextKeyType = 0
	azureCredentialKey configContextKeyType = 1
	setupOptionsKey    configContextKeyType = 2
)

func GetEnvironmentConfigFromContext(ctx context.Context) EnvironmentConfig {
	return ctx.Value(configKey).(EnvironmentConfig)
}

func GetCloudEnvironmentConfigFromContext(ctx context.Context) *CloudEnvironmentConfig {
	return ctx.Value(configKey).(*CloudEnvironmentConfig)
}

func GetDockerEnvironmentConfigFromContext(ctx context.Context) *DockerEnvironmentConfig {
	return ctx.Value(configKey).(*DockerEnvironmentConfig)
}

func SetEnvironmentConfigOnContext(ctx context.Context, config EnvironmentConfig) context.Context {
	return context.WithValue(ctx, configKey, config)
}

func GetAzureCredentialFromContext(ctx context.Context) azcore.TokenCredential {
	return ctx.Value(azureCredentialKey).(azcore.TokenCredential)
}

func SetAzureCredentialOnContext(ctx context.Context, cred azcore.TokenCredential) context.Context {
	return context.WithValue(ctx, azureCredentialKey, cred)
}
