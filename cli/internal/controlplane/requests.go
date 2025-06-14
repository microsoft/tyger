// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package controlplane

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"
	"strings"
	"time"

	"github.com/fatih/color"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/controlplane/model"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel/propagation"
)

type InvokeRequestOptions struct {
	Headers           http.Header
	LeaveResponseOpen bool
}

type InvokeRequestOptionFunc func(*InvokeRequestOptions)

func WithHeaders(headers http.Header) InvokeRequestOptionFunc {
	return func(options *InvokeRequestOptions) {
		options.Headers = headers
	}
}

func WithLeaveResponseOpen() InvokeRequestOptionFunc {
	return func(options *InvokeRequestOptions) {
		options.LeaveResponseOpen = true
	}
}

func InvokeRequest(ctx context.Context, method string, relativeUrl string, queryParams url.Values, input interface{}, output interface{}, options ...InvokeRequestOptionFunc) (*http.Response, error) {
	var opts *InvokeRequestOptions
	if len(options) > 0 {
		opts = &InvokeRequestOptions{}
		for _, option := range options {
			option(opts)
		}
	}

	tygerClient, err := GetClientFromCache()
	if err != nil || tygerClient.ControlPlaneUrl == nil {
		return nil, errors.New("run 'tyger login' to connect to a Tyger server")
	}

	token, err := tygerClient.GetAccessToken(ctx)
	if err != nil {
		return nil, fmt.Errorf("run `tyger login` to login to a server: %v", err)
	}

	if queryParams == nil {
		queryParams = url.Values{}
	}

	if strings.Contains(relativeUrl, "?") {
		url, err := url.Parse(relativeUrl)
		if err != nil {
			return nil, err
		}
		relativeUrl = url.Path
		for key, values := range url.Query() {
			for _, value := range values {
				queryParams.Add(key, value)
			}
		}
	}

	if _, exists := queryParams[ApiVersionQueryParam]; !exists {
		apiVersion := GetApiVersionFromContext(ctx)
		queryParams.Add(ApiVersionQueryParam, apiVersion)
	}

	absoluteUrl := tygerClient.ControlPlaneUrl.JoinPath(relativeUrl)
	absoluteUrl.RawQuery = queryParams.Encode()

	var body io.Reader = nil
	var serializedBody []byte
	if input != nil {
		serializedBody, err = json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("unable to serialize payload: %v", err)
		}
		body = bytes.NewBuffer(serializedBody)
	}

	req, err := retryablehttp.NewRequestWithContext(ctx, method, absoluteUrl.String(), body)
	if err != nil {
		return nil, err
	}

	propagation.Baggage{}.Inject(ctx, propagation.HeaderCarrier(req.Header))

	if options != nil && opts.Headers != nil {
		for key, values := range opts.Headers {
			for _, value := range values {
				req.Header.Add(key, value)
			}
		}
	}

	req.Header.Set("Content-Type", "application/json")

	if log.Logger.GetLevel() <= zerolog.TraceLevel {
		if token != "" {
			req.Header.Add("Authorization", "Bearer --REDACTED--")
		}
		req.Request.Body = io.NopCloser(bytes.NewBuffer(serializedBody))
		if debugOutput, err := httputil.DumpRequestOut(req.Request, true); err == nil {
			log.Trace().Str("request", string(debugOutput)).Msg("Outgoing request")
		}
	}

	// add the Authorization token after dumping the request so we don't write out the token
	if token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	}

	resp, err := tygerClient.ControlPlaneClient.Do(req)
	if err != nil {
		return resp, fmt.Errorf("unable to connect to server: %v", err)
	}

	if log.Logger.GetLevel() <= zerolog.TraceLevel {
		if debugOutput, err := httputil.DumpResponse(resp, true); err == nil {
			log.Trace().Str("response", string(debugOutput)).Msg("Incoming response")
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		defer resp.Body.Close()
		errorResponse := model.ErrorResponse{}
		if err = json.NewDecoder(resp.Body).Decode(&errorResponse); err == nil {
			return resp, &errorResponse.Error
		}

		return resp, fmt.Errorf("unexpected status code %s", resp.Status)
	}

	if options != nil && opts.LeaveResponseOpen {
		return resp, nil
	}

	defer resp.Body.Close()

	if output != nil {
		err = json.NewDecoder(resp.Body).Decode(output)
	}

	if err != nil {
		return resp, fmt.Errorf("unable to understand server response: %v", err)
	}

	return resp, nil
}

