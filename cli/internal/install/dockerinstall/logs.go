package dockerinstall

import (
	"context"

	"github.com/microsoft/tyger/cli/internal/install"
)

func (inst *Installer) GetServerLogs(ctx context.Context, options install.ServerLogOptions) error {
	var containerName string
	if options.DataPlane {
		containerName = inst.resourceName(dataPlaneContainerSuffix)
	} else {
		containerName = inst.resourceName(controlPlaneContainerSuffix)
	}

	return inst.getContainerLogs(ctx, containerName, options.Follow, options.TailLines, options.Destination, options.Destination)
}
