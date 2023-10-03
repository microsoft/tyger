package install

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/rs/zerolog/log"
)

type PrincipalKind string

const (
	PrincipalKindUser             PrincipalKind = "User"
	PrincipalKindGroup            PrincipalKind = "Group"
	PrincipalKindServicePrincipal PrincipalKind = "ServicePrincipal"
)

var errNotFound = fmt.Errorf("not found")

type Principal struct {
	Id   string
	Kind PrincipalKind
}

func GetGraphToken(ctx context.Context) (azcore.AccessToken, error) {
	cred := GetAzureCredentialFromContext(ctx)
	tokenResponse, err := cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"https://graph.microsoft.com"},
	})
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("failed to get token: %w", err)
	}

	return tokenResponse, nil
}

type aadAppApi struct {
	RequestedAccessTokenVersion int                          `json:"requestedAccessTokenVersion,omitempty"`
	Oauth2PermissionScopes      []aadAppAuth2PermissionScope `json:"oauth2PermissionScopes,omitempty"`
}

type aadAppAuth2PermissionScope struct {
	Id                      string `json:"id,omitempty"`
	AdminConsentDescription string `json:"adminConsentDescription,omitempty"`
	AdminConsentDisplayName string `json:"adminConsentDisplayName,omitempty"`
	IsEnabled               bool   `json:"isEnabled,omitempty"`
	Type                    string `json:"type,omitempty"`
	UserConsentDescription  string `json:"userConsentDescription,omitempty"`
	UserConsentDisplayName  string `json:"userConsentDisplayName,omitempty"`
	Value                   string `json:"value,omitempty"`
}

type aadAppResourceAccess struct {
	Id   string `json:"id,omitempty"`
	Type string `json:"type,omitempty"`
}

type aadAppRequiredResourceAccess struct {
	ResourceAppId  string                 `json:"resourceAppId,omitempty"`
	ResourceAccess []aadAppResourceAccess `json:"resourceAccess,omitempty"`
}

type aadAppPublicClient struct {
	RedirectUris []string `json:"redirectUris,omitempty"`
}

type aadApp struct {
	Id                     string                         `json:"id,omitempty"`
	DisplayName            string                         `json:"displayName,omitempty"`
	IdentifierUris         []string                       `json:"identifierUris,omitempty"`
	SignInAudience         string                         `json:"signInAudience,omitempty"`
	Api                    aadAppApi                      `json:"api,omitempty"`
	RequiredResourceAccess []aadAppRequiredResourceAccess `json:"requiredResourceAccess,omitempty"`
	IsFallbackPublicClient bool                           `json:"isFallbackPublicClient,omitempty"`
	PublicClient           *aadAppPublicClient            `json:"publicClient,omitempty"`
}

func GetAppByUri(ctx context.Context, uri string) (aadApp, error) {
	type responseType struct {
		Value []aadApp `json:"value"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/beta/applications/?$filter=identifierUris/any(x:x%%20eq%%20'%s')", uri), nil, &response); err != nil {
		return aadApp{}, fmt.Errorf("failed to get app by uri: %w", err)
	}

	if len(response.Value) == 0 {
		return aadApp{}, errNotFound
	}

	return response.Value[0], nil
}

func CreateOrUpdateAppByUri(ctx context.Context, app aadApp) (objectId string, err error) {
	existingApp, err := GetAppByUri(ctx, app.IdentifierUris[0])
	if err != nil && err != errNotFound {
		return "", fmt.Errorf("failed to get existing app: %w", err)
	}

	if err == errNotFound {
		log.Info().Msgf("Creating app %s", app.IdentifierUris[0])
		err = executeGraphCall(ctx, http.MethodPost, "https://graph.microsoft.com/beta/applications", app, &existingApp)
	} else {
		log.Info().Msgf("Updating app %s", app.IdentifierUris[0])
		err = executeGraphCall(ctx, http.MethodPatch, fmt.Sprintf("https://graph.microsoft.com/beta/applications/%s", existingApp.Id), app, nil)
	}

	if err != nil {
		return "", fmt.Errorf("failed to create or update app: %w", err)
	}
	return existingApp.Id, nil
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

	req, err := http.NewRequest(method, url, requestBodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	tokenResponse, err := GetGraphToken(ctx)
	if err != nil {
		return nil
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenResponse.Token))
	req.Header.Set("Content-Type", "application/json")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("graph call failed: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
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

func GetServicePrincipalDisplayName(ctx context.Context, objectId string) (string, error) {
	type responseType struct {
		DisplayName string `json:"displayName"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/v1.0/servicePrincipals/%s", objectId), nil, &response); err != nil {
		return "", fmt.Errorf("failed to get service principal details: %w", err)
	}

	return response.DisplayName, nil
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
