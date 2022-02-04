package cmd

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httputil"
	"net/url"
	"os"

	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/clicontext"
	"dev.azure.com/msresearch/compimag/_git/tyger/cli/internal/model"
	"github.com/spf13/cobra"
)

func exactlyOneArg(argName string) func(cmd *cobra.Command, args []string) error {
	return func(cmd *cobra.Command, args []string) error {
		if len(args) < 1 {
			return fmt.Errorf("one %s positional argument is required", argName)
		}
		if len(args) > 1 {
			return fmt.Errorf("unexpected positional arguments after the %s: %v", argName, args[1:])
		}
		return nil
	}
}

func invokeRequest(method string, relativeUri string, input interface{}, output interface{}, verbose bool) (*http.Response, error) {
	ctx, err := clicontext.GetCliContext()
	if err != nil {
		return nil, err
	}

	if uri, err := url.Parse(ctx.GetServerUri()); err != nil || !uri.IsAbs() {
		return nil, errors.New("run 'tyger login' to connect to a Tyger server")
	}

	token, err := ctx.GetAccessToken()
	if err != nil {
		return nil, fmt.Errorf("run 'tyger login' to connect to a Tyger server: %v", err)
	}

	absoluteUri := fmt.Sprintf("%s/%s", ctx.GetServerUri(), relativeUri)
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
	req.Header.Set("Content-Type", "application/json")

	if verbose {
		if token != "" {
			req.Header.Add("Authorization", "Bearer --REDACTED--")
		}
		if debugOutput, err := httputil.DumpRequestOut(req, true); err == nil {
			fmt.Fprintln(os.Stderr, "====REQUEST====")
			fmt.Fprintln(os.Stderr, string(debugOutput))
		}
	}

	// add the Authorization token after dumping the request so we don't write out the token
	if token != "" {
		req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", token))
	}

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return resp, fmt.Errorf("unable to connect to server: %v", err)
	}

	if verbose {
		if debugOutput, err := httputil.DumpResponse(resp, true); err == nil {
			fmt.Fprintln(os.Stderr, "====RESPONSE====")
			fmt.Fprintln(os.Stderr, string(debugOutput))
		}
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		errorResponse := model.ErrorResponse{}
		if err = json.NewDecoder(resp.Body).Decode(&errorResponse); err == nil {
			return resp, fmt.Errorf("%s: %s", errorResponse.Error.Code, errorResponse.Error.Message)
		}

		return resp, fmt.Errorf("unexpected status code %s", resp.Status)
	}

	err = json.NewDecoder(resp.Body).Decode(output)

	if err != nil {
		return resp, fmt.Errorf("unable to understand server response: %v", err)
	}

	return resp, nil
}
