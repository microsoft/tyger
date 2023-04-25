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
	"os"
	"strings"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/controlplane/model"
	"github.com/fatih/color"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"go.opentelemetry.io/otel/propagation"
)

func InvokeRequest(ctx context.Context, method string, relativeUri string, input interface{}, output interface{}) (*http.Response, error) {
	serviceInfo, err := GetPersistedServiceInfo()
	if err != nil || serviceInfo.GetServerUri() == "" {
		return nil, errors.New("run 'tyger login' to connect to a Tyger server")
	}

	token, err := serviceInfo.GetAccessToken()
	if err != nil {
		return nil, fmt.Errorf("run `tyger login` to login to a server: %v", err)
	}

	absoluteUri := fmt.Sprintf("%s/%s", serviceInfo.GetServerUri(), relativeUri)
	var body io.Reader = nil
	if input != nil {
		serializedBody, err := json.Marshal(input)
		if err != nil {
			return nil, fmt.Errorf("unable to serialize payload: %v", err)
		}
		body = bytes.NewBuffer(serializedBody)
	}

	req, err := http.NewRequest(method, absoluteUri, body)
	if err != nil {
		return nil, err
	}

	propagation.Baggage{}.Inject(ctx, propagation.HeaderCarrier(req.Header))

	req.Header.Set("Content-Type", "application/json")

	if zerolog.GlobalLevel() <= zerolog.TraceLevel {
		if token != "" {
			req.Header.Add("Authorization", "Bearer --REDACTED--")
		}
		if debugOutput, err := httputil.DumpRequestOut(req, true); err == nil {
			log.Trace().Str("request", string(debugOutput)).Msg("request sent")
		}
	}

	// add the Authorization token after dumping the request so we don't write out the token
	if token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	}

	resp, err := NewRetryableClient().Do(req)
	if err != nil {
		return resp, fmt.Errorf("unable to connect to server: %v", err)
	}

	if zerolog.GlobalLevel() <= zerolog.TraceLevel {
		if debugOutput, err := httputil.DumpResponse(resp, true); err == nil {
			log.Trace().Str("response", string(debugOutput)).Msg("response received")
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errorResponse := model.ErrorResponse{}
		if err = json.NewDecoder(resp.Body).Decode(&errorResponse); err == nil {
			return resp, fmt.Errorf("%s: %s", errorResponse.Error.Code, errorResponse.Error.Message)
		}

		return resp, fmt.Errorf("unexpected status code %s", resp.Status)
	}

	if output != nil {
		err = json.NewDecoder(resp.Body).Decode(output)
	}

	if err != nil {
		return resp, fmt.Errorf("unable to understand server response: %v", err)
	}

	return resp, nil
}

func InvokePageRequests[T any](ctx context.Context, uri string, limit int, warnIfTruncated bool) error {
	firstPage := true
	totalPrinted := 0
	truncated := false

	for uri != "" {
		page := model.Page[T]{}
		_, err := InvokeRequest(ctx, http.MethodGet, uri, nil, &page)
		if err != nil {
			return err
		}

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
			if totalPrinted == limit {
				truncated = i < len(page.Items)-1 || page.NextLink != ""
				goto End
			}
		}

		firstPage = false
		uri = strings.TrimLeft(page.NextLink, "/")
	}
End:
	fmt.Println("\n]")

	if warnIfTruncated && truncated {
		color.New(color.FgYellow).Fprintln(os.Stderr, "Warning: the output was truncated. Specify the --limit parameter to increase the number of elements.")
	}

	return nil
}
