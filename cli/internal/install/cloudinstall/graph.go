// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/rs/zerolog/log"
)

var errNotFound = fmt.Errorf("not found")
var errMultipleFound = fmt.Errorf("multiple found")

func GetGraphToken(ctx context.Context, cred azcore.TokenCredential) (azcore.AccessToken, error) {
	tokenResponse, err := cred.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"https://graph.microsoft.com"},
	})
	if err != nil {
		return azcore.AccessToken{}, fmt.Errorf("failed to get a Graph access token: %w", err)
	}

	return tokenResponse, nil
}

type aadAppApi struct {
	RequestedAccessTokenVersion int                               `json:"requestedAccessTokenVersion,omitempty"`
	Oauth2PermissionScopes      []*aadAppAuth2PermissionScope     `json:"oauth2PermissionScopes,omitempty"`
	PreAuthorizedApplications   []*aadAppPreAuthorizedApplication `json:"preAuthorizedApplications,omitempty"`
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
	ResourceAppId  string                  `json:"resourceAppId,omitempty"`
	ResourceAccess []*aadAppResourceAccess `json:"resourceAccess,omitempty"`
}

type aadAppPublicClient struct {
	RedirectUris []string `json:"redirectUris,omitempty"`
}

type aadAppRole struct {
	Id                 string   `json:"id,omitempty"`
	Description        string   `json:"description,omitempty"`
	DisplayName        string   `json:"displayName,omitempty"`
	Value              string   `json:"value,omitempty"`
	AllowedMemberTypes []string `json:"allowedMemberTypes,omitempty"`
	IsEnabled          bool     `json:"isEnabled,omitempty"`
}

type aadApp struct {
	Id                         string                          `json:"id,omitempty"`
	AppId                      string                          `json:"appId,omitempty"`
	DisplayName                string                          `json:"displayName,omitempty"`
	IdentifierUris             []string                        `json:"identifierUris,omitempty"`
	SignInAudience             string                          `json:"signInAudience,omitempty"`
	ServiceManagementReference string                          `json:"serviceManagementReference,omitempty"`
	Api                        *aadAppApi                      `json:"api,omitempty"`
	RequiredResourceAccess     []*aadAppRequiredResourceAccess `json:"requiredResourceAccess,omitempty"`
	IsFallbackPublicClient     bool                            `json:"isFallbackPublicClient,omitempty"`
	PublicClient               *aadAppPublicClient             `json:"publicClient,omitempty"`
	AppRoles                   []*aadAppRole                   `json:"appRoles,omitempty"`
}

type aadAppPreAuthorizedApplication struct {
	AppId         string   `json:"appId,omitempty"`
	PermissionIds []string `json:"permissionIds,omitempty"`
}

type aadServicePrincipal struct {
	Id       string        `json:"id,omitempty"`
	AppRoles []*aadAppRole `json:"appRoles,omitempty"`
}

type aadAppRoleAssignment struct {
	Id                   string `json:"id,omitempty"`
	AppRoleId            string `json:"appRoleId,omitempty"`
	PrincipalId          string `json:"principalId,omitempty"`
	PrincipalType        string `json:"principalType,omitempty"`
	PrincipalDisplayName string `json:"principalDisplayName,omitempty"`
	ResourceId           string `json:"resourceId,omitempty"`
}

func GetAppByAppIdOrUri(ctx context.Context, cred azcore.TokenCredential, appId, uri string) (*aadApp, error) {
	if appId != "" {
		response := aadApp{}
		if err := executeGraphCall(ctx, cred, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/beta/applications(appId='%s')", appId), nil, &response); err != nil {
			return nil, fmt.Errorf("failed to get app by id: %w", err)
		}

		for _, identifierUri := range response.IdentifierUris {
			if uri != "" {
				if identifierUri == uri {
					return &response, nil
				}
			}
		}

		return nil, fmt.Errorf("app with appId %s does not have URI %s", appId, uri)
	}

	if uri == "" {
		return nil, errNotFound
	}

	type responseType struct {
		Value []aadApp `json:"value"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, cred, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/beta/applications/?$filter=identifierUris/any(x:x%%20eq%%20'%s')", url.PathEscape(uri)), nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get app by uri: %w", err)
	}

	if len(response.Value) == 0 {
		return nil, errNotFound
	}

	if len(response.Value) > 1 {
		return nil, errMultipleFound
	}

	return &response.Value[0], nil
}

func GetAppByUri(ctx context.Context, cred azcore.TokenCredential, uri string) (*aadApp, error) {
	type responseType struct {
		Value []aadApp `json:"value"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, cred, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/beta/applications/?$filter=identifierUris/any(x:x%%20eq%%20'%s')", url.PathEscape(uri)), nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get app by uri: %w", err)
	}

	if len(response.Value) == 0 {
		return nil, errNotFound
	}

	if len(response.Value) > 1 {
		return nil, errMultipleFound
	}

	return &response.Value[0], nil
}

