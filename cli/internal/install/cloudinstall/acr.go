// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	helmclient "github.com/mittwald/go-helm-client"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	helmregistry "helm.sh/helm/v3/pkg/registry"
)

// Returns the *.azurecr.io host of an OCI chart reference (e.g. for
// "oci://foo.azurecr.io/helm/bar" returns "foo.azurecr.io", true). Returns
// false for non-OCI refs or registries that aren't ACRs.
func acrHostFromOciRef(ref string) (string, bool) {
	if !strings.HasPrefix(ref, "oci://") {
		return "", false
	}
	rest := strings.TrimPrefix(ref, "oci://")
	host, _, _ := strings.Cut(rest, "/")
	if !strings.HasSuffix(host, ".azurecr.io") {
		return "", false
	}
	return host, true
}

// Captures the runtime-resolved properties of an Azure Container Registry:
// its short name, fully-qualified login server, and the resource group it
// lives in.
type ResolvedAcr struct {
	Name          string
	LoginServer   string
	ResourceGroup string
}

type acrImportPaths struct {
	SourceImage string
	// Exactly one target mode is set. ACR's ImportImage API accepts repo[:tag]
	// values in TargetTags, so tagged sources can be copied directly to the
	// mirrored tag. Digest-pinned sources are different: the rendered manifests
	// keep using repo@sha256:..., but TargetTags cannot contain a digest. For
	// those, request a manifest-only copy into the target repository so the same
	// digest is addressable from the mirror without inventing a tag.
	TargetTag                 string
	TargetRepositoryForDigest string
}

// Looks up the login server FQDN and resource group of the named ACR.
func (inst *Installer) resolveAcr(ctx context.Context, acrName string) (*ResolvedAcr, error) {
	resourceID, err := getContainerRegistryId(ctx, acrName, inst.Config.Cloud.SubscriptionID, inst.Credential)
	if err != nil {
		return nil, fmt.Errorf("failed to find ACR '%s': %w", acrName, err)
	}

	// Resource ID format: /subscriptions/{sub}/resourceGroups/{rg}/providers/...
	parts := strings.Split(resourceID, "/")
	var resourceGroup string
	for i, p := range parts {
		if strings.EqualFold(p, "resourceGroups") && i+1 < len(parts) {
			resourceGroup = parts[i+1]
			break
		}
	}
	if resourceGroup == "" {
		return nil, fmt.Errorf("failed to parse resource group from ACR resource ID: %s", resourceID)
	}

	client, err := armcontainerregistry.NewRegistriesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return nil, err
	}

	resp, err := client.Get(ctx, resourceGroup, acrName, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to get ACR '%s': %w", acrName, err)
	}
	if resp.Properties == nil || resp.Properties.LoginServer == nil {
		return nil, fmt.Errorf("ACR '%s' has no login server", acrName)
	}

	return &ResolvedAcr{
		Name:          acrName,
		LoginServer:   *resp.Properties.LoginServer,
		ResourceGroup: resourceGroup,
	}, nil
}

// Imports an image into target using the ARM ImportImage API.
func (inst *Installer) importImageToAcr(ctx context.Context, target *ResolvedAcr, sourceRegistryHost string, paths acrImportPaths) error {
	client, err := armcontainerregistry.NewRegistriesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create container registry client: %w", err)
	}

	source, err := inst.makeImportSource(ctx, sourceRegistryHost, paths.SourceImage)
	if err != nil {
		return err
	}

	parameters := armcontainerregistry.ImportImageParameters{
		Source: source,
		Mode:   Ptr(armcontainerregistry.ImportModeForce),
	}
	switch {
	case paths.TargetTag != "" && paths.TargetRepositoryForDigest != "":
		return fmt.Errorf("invalid ACR import target for %s: target tag and digest target repository are mutually exclusive", paths.SourceImage)
	case paths.TargetTag != "":
		parameters.TargetTags = []*string{Ptr(paths.TargetTag)}
	case paths.TargetRepositoryForDigest != "":
		parameters.UntaggedTargetRepositories = []*string{Ptr(paths.TargetRepositoryForDigest)}
	default:
		return fmt.Errorf("invalid ACR import target for %s: target tag or digest target repository is required", paths.SourceImage)
	}

	poller, err := client.BeginImportImage(ctx, target.ResourceGroup, target.Name, parameters, nil)
	if err != nil {
		return fmt.Errorf("failed to start import of %s/%s into '%s': %w", sourceRegistryHost, paths.SourceImage, target.Name, err)
	}
	if _, err := poller.PollUntilDone(ctx, nil); err != nil {
		return fmt.Errorf("failed to import %s/%s into '%s': %w", sourceRegistryHost, paths.SourceImage, target.Name, err)
	}

	return nil
}

