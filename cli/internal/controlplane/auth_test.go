// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controlplane

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServerUrlNormalization(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"http://foo/", "http://foo"},
		{"http://foo", "http://foo"},
	}
	for _, tC := range testCases {
		t.Run(tC.input, func(t *testing.T) {
			normalized, err := NormalizeServerUrl(tC.input)
			assert.Nil(t, err)
			assert.Equal(t, tC.expected, normalized.String())
		})
	}
}

func TestServerUrlValidation(t *testing.T) {
	testCases := []string{
		"abc",
		"/abc",
	}
	for _, tC := range testCases {
		t.Run(tC, func(t *testing.T) {
			_, err := NormalizeServerUrl(tC)
			assert.NotNil(t, err)
		})
	}
}
