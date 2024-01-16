// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package main

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestIsPathDirectoryIntent(t *testing.T) {
	d := t.TempDir()
	assert.True(t, isPathDirectoryIntent(d))
	assert.False(t, isPathDirectoryIntent(""))
	assert.True(t, isPathDirectoryIntent(d+"/missing"))
	assert.False(t, isPathDirectoryIntent(d+"/missing.txt"))

	f, err := os.Create(d + "/file.txt")
	assert.NoError(t, err)
	defer f.Close()
	assert.False(t, isPathDirectoryIntent(f.Name()))

	f2, err := os.Create(d + "/file")
	assert.NoError(t, err)
	defer f.Close()
	assert.True(t, isPathDirectoryIntent(f2.Name()))
}
