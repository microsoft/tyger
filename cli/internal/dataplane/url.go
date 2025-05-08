// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"errors"
	"fmt"
	"net/url"
	"os"
	"strings"

	"github.com/rs/zerolog/log"
)

var (
	ErrAccessStringNotUrl = errors.New("the buffer access string is invalid. It must be a URL or the path of a file whose contents is a URL")
)

func GetUrlFromAccessString(accessString string) (*url.URL, error) {
	if fi, err := os.Stat(accessString); err == nil && !fi.IsDir() {
		if fi.Size() < 2*1024 {
			log.Debug().Msgf("JOE: Reading access string from file %s, which was modified at %s", accessString, fi.ModTime().Format("2006-01-02 15:04:05"))
			accessStringBytes, err := os.ReadFile(accessString)
			if err != nil {
				return nil, fmt.Errorf("unable to read URL string from file %s: %w", accessString, err)
			}

			accessString = string(accessStringBytes)
			accessString = strings.TrimRight(accessString, " \r\n")
		}
	}

	accessUrl, err := url.Parse(accessString)
	if err != nil || !accessUrl.IsAbs() {
		return nil, ErrAccessStringNotUrl
	}

	return accessUrl, nil
}
