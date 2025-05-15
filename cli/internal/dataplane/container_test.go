// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"net/url"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestBlobURLGeneration(t *testing.T) {
	t.Parallel()

	url, _ := url.Parse("https://microsoft.github.io/tyger")
	container := NewContainer(url)

	assert.Equal(t, url.String(), container.GetAccessUrl().String())

	fullUrl := url.JoinPath("container", StartMetadataBlobName)
	assert.Equal(t, fullUrl.String(), container.JoinPath("container", StartMetadataBlobName))

	assert.Equal(t, "00/000", MakeBlobPath(0x000))
	assert.Equal(t, "00/FFF", MakeBlobPath(0xFFF))

	assert.Equal(t, "01/000", MakeBlobPath(0x1000))
	assert.Equal(t, "01/FFF", MakeBlobPath(0x1FFF))

	assert.Equal(t, "02/00/000", MakeBlobPath(0x2000))
	assert.Equal(t, "02/01/FFF", MakeBlobPath(0x3FFF))

	assert.Equal(t, "03/00/000", MakeBlobPath(0x4000))
	assert.Equal(t, "03/03/FFF", MakeBlobPath(0x7FFF))

	assert.Equal(t, "04/00/000", MakeBlobPath(0x8000))
	assert.Equal(t, "04/07/FFF", MakeBlobPath(0xFFFF))

	assert.Equal(t, "05/00/000", MakeBlobPath(0x10000))
	assert.Equal(t, "05/0F/FFF", MakeBlobPath(0x1FFFF))

	assert.Equal(t, "06/00/000", MakeBlobPath(0x20000))
	assert.Equal(t, "06/1F/FFF", MakeBlobPath(0x3FFFF))

	assert.Equal(t, "07/00/000", MakeBlobPath(0x40000))
	assert.Equal(t, "07/3F/FFF", MakeBlobPath(0x7FFFF))

	assert.Equal(t, "08/00/000", MakeBlobPath(0x80000))
	assert.Equal(t, "08/7F/FFF", MakeBlobPath(0xFFFFF))

	assert.Equal(t, "09/00/000", MakeBlobPath(0x100000))
	assert.Equal(t, "09/FF/FFF", MakeBlobPath(0x1FFFFF))

	assert.Equal(t, "0A/00/00/000", MakeBlobPath(0x200000))
	assert.Equal(t, "0A/01/FF/FFF", MakeBlobPath(0x3FFFFF))

	assert.Equal(t, "0B/00/00/000", MakeBlobPath(0x400000))
	assert.Equal(t, "0B/03/FF/FFF", MakeBlobPath(0x7FFFFF))

	assert.Equal(t, "0C/00/00/000", MakeBlobPath(0x800000))
	assert.Equal(t, "0C/07/FF/FFF", MakeBlobPath(0xFFFFFF))

	assert.Equal(t, "0D/00/00/000", MakeBlobPath(0x1000000))
	assert.Equal(t, "0D/0F/FF/FFF", MakeBlobPath(0x1FFFFFF))

	assert.Equal(t, "0E/00/00/000", MakeBlobPath(0x2000000))
	assert.Equal(t, "0E/1F/FF/FFF", MakeBlobPath(0x3FFFFFF))

	assert.Equal(t, "0F/00/00/000", MakeBlobPath(0x4000000))
	assert.Equal(t, "0F/3F/FF/FFF", MakeBlobPath(0x7FFFFFF))

	assert.Equal(t, "10/00/00/000", MakeBlobPath(0x8000000))
	assert.Equal(t, "10/7F/FF/FFF", MakeBlobPath(0xFFFFFFF))

	assert.Equal(t, "11/00/00/000", MakeBlobPath(0x10000000))
	assert.Equal(t, "11/FF/FF/FFF", MakeBlobPath(0x1FFFFFFF))
}
