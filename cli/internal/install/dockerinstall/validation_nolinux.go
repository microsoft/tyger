// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build !linux

package dockerinstall

func defaultUseGateway() bool {
	return true
}
