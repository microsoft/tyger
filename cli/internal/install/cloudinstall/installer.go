package cloudinstall

import (
	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
)

type Installer struct {
	Config     *CloudEnvironmentConfig
	Credential azcore.TokenCredential
}
