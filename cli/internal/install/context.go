// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
)

type configContextKeyType int

const (
	configKey          configContextKeyType = 0
	azureCredentialKey configContextKeyType = 1
	setupOptionsKey    configContextKeyType = 2
)

func GetConfigFromContext(ctx context.Context) *EnvironmentConfig {
	return ctx.Value(configKey).(*EnvironmentConfig)
}

func SetConfigOnContext(ctx context.Context, config *EnvironmentConfig) context.Context {
	return context.WithValue(ctx, configKey, config)
}

func GetAzureCredentialFromContext(ctx context.Context) azcore.TokenCredential {
	return ctx.Value(azureCredentialKey).(azcore.TokenCredential)
}

func SetAzureCredentialOnContext(ctx context.Context, cred azcore.TokenCredential) context.Context {
	return context.WithValue(ctx, azureCredentialKey, cred)
}

func WaitForPoller[T any](ctx context.Context, promise *Promise[*runtime.Poller[T]]) (T, error) {
	poller, err := promise.Await()
	if err != nil {
		var t T
		return t, errDependencyFailed
	}

	return poller.PollUntilDone(ctx, nil)
}
