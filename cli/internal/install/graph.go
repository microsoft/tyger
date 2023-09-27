package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
)

type PrincipalKind string

const (
	PrincipalKindUser             PrincipalKind = "User"
	PrincipalKindGroup            PrincipalKind = "Group"
	PrincipalKindServicePrincipal PrincipalKind = "ServicePrincipal"
)

type Principal struct {
	Id   string
	Kind PrincipalKind
}

func executeGraphCall(ctx context.Context, method, url string, request, response any) error {
	var requestBodyReader io.Reader
	if request != nil {
		requestBytes, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		requestBodyReader = bytes.NewBuffer(requestBytes)
	}

	req, err := http.NewRequest(method, "https://graph.microsoft.com/v1.0/directoryObjects/getByIds", requestBodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	cred := GetAzureCredentialFromContext(ctx)
	config := GetConfigFromContext(ctx)
	tokenResponse, err := cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes:   []string{"https://graph.microsoft.com"},
		TenantID: config.Cloud.TenantID,
	})
	if err != nil {
		return fmt.Errorf("failed to get token: %w", err)
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenResponse.Token))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("graph call failed: %w", err)
	}

	if resp.StatusCode != http.StatusOK {

		var buf strings.Builder
		io.Copy(&buf, resp.Body)
		return fmt.Errorf("graph call failed with status code %d and body: %s", resp.StatusCode, buf.String())
	}

	if response != nil {
		err = json.NewDecoder(resp.Body).Decode(response)
		if err != nil {
			return fmt.Errorf("failed to decode response body: %w", err)
		}
	}

	return nil
}

func ObjectsIdToPrincipals(ctx context.Context, objectIds []string) ([]Principal, error) {
	type requestType struct {
		Ids []string `json:"ids"`
	}

	type responseValueType struct {
		Type string `json:"@odata.type"`
		Id   string `json:"id"`
	}

	type responseType struct {
		Value []responseValueType `json:"value"`
	}

	requestBody := requestType{
		Ids: objectIds,
	}

	var responseBody responseType
	err := executeGraphCall(ctx, http.MethodPost, "https://graph.microsoft.com/v1.0/directoryObjects/getByIds", requestBody, &responseBody)
	if err != nil {
		return nil, fmt.Errorf("failed to get principal types: %w", err)
	}

	principals := make([]Principal, len(objectIds))
	for i, inputId := range objectIds {
		inputId = strings.ToLower(inputId)
		var principal *Principal
		for _, value := range responseBody.Value {
			if inputId == strings.ToLower(value.Id) {
				var kind PrincipalKind
				switch value.Type {
				case "#microsoft.graph.user":
					kind = PrincipalKindUser
				case "#microsoft.graph.group":
					kind = PrincipalKindGroup
				case "#microsoft.graph.servicePrincipal":
					kind = PrincipalKindServicePrincipal
				default:
					return nil, fmt.Errorf("unknown principal type %s", value.Type)
				}

				principal = &Principal{
					Id:   value.Id,
					Kind: kind,
				}
				break
			}
		}
		if principal == nil {
			return nil, fmt.Errorf("no principal found for object id %s", inputId)
		}
		principals[i] = *principal
	}

	return principals, nil
}
