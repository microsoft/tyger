//go:build !linux

package dockerinstall

func defaultUseGateway() bool {
	return true
}