func GetServicePrincipalByUri(ctx context.Context, cred azcore.TokenCredential, uri string) (*aadServicePrincipal, error) {
	type responseType struct {
		Value []aadServicePrincipal `json:"value"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, cred, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/beta/servicePrincipals/?$filter=servicePrincipalNames/any(x:x%%20eq%%20'%s')", url.PathEscape(uri)), nil, &response); err != nil {
		return nil, fmt.Errorf("failed to get app by uri: %w", err)
	}

	if len(response.Value) == 0 {
		return nil, errNotFound
	}

	return &response.Value[0], nil
}

func ObjectsIdToPrincipals(ctx context.Context, cred azcore.TokenCredential, objectIds []string) ([]Principal, error) {
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
	err := executeGraphCall(ctx, cred, http.MethodPost, "https://graph.microsoft.com/v1.0/directoryObjects/getByIds", requestBody, &responseBody)
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
					ObjectId: value.Id,
					Kind:     kind,
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

func executeGraphCall(ctx context.Context, cred azcore.TokenCredential, method, url string, request, response any) error {
	var requestBodyReader io.Reader
	if request != nil {
		requestBytes, err := json.Marshal(request)
		if err != nil {
			return fmt.Errorf("failed to marshal request body: %w", err)
		}
		requestBodyReader = bytes.NewBuffer(requestBytes)
	}

	req, err := retryablehttp.NewRequestWithContext(ctx, method, url, requestBodyReader)
	if err != nil {
		return fmt.Errorf("failed to create request: %w", err)
	}

	tokenResponse, err := GetGraphToken(ctx, cred)
	if err != nil {
		return err
	}

	req.Header.Set("Authorization", fmt.Sprintf("Bearer %s", tokenResponse.Token))
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.DefaultClient.Do(req)
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

func GetServicePrincipalByAppId(ctx context.Context, cred azcore.TokenCredential, appId string) (string, error) {
	type responseType struct {
		Value []struct {
			Id string `json:"id"`
		} `json:"value"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, cred, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/v1.0/servicePrincipals/?$filter=appId%%20eq%%20'%s'", appId), nil, &response); err != nil {
		return "", fmt.Errorf("failed to get service principal by app id: %w", err)
	}

	if len(response.Value) == 0 {
		return "", errNotFound
	}

	return response.Value[0].Id, nil
}

func GetServicePrincipalDisplayName(ctx context.Context, cred azcore.TokenCredential, objectId string) (string, error) {
	type responseType struct {
		DisplayName string `json:"displayName"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, cred, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/beta/servicePrincipals/%s", objectId), nil, &response); err != nil {
		return "", fmt.Errorf("failed to get service principal details: %w", err)
	}

	return response.DisplayName, nil
}

func GetObjectIdByServicePrincipalDisplayName(ctx context.Context, cred azcore.TokenCredential, displayName string) (string, error) {
	type responseType struct {
		Value []struct {
			Id string `json:"id"`
		} `json:"value"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, cred, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/beta/servicePrincipals?$filter=displayName%%20eq%%20'%s'", url.PathEscape(displayName)), nil, &response); err != nil {
		return "", fmt.Errorf("failed to get service principal by display name: %w", err)
	}

	if len(response.Value) == 0 {
		return "", errNotFound
	}

	if len(response.Value) > 1 {
		return "", errMultipleFound
	}

	return response.Value[0].Id, nil
}

