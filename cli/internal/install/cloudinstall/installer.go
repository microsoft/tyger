// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/microsoft/tyger/cli/internal/install"
	"k8s.io/client-go/rest"
)

type Installer struct {
	Config     *CloudEnvironmentConfig
	Credential azcore.TokenCredential

	cachedRESTConfig *rest.Config
}

func (inst *Installer) GetConfig() install.ValidatableConfig {
	return inst.Config
}
