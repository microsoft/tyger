// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dockerinstall

import (
	"context"

	"github.com/microsoft/tyger/cli/internal/install"
)

func GetDockerEnvironmentConfigFromContext(ctx context.Context) *DockerEnvironmentConfig {
	return install.GetEnvironmentConfigFromContext(ctx).(*DockerEnvironmentConfig)
}