// Pulls a chart from a traditional helm repo and pushes it to target as an
// OCI artifact at "<target>/<targetRepoPath>:<version>".
func (inst *Installer) pullAndPushHelmChart(ctx context.Context, target *ResolvedAcr, chartName, version, repoUrl, targetRepoPath string) error {
	registryClient, err := inst.newAcrHelmRegistryClient(ctx, target.LoginServer)
	if err != nil {
		return fmt.Errorf("failed to create registry client: %w", err)
	}

	pull := action.NewPullWithOpts(action.WithConfig(&action.Configuration{}))
	pull.Settings = cli.New()
	pull.Version = version
	pull.RepoURL = repoUrl
	pull.SetRegistryClient(registryClient)

	tmpDir, err := os.MkdirTemp("", "tyger-acr-chart-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)
	pull.DestDir = tmpDir

	if _, err := pull.Run(chartName); err != nil {
		return fmt.Errorf("failed to pull chart %s: %w", chartName, err)
	}

	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		return fmt.Errorf("failed to read temp dir: %w", err)
	}
	var chartData []byte
	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".tgz") {
			chartData, err = os.ReadFile(fmt.Sprintf("%s/%s", tmpDir, entry.Name()))
			if err != nil {
				return fmt.Errorf("failed to read chart file: %w", err)
			}
			break
		}
	}
	if chartData == nil {
		return fmt.Errorf("chart package not found after pulling %s", chartName)
	}

	targetRef := fmt.Sprintf("%s/%s:%s", target.LoginServer, targetRepoPath, version)
	if _, err := registryClient.Push(chartData, targetRef); err != nil {
		return fmt.Errorf("failed to push chart %s: %w", targetRef, err)
	}
	return nil
}

// Builds an ImportSource, using ResourceID for private ACRs (*.azurecr.io)
// and RegistryURI for public registries.
func (inst *Installer) makeImportSource(ctx context.Context, registryHost, sourceImage string) (*armcontainerregistry.ImportSource, error) {
	if strings.HasSuffix(registryHost, ".azurecr.io") {
		acrName := strings.TrimSuffix(registryHost, ".azurecr.io")
		resourceID, err := getContainerRegistryId(ctx, acrName, inst.Config.Cloud.SubscriptionID, inst.Credential)
		if err != nil {
			return nil, fmt.Errorf("failed to get resource ID for ACR '%s': %w", acrName, err)
		}
		return &armcontainerregistry.ImportSource{
			ResourceID:  Ptr(resourceID),
			SourceImage: Ptr(sourceImage),
		}, nil
	}
	return &armcontainerregistry.ImportSource{
		RegistryURI: Ptr(registryHost),
		SourceImage: Ptr(sourceImage),
	}, nil
}

// Creates a helm registry client logged in to the given ACR.
func (inst *Installer) newAcrHelmRegistryClient(ctx context.Context, acrFqdn string) (*helmregistry.Client, error) {
	refreshToken, err := inst.getAcrRefreshToken(ctx, acrFqdn)
	if err != nil {
		return nil, err
	}

	client, err := helmregistry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create helm registry client: %w", err)
	}
	if err := client.Login(acrFqdn,
		helmregistry.LoginOptBasicAuth("00000000-0000-0000-0000-000000000000", refreshToken)); err != nil {
		return nil, fmt.Errorf("failed to log in to ACR '%s': %w", acrFqdn, err)
	}
	return client, nil
}

// Logs the registry client embedded in a helmclient.Client in to the given
// ACR using an exchanged ACR refresh token.
func (inst *Installer) loginHelmClientToAcr(ctx context.Context, helmClient helmclient.Client, acrFqdn string) error {
	hc, ok := helmClient.(*helmclient.HelmClient)
	if !ok {
		return fmt.Errorf("unable to access helm registry client for ACR login")
	}
	refreshToken, err := inst.getAcrRefreshToken(ctx, acrFqdn)
	if err != nil {
		return fmt.Errorf("failed to get ACR refresh token: %w", err)
	}
	if err := hc.ActionConfig.RegistryClient.Login(acrFqdn,
		helmregistry.LoginOptBasicAuth("00000000-0000-0000-0000-000000000000", refreshToken)); err != nil {
		return fmt.Errorf("failed to login to ACR '%s': %w", acrFqdn, err)
	}
	return nil
}

// Exchanges an AAD token for an ACR refresh token via the /oauth2/exchange
// endpoint.
func (inst *Installer) getAcrRefreshToken(ctx context.Context, acrFqdn string) (string, error) {
	aadToken, err := inst.Credential.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"https://management.azure.com/.default"},
	})
	if err != nil {
		return "", fmt.Errorf("failed to get AAD token: %w", err)
	}

	exchangeURL := fmt.Sprintf("https://%s/oauth2/exchange", acrFqdn)
	return exchangeAcrRefreshToken(ctx, http.DefaultClient, exchangeURL, acrFqdn, inst.Config.Cloud.TenantID, aadToken.Token)
}

type httpDoer interface {
	Do(*http.Request) (*http.Response, error)
}

func exchangeAcrRefreshToken(ctx context.Context, client httpDoer, exchangeURL, acrFqdn, tenantID, aadAccessToken string) (string, error) {
	formData := url.Values{}
	formData.Set("grant_type", "access_token")
	formData.Set("service", acrFqdn)
	formData.Set("tenant", tenantID)
	formData.Set("access_token", aadAccessToken)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exchangeURL, strings.NewReader(formData.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to exchange ACR token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, err := io.ReadAll(resp.Body)
		if err != nil {
			return "", fmt.Errorf("ACR token exchange failed (status %d) and failed to read response body: %w", resp.StatusCode, err)
		}
		return "", fmt.Errorf("ACR token exchange failed (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode ACR exchange response: %w", err)
	}
	if result.RefreshToken == "" {
		return "", fmt.Errorf("ACR token exchange response did not include a refresh token")
	}
	return result.RefreshToken, nil
}
