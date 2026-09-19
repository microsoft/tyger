// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controlplane

import (
	"errors"
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

func TestTranslateAzureCliCredentialErrorAzureCliNotFound(t *testing.T) {
	testCases := map[string]string{
		"current SDK": "AzureCLICredential: executable not found on path",
		"legacy SDK":  "AzureCLICredential: Azure CLI not found on path",
	}

	for name, message := range testCases {
		t.Run(name, func(t *testing.T) {
			originalErr := errors.New(message)

			translatedErr := translateAzureCliCredentialError(originalErr, "tenant-id")

			assert.ErrorIs(t, translatedErr, originalErr)
			assert.ErrorContains(t, translatedErr, "https://aka.ms/azure-cli")
		})
	}
}
