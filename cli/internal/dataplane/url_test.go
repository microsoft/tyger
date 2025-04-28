// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"os"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAccessStringIsUrl(t *testing.T) {
	url, err := GetUrlFromAccessString("https://example.com")
	assert.Nil(t, err)
	assert.Equal(t, "https://example.com", url.String())
}

func TestAccessStringIsInvalidUrl(t *testing.T) {
	_, err := GetUrlFromAccessString("notafileoranabsoluteuri")
	assert.ErrorContains(t, err, "the buffer access string is invalid")
}

func TestAccessStringIsFile(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/access_string.txt"
	os.WriteFile(path, []byte("https://example.com"), 0644)
	url, err := GetUrlFromAccessString(path)
	assert.Nil(t, err)
	assert.Equal(t, "https://example.com", url.String())
}

func TestAccessStringIsFileWithInvalidUrl(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/access_string.txt"
	os.WriteFile(path, []byte("notanabsoluteuri"), 0644)
	_, err := GetUrlFromAccessString(path)
	assert.ErrorContains(t, err, "the buffer access string is invalid")
}

func TestAccessStringIsFileWithLargeUrl(t *testing.T) {
	dir := t.TempDir()
	path := dir + "/access_string.txt"
	f, err := os.Create(path)
	require.Nil(t, err)
	f.WriteString("https://example.com")
	for i := 0; i < 40; i++ {
		f.WriteString("/a/b/c/d/e/f/g/h/i/j/k/l/m/n/o/p/q/r/s/t/u/v/w/x/y/z")
	}
	err = f.Close()
	require.Nil(t, err)
	_, err = GetUrlFromAccessString(path)
	assert.ErrorContains(t, err, "the buffer access string is invalid")
}
