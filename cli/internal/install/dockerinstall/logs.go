package dockerinstall

import (
	"context"
	"io"
)

func (inst *Installer) GetServerLogs(ctx context.Context, follow bool, tail int, destination io.Writer) error {
	return inst.getContainerLogs(ctx, inst.resourceName(controlPlaneContainerSuffix), follow, tail, destination, destination)
}
