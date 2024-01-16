// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"
)

var (
	ErrAccessStringNotUri = errors.New("the buffer access string is invalid. It must be a URI or the path of a file whose contents is a URI")
)

func GetUriFromAccessString(accessString string) (string, error) {
	if fi, err := os.Stat(accessString); err == nil && !fi.IsDir() {
		if fi.Size() < 2*1024 {
			accessStringBytes, err := os.ReadFile(accessString)
			if err != nil {
				return "", fmt.Errorf("unable to read URI string from file %s: %w", accessString, err)
			}

			accessString = string(accessStringBytes)
			accessString = strings.TrimRight(accessString, " \r\n")
		}
	}

	uri, err := url.Parse(accessString)
	if err != nil || !uri.IsAbs() {
		return "", ErrAccessStringNotUri
	}

	return accessString, nil
}