func InvokePageRequests[T any](ctx context.Context, requestUrl string, queryParams url.Values, totalLimit int, warnIfTruncated bool) error {
	firstPage := true
	totalPrinted := 0
	truncated := false

	for requestUrl != "" {
		page := model.Page[T]{}
		_, err := InvokeRequest(ctx, http.MethodGet, requestUrl, queryParams, nil, &page)
		if err != nil {
			return err
		}
		queryParams = nil // Clear query params for subsequent requests

		if firstPage && page.NextLink == "" {
			formattedRuns, err := json.MarshalIndent(page.Items, "  ", "  ")
			if err != nil {
				return err
			}

			fmt.Println(string(formattedRuns))
			return nil
		}

		if firstPage {
			fmt.Print("[\n  ")
		}

		for i, r := range page.Items {
			if !firstPage || i != 0 {
				fmt.Print(",\n  ")
			}

			formattedRun, err := json.MarshalIndent(r, "  ", "  ")
			if err != nil {
				return err
			}

			fmt.Print(string(formattedRun))
			totalPrinted++
			if totalPrinted == totalLimit {
				truncated = i < len(page.Items)-1 || page.NextLink != ""
				goto End
			}
		}

		firstPage = false
		requestUrl = strings.TrimLeft(page.NextLink, "/")
	}
End:
	fmt.Println("\n]")

	if warnIfTruncated && truncated {
		color.New(color.FgYellow).Fprintln(os.Stderr, "Warning: the output was truncated. Specify the --limit parameter to increase the number of elements.")
	}

	return nil
}

func SetFieldsOnEntity(ctx context.Context, relativeUrlPath string, queryParams url.Values, etag string, clearTags bool, tags map[string]string, expiresAt *time.Time, reponseObject any) error {
	type Resource struct {
		ETag      string            `json:"eTag"`
		Tags      map[string]string `json:"tags"`
		ExpiresAt *time.Time        `json:"expiresAt"`
	}

	for {
		resource := Resource{}
		var headers = make(http.Header)
		requestEtag := etag

		var newTagEntries map[string]string
		if clearTags {
			newTagEntries = tags
		} else {
			_, err := InvokeRequest(ctx, http.MethodGet, relativeUrlPath, queryParams, nil, &resource)
			if err != nil {
				return err
			}

			if etag != "" && etag != resource.ETag {
				return fmt.Errorf("the server's ETag does not match the provided ETag")
			}

			requestEtag = resource.ETag

			newTagEntries = make(map[string]string)
			for k, v := range resource.Tags {
				newTagEntries[k] = v
			}

			for k, v := range tags {
				newTagEntries[k] = v
			}
		}

		resource.Tags = newTagEntries

		if expiresAt != nil {
			resource.ExpiresAt = expiresAt
		}

		if etag != "" {
			headers.Set("If-Match", requestEtag)
		}

		resp, err := InvokeRequest(ctx, http.MethodPut, relativeUrlPath, queryParams, resource, &reponseObject, WithHeaders(headers))

		if err != nil {
			return err
		}

		if resp.StatusCode == http.StatusPreconditionFailed {
			if etag == "" {
				continue
			}
			return fmt.Errorf("the server's ETag does not match the provided ETag")
		}

		formattedBuffer, err := json.MarshalIndent(reponseObject, "", "  ")
		if err != nil {
			return err
		}

		fmt.Println(string(formattedBuffer))
		return nil
	}
}
