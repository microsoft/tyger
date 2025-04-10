// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controlplane

import (
	"context"
)

const (
	DefaultApiVersion = "1.0"

	ApiVersionQueryParam = "api-version"
)

type apiVersionContextKeyType int

const apiVersionKey apiVersionContextKeyType = iota

func GetApiVersionFromContext(ctx context.Context) string {
	if version, ok := ctx.Value(apiVersionKey).(string); ok {
		return version
	} else {
		return DefaultApiVersion
	}
}

func SetApiVersionOnContext(ctx context.Context, apiVersion string) context.Context {
	return context.WithValue(ctx, apiVersionKey, apiVersion)
}
