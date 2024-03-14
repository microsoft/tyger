// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controlplane

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestServerUriNormalization(t *testing.T) {
	testCases := []struct {
		input    string
		expected string
	}{
		{"http://foo/", "http://foo"},
		{"http://foo", "http://foo"},
	}
	for _, tC := range testCases {
		t.Run(tC.input, func(t *testing.T) {
			normalized, err := NormalizeServerUri(tC.input)
			assert.Nil(t, err)
			assert.Equal(t, tC.expected, normalized.String())
		})
	}
}

func TestServerUriValidation(t *testing.T) {
	testCases := []string{
		"abc",
		"/abc",
	}
	for _, tC := range testCases {
		t.Run(tC, func(t *testing.T) {
			_, err := NormalizeServerUri(tC)
			assert.NotNil(t, err)
		})
	}
}
