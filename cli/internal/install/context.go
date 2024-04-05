// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"context"
)

type configContextKeyType int

const (
	configKey configContextKeyType = 0
)

func GetEnvironmentConfigFromContext(ctx context.Context) any {
	return ctx.Value(configKey)
}

func SetEnvironmentConfigOnContext(ctx context.Context, config any) context.Context {
	return context.WithValue(ctx, configKey, config)
}
