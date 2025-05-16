// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dataplane

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/controlplane"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
)

var (
	ErrAccessStringNotUrl = errors.New("the buffer access string is invalid. It must be a URL or the path of a file whose contents is a URL")
)

func GetUrlFromAccessString(accessString string) (*url.URL, error) {
	if fi, err := os.Stat(accessString); err == nil && !fi.IsDir() {
		if fi.Size() < 2*1024 {
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

func GetNewBufferAccessUrl(ctx context.Context, bufferId string, writable bool, accessTtl string) (*url.URL, error) {
	bufferAccess := model.BufferAccess{}

	queryOptions := url.Values{}
	queryOptions.Add("writeable", strconv.FormatBool(writable))
	if accessTtl != "" {
		queryOptions.Add("ttl", accessTtl)
	}

	tygerClient, err := controlplane.GetClientFromCache()
	if err == nil {
		// We're ignoring the error here and will let InvokeRequest handle it
		switch tygerClient.ConnectionType() {
		case client.TygerConnectionTypeDocker:
			queryOptions.Add("preferTcp", "true")
			if os.Getenv("TYGER_ACCESSING_FROM_DOCKER") == "1" {
				queryOptions.Add("fromDocker", "true")
			}
		}
	}

	requestUrl := fmt.Sprintf("/buffers/%s/access", bufferId)
	_, err = controlplane.InvokeRequest(ctx, http.MethodPost, requestUrl, queryOptions, nil, &bufferAccess)
	if err != nil {
		return nil, err
	}

	return url.Parse(bufferAccess.Uri)
}

func getSasExpirationTime(accessUrl *url.URL) (time.Time, error) {
	queryString := accessUrl.Query()

	se := queryString.Get("se")
	if se == "" {
		return time.Time{}, fmt.Errorf("SAS expiration not found in access URL %s", accessUrl)
	}

	seParsed, err := time.Parse(time.RFC3339, se)
	if err != nil {
		return time.Time{}, fmt.Errorf("error parsing SAS expiration time %s: %w", se, err)
	}

	return seParsed, nil
}
