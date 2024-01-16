// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package settings

import (
	"context"
	"net/http"
	"net/url"
)

type ServiceInfo interface {
	GetServerUri() *url.URL
	GetPrincipal() string
	GetAccessToken(ctx context.Context) (string, error)
	GetProxyFunc() func(*http.Request) (*url.URL, error)
	GetDisableTlsCertificateValidation() bool
}

type serviceinfoKeyType int

const (
	serviceInfoKey serviceinfoKeyType = iota
	serviceInfoFuncKey
)

func SetServiceInfoOnContext(ctx context.Context, serviceInfo ServiceInfo) context.Context {
	return context.WithValue(ctx, serviceInfoKey, serviceInfo)
}

func SetServiceInfoFuncOnContext(ctx context.Context, serviceInfoFunc func() (ServiceInfo, error)) context.Context {
	return context.WithValue(ctx, serviceInfoFuncKey, serviceInfoFunc)
}

func GetServiceInfoFromContext(ctx context.Context) (ServiceInfo, error) {
	if serviceInfo, ok := ctx.Value(serviceInfoKey).(ServiceInfo); ok {
		return serviceInfo, nil
	}

	if serviceInfoFunc, ok := ctx.Value(serviceInfoFuncKey).(func() (ServiceInfo, error)); ok {
		return serviceInfoFunc()
	}

	panic("service info not set on context")
}
