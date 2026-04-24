// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
	"sync"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerregistry/armcontainerregistry"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
	"helm.sh/helm/v3/pkg/action"
	"helm.sh/helm/v3/pkg/cli"
	"helm.sh/helm/v3/pkg/registry"
	corev1 "k8s.io/api/core/v1"
)

const (
	// MirrorRepoPrefix is the repository prefix used for all mirrored artifacts in the target ACR.
	MirrorRepoPrefix = "tyger"

	// Image constants for third-party charts.
	TraefikImageRegistry   = "mcr.microsoft.com"
	TraefikImageRepository = "oss/traefik/traefik"
	TraefikImageTag        = "v2.10.7"

	AzureLinuxImageRepository = "azurelinux/base/core"
	AzureLinuxImageTag        = "3.0"

	CertManagerImageTag = "1.12.15-4.3.0.20251206"

	NvidiaDevicePluginImageRepository = "oss/v2/nvidia/k8s-device-plugin"
	NvidiaDevicePluginImageTag        = "v0.17.0"
)

// MirrorableImage describes a container image that must be available in the mirror ACR.
type MirrorableImage struct {
	SourceRegistry string
	SourceRepo     string
	Tag            string
}

// SourceRef returns the full source image reference (registry/repo:tag or registry/repo@digest).
func (m MirrorableImage) SourceRef() string {
	sep := ":"
	if strings.HasPrefix(m.Tag, "sha256:") {
		sep = "@"
	}
	return fmt.Sprintf("%s/%s%s%s", m.SourceRegistry, m.SourceRepo, sep, m.Tag)
}

// ParseImageRef parses an image reference like "registry/repo:tag" or "registry/repo@sha256:..." into a MirrorableImage.
func ParseImageRef(ref string) (MirrorableImage, error) {
	// Split registry from the rest (first slash separates registry from repo)
	slashIdx := strings.Index(ref, "/")
	if slashIdx < 0 {
		return MirrorableImage{}, fmt.Errorf("invalid image reference %q: no registry", ref)
	}
	registry := ref[:slashIdx]
	remainder := ref[slashIdx+1:]

	// Split repo from tag/digest
	if atIdx := strings.Index(remainder, "@"); atIdx >= 0 {
		return MirrorableImage{SourceRegistry: registry, SourceRepo: remainder[:atIdx], Tag: remainder[atIdx+1:]}, nil
	}
	if colonIdx := strings.LastIndex(remainder, ":"); colonIdx >= 0 {
		return MirrorableImage{SourceRegistry: registry, SourceRepo: remainder[:colonIdx], Tag: remainder[colonIdx+1:]}, nil
	}
	return MirrorableImage{}, fmt.Errorf("invalid image reference %q: no tag or digest", ref)
}

// TargetRepo returns the repository path in the mirror ACR (under MirrorRepoPrefix).
func (m MirrorableImage) TargetRepo() string {
	return fmt.Sprintf("%s/%s", MirrorRepoPrefix, m.SourceRepo)
}

// TargetRef returns the full target image reference in the mirror ACR.
func (m MirrorableImage) TargetRef(mirrorAcrFqdn string) string {
	sep := ":"
	if strings.HasPrefix(m.Tag, "sha256:") {
		sep = "@"
	}
	return fmt.Sprintf("%s/%s%s%s", mirrorAcrFqdn, m.TargetRepo(), sep, m.Tag)
}

// MirrorableChart describes a helm chart that must be available in the mirror ACR.
type MirrorableChart struct {
	SourceChartRef string
	SourceRepoName string
	SourceRepoUrl  string
	Version        string
	IsOCI          bool
}

// TargetChartRef returns the OCI chart reference in the mirror ACR.
func (c MirrorableChart) TargetChartRef(mirrorAcrFqdn string) string {
	parts := strings.Split(c.SourceChartRef, "/")
	chartName := parts[len(parts)-1]
	return fmt.Sprintf("oci://%s/%s/helm/%s", mirrorAcrFqdn, MirrorRepoPrefix, chartName)
}

// GetTraefikImages returns the images used by the Traefik chart.
func GetTraefikImages() []MirrorableImage {
	return []MirrorableImage{
		{SourceRegistry: TraefikImageRegistry, SourceRepo: TraefikImageRepository, Tag: TraefikImageTag},
		{SourceRegistry: TraefikImageRegistry, SourceRepo: AzureLinuxImageRepository, Tag: AzureLinuxImageTag},
	}
}