func GetUserPrincipalName(ctx context.Context, cred azcore.TokenCredential, objectId string) (string, error) {
	type responseType struct {
		UserPrincipalName string `json:"userPrincipalName"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, cred, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s?$select=userPrincipalName", objectId), nil, &response); err != nil {
		return "", fmt.Errorf("failed to get user with object ID '%s': %w", objectId, err)
	}

	return response.UserPrincipalName, nil
}

func GetObjectIdByUserPrincipalName(ctx context.Context, cred azcore.TokenCredential, userPrincipalName string) (string, error) {
	type responseType struct {
		Id string `json:"id"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, cred, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/v1.0/users/%s?$select=id", url.PathEscape(userPrincipalName)), nil, &response); err != nil {
		return "", fmt.Errorf("failed to get user by userPrincipalName '%s': %w", userPrincipalName, err)
	}

	return response.Id, nil
}

func GetObjectIdByGroupDisplayName(ctx context.Context, cred azcore.TokenCredential, displayName string) (string, error) {
	type responseType struct {
		Value []struct {
			Id string `json:"id"`
		} `json:"value"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, cred, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/v1.0/groups?$filter=displayName%%20eq%%20'%s'", url.PathEscape(displayName)), nil, &response); err != nil {
		return "", fmt.Errorf("failed to get group by display name: %w", err)
	}

	if len(response.Value) == 0 {
		return "", errNotFound
	}

	if len(response.Value) > 1 {
		return "", errMultipleFound
	}

	return response.Value[0].Id, nil
}

func GetGroupDisplayName(ctx context.Context, cred azcore.TokenCredential, objectId string) (string, error) {
	type responseType struct {
		DisplayName string `json:"displayName"`
	}

	response := responseType{}
	if err := executeGraphCall(ctx, cred, http.MethodGet, fmt.Sprintf("https://graph.microsoft.com/v1.0/groups/%s?$select=displayName", objectId), nil, &response); err != nil {
		return "", fmt.Errorf("failed to get group with object ID '%s': %w", objectId, err)
	}

	return response.DisplayName, nil
}

func CreateServicePrincipal(ctx context.Context, cred azcore.TokenCredential, appId string) (string, error) {
	type requestType struct {
		AppId string `json:"appId"`
	}

	type responseType struct {
		Id string `json:"id"`
	}

	requestBody := requestType{
		AppId: appId,
	}

	log.Ctx(ctx).Info().Msgf("Creating service principal for app %s", appId)
	response := responseType{}
	if err := executeGraphCall(ctx, cred, http.MethodPost, "https://graph.microsoft.com/beta/servicePrincipals", requestBody, &response); err != nil {
		return "", fmt.Errorf("failed to create service principal: %w", err)
	}

	return response.Id, nil
}

func assignAppRole(ctx context.Context, cred azcore.TokenCredential, serverServicePrincipalId, roleId string, assignment Principal) error {
	type requestType struct {
		AppRoleId   string `json:"appRoleId"`
		PrincipalId string `json:"principalId"`
		ResourceId  string `json:"resourceId"`
	}

	requestBody := requestType{
		AppRoleId:   roleId,
		PrincipalId: assignment.ObjectId,
		ResourceId:  serverServicePrincipalId,
	}

	if err := executeGraphCall(ctx, cred, http.MethodPost, "https://graph.microsoft.com/beta/servicePrincipals/"+serverServicePrincipalId+"/appRoleAssignments", requestBody, nil); err != nil {
		return fmt.Errorf("failed to assign app role: %w", err)
	}

	return nil
}

func removeAppRoleAssignment(ctx context.Context, cred azcore.TokenCredential, assignment aadAppRoleAssignment) error {
	type requestType struct {
		Id string `json:"id"`
	}

	requestBody := requestType{
		Id: assignment.AppRoleId,
	}

	if err := executeGraphCall(ctx, cred, http.MethodDelete, fmt.Sprintf("https://graph.microsoft.com/beta/servicePrincipals/%s/appRoleAssignments/%s", assignment.ResourceId, assignment.Id), requestBody, nil); err != nil {
		return fmt.Errorf("failed to remove app role assignment: %w", err)
	}

	return nil
}