// GetTraefikChart returns the chart descriptor for Traefik.
func GetTraefikChart() MirrorableChart {
	return MirrorableChart{
		SourceChartRef: "traefik/traefik",
		SourceRepoName: "traefik",
		SourceRepoUrl:  "https://helm.traefik.io/traefik",
		Version:        "24.0.0",
		IsOCI:          false,
	}
}

// CertManagerImageSet holds the cert-manager component images with named fields.
type CertManagerImageSet struct {
	Controller MirrorableImage
	AcmeSolver MirrorableImage
	CaInjector MirrorableImage
	Webhook    MirrorableImage
}

// All returns all cert-manager images as a slice.
func (s CertManagerImageSet) All() []MirrorableImage {
	return []MirrorableImage{s.Controller, s.AcmeSolver, s.CaInjector, s.Webhook}
}

func certManagerImage(component string) MirrorableImage {
	return MirrorableImage{
		SourceRegistry: "mcr.microsoft.com",
		SourceRepo:     "azurelinux/base/" + component,
		Tag:            CertManagerImageTag,
	}
}

// GetCertManagerImages returns the images used by the cert-manager chart.
func GetCertManagerImages() CertManagerImageSet {
	return CertManagerImageSet{
		Controller: certManagerImage("cert-manager-controller"),
		AcmeSolver: certManagerImage("cert-manager-acmesolver"),
		CaInjector: certManagerImage("cert-manager-cainjector"),
		Webhook:    certManagerImage("cert-manager-webhook"),
	}
}

// GetCertManagerChart returns the chart descriptor for cert-manager.
func GetCertManagerChart() MirrorableChart {
	return MirrorableChart{
		SourceChartRef: "oci://mcr.microsoft.com/azurelinux/helm/cert-manager",
		Version:        "1.12.12-12",
		IsOCI:          true,
	}
}

// GetNvidiaDevicePluginImages returns the images used by the NVIDIA device plugin chart.
func GetNvidiaDevicePluginImages() []MirrorableImage {
	return []MirrorableImage{
		{SourceRegistry: "mcr.microsoft.com", SourceRepo: NvidiaDevicePluginImageRepository, Tag: NvidiaDevicePluginImageTag},
	}
}

// GetNvidiaDevicePluginChart returns the chart descriptor for the NVIDIA device plugin.
func GetNvidiaDevicePluginChart() MirrorableChart {
	return MirrorableChart{
		SourceChartRef: "nvdp/nvidia-device-plugin",
		SourceRepoName: "nvdp",
		SourceRepoUrl:  "https://nvidia.github.io/k8s-device-plugin",
		Version:        "0.17.0",
		IsOCI:          false,
	}
}

// GetTygerImages returns the Tyger container images for the current build.
func GetTygerImages() []MirrorableImage {
	registry := install.ContainerRegistry
	dir := strings.TrimPrefix(strings.TrimSuffix(install.GetNormalizedContainerRegistryDirectory(), "/"), "/")
	tag := install.ContainerImageTag

	repos := []string{"tyger-server", "buffer-sidecar", "buffer-copier", "worker-waiter"}
	images := make([]MirrorableImage, len(repos))
	for i, repo := range repos {
		sourceRepo := repo
		if dir != "" {
			sourceRepo = dir + "/" + repo
		}
		images[i] = MirrorableImage{SourceRegistry: registry, SourceRepo: sourceRepo, Tag: tag}
	}
	return images
}

// GetTygerChart returns the chart descriptor for the Tyger helm chart.
func GetTygerChart() MirrorableChart {
	return MirrorableChart{
		SourceChartRef: fmt.Sprintf("oci://%s%shelm/tyger", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory()),
		Version:        install.ContainerImageTag,
		IsOCI:          true,
	}
}

// GetAllMirrorableImages returns every container image that must be mirrored.
func GetAllMirrorableImages() []MirrorableImage {
	var all []MirrorableImage
	all = append(all, GetTraefikImages()...)
	all = append(all, GetCertManagerImages().All()...)
	all = append(all, GetNvidiaDevicePluginImages()...)
	all = append(all, GetTygerImages()...)
	return all
}

// GetSharedMirrorableImages returns third-party images mirrored during cloud install.
func GetSharedMirrorableImages() []MirrorableImage {
	var all []MirrorableImage
	all = append(all, GetTraefikImages()...)
	all = append(all, GetCertManagerImages().All()...)
	all = append(all, GetNvidiaDevicePluginImages()...)
	return all
}

// GetSharedMirrorableCharts returns third-party charts mirrored during cloud install.
func GetSharedMirrorableCharts() []MirrorableChart {
	return []MirrorableChart{
		GetTraefikChart(),
		GetCertManagerChart(),
		GetNvidiaDevicePluginChart(),
	}
}

// MirrorSharedArtifacts mirrors third-party images and charts during cloud install.
func (inst *Installer) MirrorSharedArtifacts(ctx context.Context) error {
	if inst.Config.Cloud.MirrorAcr == "" {
		return nil
	}

	log.Ctx(ctx).Info().Msg("Mirroring shared artifacts to private ACR")

	mirrorAcrName := inst.Config.Cloud.GetMirrorAcrName()
	if err := inst.resolveMirrorAcrLoginServer(ctx, mirrorAcrName); err != nil {
		return fmt.Errorf("failed to resolve mirror ACR: %w", err)
	}

	if err := inst.mirrorImages(ctx, GetSharedMirrorableImages()); err != nil {
		return err
	}

	if err := inst.mirrorCharts(ctx, GetSharedMirrorableCharts()); err != nil {
		return err
	}

	return nil
}

// MirrorTygerArtifacts mirrors Tyger images and chart during api install.
func (inst *Installer) MirrorTygerArtifacts(ctx context.Context) error {
	if inst.Config.Cloud.MirrorAcr == "" {
		return nil
	}

	log.Ctx(ctx).Info().Msg("Mirroring Tyger artifacts to private ACR")

	mirrorAcrName := inst.Config.Cloud.GetMirrorAcrName()
	if err := inst.resolveMirrorAcrLoginServer(ctx, mirrorAcrName); err != nil {
		return fmt.Errorf("failed to resolve mirror ACR: %w", err)
	}

	images := GetTygerImages()
	images = append(images, inst.getOrgExtraImages()...)

	if err := inst.mirrorImages(ctx, images); err != nil {
		return err
	}

	if err := inst.mirrorCharts(ctx, []MirrorableChart{GetTygerChart()}); err != nil {
		return err
	}

	return nil
}

// getOrgExtraImages collects additional per-org images (e.g. miseImage) that need mirroring.
func (inst *Installer) getOrgExtraImages() []MirrorableImage {
	seen := make(map[string]bool)
	var images []MirrorableImage
	for _, org := range inst.Config.Organizations {
		if org.Api.AccessControl.MiseImage != "" {
			ref := org.Api.AccessControl.MiseImage
			if seen[ref] {
				continue
			}
			seen[ref] = true
			img, err := ParseImageRef(ref)
			if err != nil {
				log.Warn().Err(err).Msgf("Skipping invalid miseImage reference: %s", ref)
				continue
			}
			images = append(images, img)
		}
	}
	return images
}

// mirrorImages imports all required container images into the mirror ACR using the ARM ImportImage API.
func (inst *Installer) mirrorImages(ctx context.Context, images []MirrorableImage) error {
	mirrorAcrName := inst.Config.Cloud.GetMirrorAcrName()

	log.Ctx(ctx).Info().Msgf("Mirroring %d container images to ACR '%s'", len(images), mirrorAcrName)

	registryClient, err := armcontainerregistry.NewRegistriesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create container registry client: %w", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(images))

	for _, img := range images {
		wg.Add(1)
		go func(img MirrorableImage) {
			defer wg.Done()
			if err := importImage(ctx, registryClient, inst.Config.Cloud.ResourceGroup, mirrorAcrName, inst.Config.Cloud.SubscriptionID, inst.Credential, img); err != nil {
				errCh <- fmt.Errorf("failed to import image %s: %w", img.SourceRef(), err)
			}
		}(img)
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("image mirroring failed: %v", errs)
	}

	log.Ctx(ctx).Info().Msgf("Successfully mirrored %d container images", len(images))
	return nil
}

// mirrorCharts imports OCI-based helm charts and pulls+pushes traditional charts to the mirror ACR.
func (inst *Installer) mirrorCharts(ctx context.Context, charts []MirrorableChart) error {
	mirrorAcrFqdn := inst.Config.Cloud.GetMirrorAcrFqdn()
	mirrorAcrName := inst.Config.Cloud.GetMirrorAcrName()

	log.Ctx(ctx).Info().Msgf("Mirroring %d helm charts to ACR '%s'", len(charts), mirrorAcrName)

	registryClient, err := armcontainerregistry.NewRegistriesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create container registry client: %w", err)
	}

	var wg sync.WaitGroup
	errCh := make(chan error, len(charts))

	for _, chart := range charts {
		if chart.IsOCI {
			wg.Add(1)
			go func(chart MirrorableChart) {
				defer wg.Done()
				if err := importOCIChart(ctx, registryClient, inst.Config.Cloud.ResourceGroup, mirrorAcrName, inst.Config.Cloud.SubscriptionID, inst.Credential, chart); err != nil {
					errCh <- fmt.Errorf("failed to import OCI chart %s: %w", chart.SourceChartRef, err)
				}
			}(chart)
		} else {
			wg.Add(1)
			go func(chart MirrorableChart) {
				defer wg.Done()
				if err := inst.mirrorTraditionalChart(ctx, chart, mirrorAcrFqdn); err != nil {
					errCh <- fmt.Errorf("failed to mirror chart %s: %w", chart.SourceChartRef, err)
				}
			}(chart)
		}
	}

	wg.Wait()
	close(errCh)

	var errs []error
	for err := range errCh {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return fmt.Errorf("chart mirroring failed: %v", errs)
	}

	log.Ctx(ctx).Info().Msgf("Successfully mirrored %d helm charts", len(charts))
	return nil
}

func importImage(ctx context.Context, client *armcontainerregistry.RegistriesClient, resourceGroup, registryName, subscriptionID string, credential azcore.TokenCredential, img MirrorableImage) error {
	log.Ctx(ctx).Info().Msgf("Importing image %s", img.SourceRef())

	// For digest-based refs use repo@sha256:..., otherwise repo:tag
	var sourceImage, targetTag string
	if strings.HasPrefix(img.Tag, "sha256:") {
		sourceImage = fmt.Sprintf("%s@%s", img.SourceRepo, img.Tag)
		targetTag = fmt.Sprintf("%s@%s", img.TargetRepo(), img.Tag)
	} else {
		sourceImage = fmt.Sprintf("%s:%s", img.SourceRepo, img.Tag)
		targetTag = fmt.Sprintf("%s:%s", img.TargetRepo(), img.Tag)
	}

	source, err := makeImportSource(ctx, img.SourceRegistry, sourceImage, subscriptionID, credential)
	if err != nil {
		return err
	}

	poller, err := client.BeginImportImage(ctx, resourceGroup, registryName, armcontainerregistry.ImportImageParameters{
		Source: source,
		TargetTags: []*string{
			Ptr(targetTag),
		},
		Mode: Ptr(armcontainerregistry.ImportModeForce),
	}, nil)
	if err != nil {
		return err
	}

	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

func importOCIChart(ctx context.Context, client *armcontainerregistry.RegistriesClient, resourceGroup, registryName, subscriptionID string, credential azcore.TokenCredential, chart MirrorableChart) error {
	sourceRef := strings.TrimPrefix(chart.SourceChartRef, "oci://")
	parts := strings.SplitN(sourceRef, "/", 2)
	registryHost := parts[0]
	repoPath := parts[1] // e.g. "azurelinux/helm/cert-manager"

	repoParts := strings.Split(repoPath, "/")
	chartName := repoParts[len(repoParts)-1]
	targetRepo := fmt.Sprintf("%s/helm/%s", MirrorRepoPrefix, chartName)

	log.Ctx(ctx).Info().Msgf("Importing OCI chart %s:%s", sourceRef, chart.Version)

	source, err := makeImportSource(ctx, registryHost, fmt.Sprintf("%s:%s", repoPath, chart.Version), subscriptionID, credential)
	if err != nil {
		return err
	}

	poller, err := client.BeginImportImage(ctx, resourceGroup, registryName, armcontainerregistry.ImportImageParameters{
		Source: source,
		TargetTags: []*string{
			Ptr(fmt.Sprintf("%s:%s", targetRepo, chart.Version)),
		},
		Mode: Ptr(armcontainerregistry.ImportModeForce),
	}, nil)
	if err != nil {
		return err
	}

	_, err = poller.PollUntilDone(ctx, nil)
	return err
}

// makeImportSource builds an ImportSource, using ResourceID for private ACRs
// (*.azurecr.io) and RegistryURI for public registries (e.g. mcr.microsoft.com).
func makeImportSource(ctx context.Context, registryHost, sourceImage, subscriptionID string, credential azcore.TokenCredential) (*armcontainerregistry.ImportSource, error) {
	if strings.HasSuffix(registryHost, ".azurecr.io") {
		acrName := strings.TrimSuffix(registryHost, ".azurecr.io")
		resourceID, err := getContainerRegistryId(ctx, acrName, subscriptionID, credential)
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

func (inst *Installer) mirrorTraditionalChart(ctx context.Context, chart MirrorableChart, mirrorAcrFqdn string) error {
	log.Ctx(ctx).Info().Msgf("Mirroring traditional chart %s:%s", chart.SourceChartRef, chart.Version)

	registryClient, err := inst.newRegistryClient(ctx, mirrorAcrFqdn)
	if err != nil {
		return fmt.Errorf("failed to create registry client: %w", err)
	}

	// Pull the chart from the source repo using action.Pull
	pull := action.NewPullWithOpts(action.WithConfig(&action.Configuration{}))
	pull.Settings = helmEnvSettings()
	pull.Version = chart.Version
	pull.RepoURL = chart.SourceRepoUrl
	pull.SetRegistryClient(registryClient)

	tmpDir, err := os.MkdirTemp("", "tyger-mirror-*")
	if err != nil {
		return fmt.Errorf("failed to create temp dir: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	pull.DestDir = tmpDir

	// chart.SourceChartRef is like "traefik/traefik" — we need just the chart name
	parts := strings.Split(chart.SourceChartRef, "/")
	chartName := parts[len(parts)-1]

	if _, err := pull.Run(chartName); err != nil {
		return fmt.Errorf("failed to pull chart: %w", err)
	}

	// Find the downloaded .tgz file
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
		return fmt.Errorf("chart package not found in temp dir")
	}

	// Push to the mirror ACR as OCI
	targetRef := fmt.Sprintf("%s/%s/helm/%s:%s", mirrorAcrFqdn, MirrorRepoPrefix, chartName, chart.Version)
	if _, err := registryClient.Push(chartData, targetRef); err != nil {
		return fmt.Errorf("failed to push chart: %w", err)
	}

	return nil
}

// newRegistryClient creates a helm registry client logged in to the mirror ACR.
func (inst *Installer) newRegistryClient(ctx context.Context, mirrorAcrFqdn string) (*registry.Client, error) {
	refreshToken, err := inst.getAcrRefreshToken(ctx, mirrorAcrFqdn)
	if err != nil {
		return nil, fmt.Errorf("failed to get ACR refresh token: %w", err)
	}

	registryClient, err := registry.NewClient()
	if err != nil {
		return nil, fmt.Errorf("failed to create registry client: %w", err)
	}

	if err := registryClient.Login(mirrorAcrFqdn,
		registry.LoginOptBasicAuth("00000000-0000-0000-0000-000000000000", refreshToken),
	); err != nil {
		return nil, fmt.Errorf("failed to log in to registry: %w", err)
	}

	return registryClient, nil
}

// getAcrRefreshToken exchanges an AAD access token for an ACR refresh token
// via the /oauth2/exchange endpoint.
func (inst *Installer) getAcrRefreshToken(ctx context.Context, acrFqdn string) (string, error) {
	aadToken, err := inst.Credential.GetToken(ctx, policy.TokenRequestOptions{
		Scopes: []string{"https://management.azure.com/.default"},
	})
	if err != nil {
		return "", fmt.Errorf("failed to get AAD token: %w", err)
	}

	exchangeURL := fmt.Sprintf("https://%s/oauth2/exchange", acrFqdn)
	formData := fmt.Sprintf("grant_type=access_token&service=%s&access_token=%s", acrFqdn, aadToken.Token)

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, exchangeURL, strings.NewReader(formData))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")

	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return "", fmt.Errorf("failed to exchange token: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("ACR token exchange failed (status %d): %s", resp.StatusCode, string(body))
	}

	var result struct {
		RefreshToken string `json:"refresh_token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return "", fmt.Errorf("failed to decode exchange response: %w", err)
	}

	return result.RefreshToken, nil
}

// helmEnvSettings returns minimal Helm environment settings.
func helmEnvSettings() *cli.EnvSettings {
	return cli.New()
}

func (inst *Installer) resolveMirrorAcrLoginServer(ctx context.Context, acrName string) error {
	resourceID, err := getContainerRegistryId(ctx, acrName, inst.Config.Cloud.SubscriptionID, inst.Credential)
	if err != nil {
		return fmt.Errorf("failed to find mirror ACR '%s': %w", acrName, err)
	}

	// Parse the resource group from the resource ID
	// Format: /subscriptions/{sub}/resourceGroups/{rg}/providers/...
	parts := strings.Split(resourceID, "/")
	var resourceGroup string
	for i, p := range parts {
		if strings.EqualFold(p, "resourceGroups") && i+1 < len(parts) {
			resourceGroup = parts[i+1]
			break
		}
	}
	if resourceGroup == "" {
		return fmt.Errorf("failed to parse resource group from ACR resource ID: %s", resourceID)
	}

	registryClient, err := armcontainerregistry.NewRegistriesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return err
	}

	resp, err := registryClient.Get(ctx, resourceGroup, acrName, nil)
	if err != nil {
		return fmt.Errorf("failed to get mirror ACR '%s': %w", acrName, err)
	}

	if resp.Properties != nil && resp.Properties.LoginServer != nil {
		inst.Config.Cloud.SetMirrorAcrLoginServer(*resp.Properties.LoginServer)
	}
	return nil
}

// --- Mirror override functions for each chart ---

// ApplyTraefikMirrorOverrides updates Traefik helm config to reference the mirror ACR.
func ApplyTraefikMirrorOverrides(mirrorAcrFqdn string, helmConfig *HelmChartConfig) {
	if mirrorAcrFqdn == "" {
		return
	}

	chart := GetTraefikChart()
	helmConfig.ChartRef = chart.TargetChartRef(mirrorAcrFqdn)
	helmConfig.RepoName = ""
	helmConfig.RepoUrl = ""

	images := GetTraefikImages()
	traefik := images[0]
	if imageMap, ok := helmConfig.Values["image"].(map[string]any); ok {
		imageMap["registry"] = mirrorAcrFqdn
		imageMap["repository"] = traefik.TargetRepo()
		imageMap["tag"] = traefik.Tag
	}
}

// ApplyTraefikSidecarMirrorOverride updates the config-reloader sidecar image to use the mirror ACR.
func ApplyTraefikSidecarMirrorOverride(mirrorAcrFqdn string, sidecar *corev1.Container) {
	if mirrorAcrFqdn == "" {
		return
	}
	images := GetTraefikImages()
	azureLinux := images[1]
	sidecar.Image = azureLinux.TargetRef(mirrorAcrFqdn)
}

// ApplyCertManagerMirrorOverrides updates cert-manager helm config to reference the mirror ACR.
func ApplyCertManagerMirrorOverrides(mirrorAcrFqdn string, helmConfig *HelmChartConfig) {
	if mirrorAcrFqdn == "" {
		return
	}

	chart := GetCertManagerChart()
	helmConfig.ChartRef = chart.TargetChartRef(mirrorAcrFqdn)

	images := GetCertManagerImages()
	certManagerValues, ok := helmConfig.Values["cert-manager"].(map[string]any)
	if !ok {
		return
	}

	if imageMap, ok := certManagerValues["image"].(map[string]any); ok {
		imageMap["repository"] = fmt.Sprintf("%s/%s", mirrorAcrFqdn, images.Controller.TargetRepo())
		imageMap["tag"] = images.Controller.Tag
	}
	if compMap, ok := certManagerValues["acmesolver"].(map[string]any); ok {
		if imageMap, ok := compMap["image"].(map[string]any); ok {
			imageMap["repository"] = fmt.Sprintf("%s/%s", mirrorAcrFqdn, images.AcmeSolver.TargetRepo())
			imageMap["tag"] = images.AcmeSolver.Tag
		}
	}
	if compMap, ok := certManagerValues["cainjector"].(map[string]any); ok {
		if imageMap, ok := compMap["image"].(map[string]any); ok {
			imageMap["repository"] = fmt.Sprintf("%s/%s", mirrorAcrFqdn, images.CaInjector.TargetRepo())
			imageMap["tag"] = images.CaInjector.Tag
		}
	}
	if compMap, ok := certManagerValues["webhook"].(map[string]any); ok {
		if imageMap, ok := compMap["image"].(map[string]any); ok {
			imageMap["repository"] = fmt.Sprintf("%s/%s", mirrorAcrFqdn, images.Webhook.TargetRepo())
			imageMap["tag"] = images.Webhook.Tag
		}
	}
}

// ApplyNvidiaDevicePluginMirrorOverrides updates NVIDIA device plugin helm config to reference the mirror ACR.
func ApplyNvidiaDevicePluginMirrorOverrides(mirrorAcrFqdn string, helmConfig *HelmChartConfig) {
	if mirrorAcrFqdn == "" {
		return
	}

	chart := GetNvidiaDevicePluginChart()
	helmConfig.ChartRef = chart.TargetChartRef(mirrorAcrFqdn)
	helmConfig.RepoName = ""
	helmConfig.RepoUrl = ""

	images := GetNvidiaDevicePluginImages()
	nvidia := images[0]
	if imageMap, ok := helmConfig.Values["image"].(map[string]any); ok {
		imageMap["repository"] = fmt.Sprintf("%s/%s", mirrorAcrFqdn, nvidia.TargetRepo())
		imageMap["tag"] = nvidia.Tag
	}
}

// ApplyTygerMirrorOverrides updates Tyger helm config to reference the mirror ACR.
// It also rewrites the miseImage if present.
func ApplyTygerMirrorOverrides(mirrorAcrFqdn string, helmConfig *HelmChartConfig) {
	if mirrorAcrFqdn == "" {
		return
	}

	chart := GetTygerChart()
	helmConfig.ChartRef = chart.TargetChartRef(mirrorAcrFqdn)

	images := GetTygerImages()
	for _, img := range images {
		targetRef := img.TargetRef(mirrorAcrFqdn)
		parts := strings.Split(img.SourceRepo, "/")
		baseName := parts[len(parts)-1]

		switch baseName {
		case "tyger-server":
			helmConfig.Values["image"] = targetRef
		case "buffer-sidecar":
			helmConfig.Values["bufferSidecarImage"] = targetRef
		case "buffer-copier":
			helmConfig.Values["bufferCopierImage"] = targetRef
		case "worker-waiter":
			helmConfig.Values["workerWaiterImage"] = targetRef
		}
	}

	// Rewrite miseImage if present
	if acMap, ok := helmConfig.Values["accessControl"].(map[string]any); ok {
		if miseMap, ok := acMap["mise"].(map[string]any); ok {
			if origRef, ok := miseMap["image"].(string); ok && origRef != "" {
				if img, err := ParseImageRef(origRef); err == nil {
					miseMap["image"] = img.TargetRef(mirrorAcrFqdn)
				}
			}
		}
	}
}

var imageRefPattern = regexp.MustCompile(`image:\s*"?([^\s"]+)"?`)

// ValidateManifestMirrorImages checks that all container image references in a
// rendered helm manifest point to the mirror ACR. Returns an error listing any
// images that do not.
func ValidateManifestMirrorImages(manifest, mirrorAcrFqdn string) error {
	if mirrorAcrFqdn == "" || manifest == "" {
		return nil
	}

	prefix := mirrorAcrFqdn + "/"
	matches := imageRefPattern.FindAllStringSubmatch(manifest, -1)
	var bad []string
	for _, m := range matches {
		img := m[1]
		if !strings.HasPrefix(img, prefix) {
			bad = append(bad, img)
		}
	}
	if len(bad) > 0 {
		return fmt.Errorf("internal error: the following container images do not reference the mirror ACR %s: %s", mirrorAcrFqdn, strings.Join(bad, ", "))
	}
	return nil
}
