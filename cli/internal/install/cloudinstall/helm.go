// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"time"

	"dario.cat/mergo"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/install"
	helmclient "github.com/mittwald/go-helm-client"
	"github.com/rs/zerolog/log"
	"helm.sh/helm/v3/pkg/release"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage/driver"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const (
	TraefikNamespace = "traefik"
)

func (inst *Installer) installTraefik(ctx context.Context, restConfigPromise *install.Promise[*rest.Config], keyVaultClientManagedIdentityPromise *install.Promise[*armmsi.Identity]) (any, error) {
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	log.Ctx(ctx).Info().Msg("Installing Traefik")

	clientset := kubernetes.NewForConfigOrDie(restConfig)
	if err := inst.ensureTraefikDynamicConfigMap(ctx, clientset); err != nil {
		return nil, fmt.Errorf("failed to ensure Traefik dynamic ConfigMap: %w", err)
	}

	traefikConfig := HelmChartConfig{
		RepoName:    "traefik",
		Namespace:   TraefikNamespace,
		ReleaseName: "traefik",
		RepoUrl:     "https://helm.traefik.io/traefik",
		ChartRef:    "traefik/traefik",
		Version:     "24.0.0",
		Values: map[string]any{
			"image": map[string]any{
				"registry":   "mcr.microsoft.com",
				"repository": "oss/traefik/traefik",
				"tag":        "v2.10.7",
			},
			"logs": map[string]any{
				"general": map[string]any{
					"format": "json",
				},
				"access": map[string]any{
					"enabled": "true",
					"format":  "json",
				},
			},
			"service": map[string]any{
				"annotations": map[string]any{
					"service.beta.kubernetes.io/azure-dns-label-name": inst.Config.Cloud.Compute.DnsLabel,
				},
			},
			"additionalArguments": []string{
				"--entryPoints.websecure.http.tls=true",
			},
		}}

	usingCertificate := keyVaultClientManagedIdentityPromise != nil
	if usingCertificate {
		kvClientIdentity, err := keyVaultClientManagedIdentityPromise.Await()
		if err != nil {
			return nil, install.ErrDependencyFailed
		}

		traefikConfig.Values["serviceAccountAnnotations"] = map[string]any{
			"azure.workload.identity/client-id": *kvClientIdentity.Properties.ClientID,
		}

		traefikConfig.Values["volumes"] = []any{
			map[string]any{
				"name":      "traefik-dynamic",
				"mountPath": "/config",
				"type":      "configMap",
			},
		}

		traefikConfig.Values["deployment"] = map[string]any{
			"additionalVolumes": []any{
				map[string]any{
					"name": "kv-certs",
					"csi": map[string]any{
						"driver":   "secrets-store.csi.k8s.io",
						"readOnly": true,
						"volumeAttributes": map[string]any{
							"secretProviderClass": inst.Config.Cloud.TlsCertificate.CertificateName,
						},
					},
				},
			},
		}

		traefikConfig.Values["additionalVolumeMounts"] = []any{
			map[string]any{
				"name":      "kv-certs",
				"mountPath": "/certs",
				"readOnly":  true,
			},
		}

		traefikConfig.Values["additionalArguments"] = append(
			traefikConfig.Values["additionalArguments"].([]string),
			"--providers.file.filename=/config/dynamic.toml")
	}

	var overrides *HelmChartConfig
	if inst.Config.Cloud.Compute.Helm != nil && inst.Config.Cloud.Compute.Helm.Traefik != nil {
		overrides = inst.Config.Cloud.Compute.Helm.Traefik
	}

	startTime := time.Now().Add(-10 * time.Second)
	if _, _, err := installHelmChart(ctx, restConfig, &traefikConfig, overrides, false); err != nil {
		installErr := err

		// List warning events in the namespace
		clientset := kubernetes.NewForConfigOrDie(restConfig)
		events, err := clientset.CoreV1().Events(traefikConfig.Namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to install Traefik: %w", installErr)
		}

		sort.SliceStable(events.Items, func(i, j int) bool {
			return events.Items[i].LastTimestamp.After(events.Items[j].LastTimestamp.Time)
		})

		for _, event := range events.Items {
			if event.Type == corev1.EventTypeWarning && event.LastTimestamp.After(startTime) {
				log.Ctx(ctx).Warn().Str("Reason", event.Reason).Msg(event.Message)
			}
		}

		return nil, fmt.Errorf("failed to install Traefik: %w", installErr)
	}

	return nil, nil
}

func (inst *Installer) ensureTraefikDynamicConfigMap(ctx context.Context, clientset *kubernetes.Clientset) error {
	configMapName := "traefik-dynamic"
	namespace := TraefikNamespace

	desiredData := map[string]string{
		"dynamic.toml": `
# Dynamic configuration
[[tls.certificates]]
certFile = "/certs/tls.crt"
keyFile = "/certs/tls.key"
`,
	}

	// Check if the ConfigMap already exists
	existingConfigMap, err := clientset.CoreV1().ConfigMaps(namespace).Get(ctx, configMapName, metav1.GetOptions{})
	if err == nil {
		// ConfigMap exists, check if the data matches
		if reflect.DeepEqual(existingConfigMap.Data, desiredData) {
			// No update needed
			return nil
		}

		// Update the ConfigMap if the data is different
		existingConfigMap.Data = desiredData
		_, err = clientset.CoreV1().ConfigMaps(namespace).Update(ctx, existingConfigMap, metav1.UpdateOptions{})
		if err != nil {
			return fmt.Errorf("failed to update ConfigMap: %w", err)
		}
		return nil
	}

	if !apierrors.IsNotFound(err) {
		// Return any error other than "not found"
		return fmt.Errorf("failed to get ConfigMap: %w", err)
	}

	// ConfigMap does not exist, create it
	newConfigMap := &corev1.ConfigMap{
		ObjectMeta: metav1.ObjectMeta{
			Name:      configMapName,
			Namespace: namespace,
		},
		Data: desiredData,
	}

	_, err = clientset.CoreV1().ConfigMaps(namespace).Create(ctx, newConfigMap, metav1.CreateOptions{})
	if err != nil {
		return fmt.Errorf("failed to create ConfigMap: %w", err)
	}

	return nil
}

func (inst *Installer) installCertManager(ctx context.Context, restConfigPromise *install.Promise[*rest.Config]) (any, error) {
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	log.Ctx(ctx).Info().Msg("Installing cert-manager")

	certManagerConfig := HelmChartConfig{
		Namespace:   "cert-manager",
		ReleaseName: "cert-manager",
		ChartRef:    "oci://mcr.microsoft.com/azurelinux/helm/cert-manager",
		Version:     "1.12.12-4",
		Values: map[string]any{
			"cert-manager": map[string]any{
				"installCRDs": true,
			},
		},
	}

	var overrides *HelmChartConfig
	if inst.Config.Cloud.Compute.Helm != nil && inst.Config.Cloud.Compute.Helm.CertManager != nil {
		overrides = inst.Config.Cloud.Compute.Helm.CertManager
	}

	if _, _, err := installHelmChart(ctx, restConfig, &certManagerConfig, overrides, false); err != nil {
		return nil, fmt.Errorf("failed to install cert-manager: %w", err)
	}

	return nil, nil
}

func (inst *Installer) installNvidiaDevicePlugin(ctx context.Context, restConfigPromise *install.Promise[*rest.Config]) (any, error) {
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	log.Ctx(ctx).Info().Msg("Installing nvidia-device-plugin")

	nvdpConfig := HelmChartConfig{
		Namespace:   "nvidia-device-plugin",
		ReleaseName: "nvidia-device-plugin",
		RepoName:    "nvdp",
		RepoUrl:     "https://nvidia.github.io/k8s-device-plugin",
		ChartRef:    "nvdp/nvidia-device-plugin",
		Version:     "0.14.1",
		Values: map[string]any{
			"nodeSelector": map[string]any{
				"kubernetes.azure.com/accelerator": "nvidia",
			},
			"tolerations": []any{
				map[string]any{
					// Allow this pod to be rescheduled while the node is in "critical add-ons only" mode.
					// This, along with the annotation above marks this pod as a critical add-on.
					"key":      "CriticalAddonsOnly",
					"operator": "Exists",
				},
				map[string]any{
					"key":      "nvidia.com/gpu",
					"operator": "Exists",
					"effect":   "NoSchedule",
				},
				map[string]any{
					"key":      "sku",
					"operator": "Equal",
					"value":    "gpu",
					"effect":   "NoSchedule",
				},
				map[string]any{
					"key":      "tyger",
					"operator": "Equal",
					"value":    "run",
					"effect":   "NoSchedule",
				},
			},
		},
	}

	var overrides *HelmChartConfig
	if inst.Config.Cloud.Compute.Helm != nil && inst.Config.Cloud.Compute.Helm.NvidiaDevicePlugin != nil {
		overrides = inst.Config.Cloud.Compute.Helm.NvidiaDevicePlugin
	}

	if _, _, err := installHelmChart(ctx, restConfig, &nvdpConfig, overrides, false); err != nil {
		return nil, fmt.Errorf("failed to install NVIDIA device plugin: %w", err)
	}

	return nil, nil
}

func (inst *Installer) InstallTyger(ctx context.Context) error {
	restConfig, err := inst.GetUserRESTConfig(ctx)
	if err != nil {
		return err
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	return inst.Config.ForEachOrgInParallel(ctx, func(ctx context.Context, org *OrganizationConfig) error {
		revision, err := inst.GetTygerInstallationRevision(ctx, org)
		if err != nil {
			return fmt.Errorf("failed to get Tyger installation revision: %w", err)
		}

		logsCtx, cancel := context.WithCancel(ctx)
		logsMapChan := make(chan map[string][]byte, 1)
		go func() {
			results, err := followPodsLogsUntilContextCanceled(logsCtx, clientset, org.Cloud.KubernetesNamespace, fmt.Sprintf("tyger-helm-revision=%d", revision+1))
			if err != nil {
				log.Ctx(ctx).Error().Err(err).Msg("Failed to follow Tyger server logs")
			}
			logsMapChan <- results
		}()
		_, _, err = inst.InstallTygerHelmChart(ctx, org, false)
		cancel()
		logsMap := <-logsMapChan
		if err != nil {
			for _, logs := range logsMap {
				parsedLogLines, err := install.ParseJsonLogs(logs)
				if err != nil {
					continue
				}

				for _, parsedLogLine := range parsedLogLines {
					if category, ok := parsedLogLine["category"].(string); ok {
						if category == "Tyger.ControlPlane.Database.Migrations.DatabaseVersions[DatabaseMigrationRequired]" {
							if message, ok := parsedLogLine["message"].(string); ok {
								log.Ctx(ctx).Error().Msg(message)
							}
							if args, ok := parsedLogLine["args"].(map[string]any); ok {
								if version, ok := args["requiredVersion"].(float64); ok {
									log.Ctx(ctx).Error().Msgf("Run `tyger api uninstall -f <CONFIG_PATH>` followed by `tyger api migrations apply --target-version %d --offline --wait -f <CONFIG_PATH>` followed by `tyger api install -f <CONFIG_PATH>` ", int(version))
									return install.ErrAlreadyLoggedError
								}
							}
						}
					}
				}
			}

			log.Ctx(ctx).Error().Err(err).Msg("Failed to install Tyger Helm chart")

			for podName, logs := range logsMap {
				if len(logs) > 0 {
					log.Ctx(ctx).Info().Str("pod", podName).Msg("Pod logs:")
					fmt.Fprintln(os.Stderr, string(logs))
				}
			}

			return install.ErrAlreadyLoggedError
		}

		baseEndpoint := fmt.Sprintf("https://%s", org.Api.DomainName)
		healthCheckEndpoint := fmt.Sprintf("%s/healthcheck", baseEndpoint)

		client := client.DefaultClient
		client.RetryMax = 0 // we do own own retrying here

		for i := 0; ; i++ {
			req, err := retryablehttp.NewRequestWithContext(ctx, http.MethodGet, healthCheckEndpoint, nil)
			if err != nil {
				return fmt.Errorf("failed to create health check request: %w", err)
			}

			resp, err := client.Do(req)
			errorLogger := log.Debug()
			exit := false
			if i == 60 {
				exit = true
				errorLogger = log.Ctx(ctx).Error()
			}
			if err != nil {
				if errors.Is(err, ctx.Err()) {
					return err
				}
				errorLogger.Err(err).Msg("Tyger health check failed")
			} else if resp.StatusCode != http.StatusOK {
				errorLogger.Msgf("Tyger health check failed with status code %d", resp.StatusCode)
			} else {
				log.Ctx(ctx).Info().Msgf("Tyger API up at %s", baseEndpoint)
				break
			}

			if exit {
				return install.ErrAlreadyLoggedError
			}

			time.Sleep(time.Second)
		}

		found := false
		for _, logs := range logsMap {
			if found {
				break
			}
			parsedLines, err := install.ParseJsonLogs(logs)
			if err != nil {
				continue
			}

			for _, parsedLine := range parsedLines {
				if category, ok := parsedLine["category"].(string); ok {
					if category == "Tyger.ControlPlane.Database.Migrations.MigrationRunner[NewerDatabaseVersionsExist]" {
						log.Ctx(ctx).Warn().Msgf("The database schema should be upgraded. Run `tyger api migrations list` to see the available migrations and `tyger api migrations apply` to apply them.")
						found = true
						break
					}

					if category == "Tyger.ControlPlane.Database.Migrations.MigrationRunner[UsingMostRecentDatabaseVersion]" {
						log.Debug().Msg("Database schema is up to date")
						found = true
						break
					}
				}
			}
		}

		if !found {
			return errors.New("failed to find expected migration log message")
		}

		return nil
	})
}

// 0 means not installed.
func (inst *Installer) GetTygerInstallationRevision(ctx context.Context, org *OrganizationConfig) (int, error) {
	restConfig, err := inst.GetUserRESTConfig(ctx)
	if err != nil {
		return 0, err
	}

	helmOptions := helmclient.RestConfClientOptions{
		RestConfig: restConfig,
		Options: &helmclient.Options{
			DebugLog: func(format string, v ...interface{}) {
				log.Debug().Msgf(format, v...)
			},
			Namespace: org.Cloud.KubernetesNamespace,
		},
	}

	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return 0, fmt.Errorf("failed to create helm client: %w", err)
	}

	r, err := helmClient.GetRelease(DefaultTygerReleaseName)
	if err != nil {
		if err == driver.ErrReleaseNotFound {
			return 0, nil
		}
		return 0, err
	}

	return r.Version, nil
}

func (inst *Installer) InstallTygerHelmChart(ctx context.Context, org *OrganizationConfig, dryRun bool) (manifest string, valuesYaml string, err error) {
	restConfig, err := inst.GetUserRESTConfig(ctx)
	if err != nil {
		return "", "", err
	}

	clustersConfigJson, err := json.Marshal(inst.Config.Cloud.Compute.Clusters)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal cluster configuration: %w", err)
	}
	clustersConfig := make([]map[string]any, 0)
	if err := json.Unmarshal(clustersConfigJson, &clustersConfig); err != nil {
		return "", "", fmt.Errorf("failed to unmarshal cluster configuration: %w", err)
	}

	identitiesClient, err := armmsi.NewUserAssignedIdentitiesClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create managed identities client: %w", err)
	}

	tygerServerIdentity, err := identitiesClient.Get(ctx, org.Cloud.ResourceGroup, tygerServerManagedIdentityName, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to get managed identity: %w", err)
	}

	migrationRunnerIdentity, err := identitiesClient.Get(ctx, org.Cloud.ResourceGroup, migrationRunnerManagedIdentityName, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to get managed identity: %w", err)
	}

	customIdentitiesValues := make([]map[string]any, 0)
	for _, identity := range org.Cloud.Identities {
		identity, err := identitiesClient.Get(ctx, org.Cloud.ResourceGroup, identity, nil)
		if err != nil {
			return "", "", fmt.Errorf("failed to get managed identity: %w", err)
		}

		customIdentitiesValues = append(
			customIdentitiesValues,
			map[string]any{
				"name":     identity.Name,
				"clientId": identity.Properties.ClientID,
			})
	}

	storageClient, err := armstorage.NewAccountsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create storage client: %w", err)
	}

	buffersStorageAccountValues := make([]map[string]any, 0)
	for _, accountConfig := range org.Cloud.Storage.Buffers {
		acc, err := storageClient.GetProperties(ctx, org.Cloud.ResourceGroup, accountConfig.Name, nil)
		if err != nil {
			return "", "", fmt.Errorf("failed to get buffer storage account properties: %w", err)
		}

		accountValues := map[string]any{
			"name":     accountConfig.Name,
			"location": *acc.Properties.PrimaryLocation,
			"endpoint": *acc.Properties.PrimaryEndpoints.Blob,
		}

		buffersStorageAccountValues = append(buffersStorageAccountValues, accountValues)
	}

	logArchiveAccount, err := storageClient.GetProperties(ctx, org.Cloud.ResourceGroup, org.Cloud.Storage.Logs.Name, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to get logs storage account properties: %w", err)
	}

	dbServersClient, err := armpostgresqlflexibleservers.NewServersClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create PostgreSQL server client: %w", err)
	}

	dbServerName := inst.Config.Cloud.Database.ServerName

	dbServer, err := dbServersClient.Get(ctx, inst.Config.Cloud.ResourceGroup, dbServerName, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to get PostgreSQL server: %w", err)
	}

	helmConfig := HelmChartConfig{
		Namespace:   org.Cloud.KubernetesNamespace,
		ReleaseName: DefaultTygerReleaseName,
		ChartRef:    fmt.Sprintf("oci://%s%shelm/tyger", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory()),
		Version:     install.ContainerImageTag,
		Values: map[string]any{
			"image":              fmt.Sprintf("%s%styger-server:%s", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory(), install.ContainerImageTag),
			"bufferSidecarImage": fmt.Sprintf("%s%sbuffer-sidecar:%s", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory(), install.ContainerImageTag),
			"bufferCopierImage":  fmt.Sprintf("%s%sbuffer-copier:%s", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory(), install.ContainerImageTag),
			"workerWaiterImage":  fmt.Sprintf("%s%sworker-waiter:%s", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory(), install.ContainerImageTag),
			"hostname":           org.Api.DomainName,
			"location":           inst.Config.Cloud.DefaultLocation,
			"identity": map[string]any{
				"tygerServer": map[string]any{
					"name":             *tygerServerIdentity.Name,
					"databaseRoleName": getDatabaseRoleName(org, *tygerServerIdentity.Name),
					"clientId":         *tygerServerIdentity.Properties.ClientID,
				},
				"migrationRunner": map[string]any{
					"name":             *migrationRunnerIdentity.Name,
					"databaseRoleName": getDatabaseRoleName(org, *migrationRunnerIdentity.Name),
					"clientId":         *migrationRunnerIdentity.Properties.ClientID,
				},
				"custom": customIdentitiesValues,
			},
			"security": map[string]any{
				"enabled":   true,
				"authority": cloud.AzurePublic.ActiveDirectoryAuthorityHost + org.Api.Auth.TenantID,
				"audience":  org.Api.Auth.ApiAppUri,
				"apiAppId":  org.Api.Auth.ApiAppId,
				"apiAppUri": org.Api.Auth.ApiAppUri,
				"cliAppUri": org.Api.Auth.CliAppUri,
				"cliAppId":  org.Api.Auth.CliAppId,
			},
			"tls": map[string]any{
				"letsEncrypt": map[string]any{
					"enabled": org.Api.TlsCertificateProvider == TlsCertificateProviderLetsEncrypt,
				},
			},
			"database": map[string]any{
				"host":           *dbServer.Properties.FullyQualifiedDomainName,
				"databaseName":   org.Cloud.DatabaseName,
				"port":           fmt.Sprintf("%d", databasePort),
				"ownersRoleName": getDatabaseRoleName(org, unqualifiedOwnersRole),
			},
			"buffers": map[string]any{
				"storageAccounts":     buffersStorageAccountValues,
				"activeLifetime":      org.Api.Buffers.ActiveLifetime,
				"softDeletedLifetime": org.Api.Buffers.SoftDeletedLifetime,
			},
			"logArchive": map[string]any{
				"storageAccountEndpoint": *logArchiveAccount.Properties.PrimaryEndpoints.Blob,
			},
			"clusterConfiguration": clustersConfig,
		},
	}

	var overrides *HelmChartConfig
	if org.Api.Helm != nil && org.Api.Helm.Tyger != nil {
		overrides = org.Api.Helm.Tyger
	}

	adjustSpec := func(cs *helmclient.ChartSpec, c helmclient.Client) error {
		cs.CreateNamespace = false
		return nil
	}

	if manifest, valuesYaml, err = installHelmChart(ctx, restConfig, &helmConfig, overrides, dryRun, adjustSpec); err != nil {
		return "", "", fmt.Errorf("failed to install Tyger Helm chart: %w", err)
	}

	return manifest, valuesYaml, nil
}

func (inst *Installer) getMigrationRunnerJobDefinition(ctx context.Context, org *OrganizationConfig) (*batchv1.Job, error) {
	dryRun := true
	manifest, _, err := inst.InstallTygerHelmChart(ctx, org, dryRun)
	if err != nil {
		return nil, err
	}

	return getMigrationRunnerJobDefinitionFromManifest(manifest)
}

func getMigrationRunnerJobDefinitionFromManifest(manifest string) (*batchv1.Job, error) {
	scheme := runtime.NewScheme()
	if err := batchv1.AddToScheme(scheme); err != nil {
		return nil, fmt.Errorf("failed to add batchv1 to scheme: %w", err)
	}

	factory := serializer.NewCodecFactory(scheme)
	decoder := factory.UniversalDeserializer()
	for _, input := range strings.Split(manifest, "---") {
		obj, _, err := decoder.Decode([]byte(input), nil, nil)
		if err != nil {
			if runtime.IsNotRegisteredError(err) || runtime.IsMissingKind(err) {
				continue
			}
			return nil, fmt.Errorf("failed to decode helm manifest: %w", err)
		}

		if job, ok := obj.(*batchv1.Job); ok {
			return job, nil
		}
	}

	return nil, errors.New("failed to find migration worker job in Tyger Helm release")
}

func installHelmChart(
	ctx context.Context,
	restConfig *rest.Config,
	helmChartConfig,
	overrideHelmChartConfig *HelmChartConfig,
	dryRun bool,
	customizeSpec ...func(*helmclient.ChartSpec, helmclient.Client) error,
) (manifest string, valuesYaml string, err error) {
	helmOptions := helmclient.RestConfClientOptions{
		RestConfig: restConfig,
		Options: &helmclient.Options{
			DebugLog: func(format string, v ...interface{}) {
				log.Debug().Msgf(format, v...)
			},
			Namespace: helmChartConfig.Namespace,
		},
	}

	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return "", "", fmt.Errorf("failed to create helm client: %w", err)
	}

	chartSpec, err := GetChartSpec(helmChartConfig, helmClient, overrideHelmChartConfig, customizeSpec...)
	if err != nil {
		return "", "", err
	}

	if dryRun {
		manifest, err := helmClient.TemplateChart(&chartSpec, nil)
		if err != nil {
			return "", "", fmt.Errorf("failed to template chart: %w", err)
		}
		return string(manifest), chartSpec.ValuesYaml, nil
	}

	for i := 0; ; i++ {
		var release *release.Release
		release, err = helmClient.InstallOrUpgradeChart(ctx, &chartSpec, nil)
		if err == nil || i == 30 {
			if err == nil && i > 0 {
				log.Ctx(ctx).Info().Msg("Transient Helm error resolved")
			}
			if release != nil {
				manifest = release.Manifest
			}
			break
		}

		if strings.Contains(err.Error(), "the server could not find the requested resource") {
			// we get this transient error from time to time, related to the installation of CRDs
			log.Ctx(ctx).Info().Err(err).Msg("Possible transient Helm error. Will retry...")
			time.Sleep(10 * time.Second)
		} else {
			return "", "", err
		}
	}
	return manifest, chartSpec.ValuesYaml, err
}

func GetChartSpec(
	helmChartConfig *HelmChartConfig,
	helmClient helmclient.Client,
	overrideHelmChartConfig *HelmChartConfig,
	customizeSpec ...func(*helmclient.ChartSpec, helmclient.Client) error,
) (helmclient.ChartSpec, error) {
	if helmChartConfig.RepoUrl != "" {
		err := helmClient.AddOrUpdateChartRepo(repo.Entry{Name: helmChartConfig.RepoName, URL: helmChartConfig.RepoUrl})
		if err != nil {
			return helmclient.ChartSpec{}, fmt.Errorf("failed to add helm repo: %w", err)
		}
	}

	if overrideHelmChartConfig != nil {
		if err := mergo.Merge(helmChartConfig, overrideHelmChartConfig, mergo.WithOverride); err != nil {
			return helmclient.ChartSpec{}, fmt.Errorf("failed to merge helm config: %w", err)
		}
	}

	values, err := yaml.Marshal(helmChartConfig.Values)
	if err != nil {
		return helmclient.ChartSpec{}, fmt.Errorf("failed to marshal helm values: %w", err)
	}

	_, err = helmClient.GetRelease(helmChartConfig.ReleaseName)
	atomic := true
	if err != nil {
		if err == driver.ErrReleaseNotFound {
			atomic = false
		} else {
			return helmclient.ChartSpec{}, err
		}
	}

	chartSpec := helmclient.ChartSpec{
		Namespace:       helmChartConfig.Namespace,
		ReleaseName:     helmChartConfig.ReleaseName,
		ChartName:       helmChartConfig.ChartRef,
		Version:         helmChartConfig.Version,
		CreateNamespace: true,
		Wait:            true,
		WaitForJobs:     true,
		Atomic:          atomic,
		Force:           false,
		UpgradeCRDs:     true,
		Timeout:         2 * time.Minute,
		ValuesYaml:      string(values),
	}

	for _, f := range customizeSpec {
		if err := f(&chartSpec, helmClient); err != nil {
			return helmclient.ChartSpec{}, err
		}
	}
	return chartSpec, nil
}

func (inst *Installer) UninstallTyger(ctx context.Context, _, _ bool) error {
	restConfig, err := inst.GetUserRESTConfig(ctx)
	if err != nil {
		return err
	}

	return inst.Config.ForEachOrgInParallel(ctx, func(ctx context.Context, org *OrganizationConfig) error {
		// 1. Remove the Helm chart

		helmOptions := helmclient.RestConfClientOptions{
			RestConfig: restConfig,
			Options: &helmclient.Options{
				DebugLog: func(format string, v ...interface{}) {
					log.Debug().Msgf(format, v...)
				},
				Namespace: org.Cloud.KubernetesNamespace,
			},
		}

		helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
		if err != nil {
			return fmt.Errorf("failed to create helm client: %w", err)
		}

		if err := helmClient.UninstallReleaseByName(DefaultTygerReleaseName); err != nil {
			if !errors.Is(err, driver.ErrReleaseNotFound) {
				return fmt.Errorf("failed to uninstall Tyger Helm chart: %w", err)
			}
		}

		clientset, err := kubernetes.NewForConfig(restConfig)
		if err != nil {
			return fmt.Errorf("failed to create Kubernetes client: %w", err)
		}

		// 2. Remove the finalizer we put on run pods so that they can be deleted

		tygerRunLabelSelector := "tyger-run"

		pods, err := clientset.CoreV1().Pods(org.Cloud.KubernetesNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: tygerRunLabelSelector,
		})
		if err != nil {
			return fmt.Errorf("failed to list pods: %w", err)
		}
		patchData := []byte(`[ { "op": "remove", "path": "/metadata/finalizers" } ]`)
		for _, pod := range pods.Items {
			_, err := clientset.CoreV1().Pods(org.Cloud.KubernetesNamespace).Patch(ctx, pod.Name, types.JSONPatchType, patchData, metav1.PatchOptions{})
			if err != nil {
				fmt.Printf("Failed to patch pod %s: %v\n", pod.Name, err)
			}
		}

		// 3. Delete all the resources created by Tyger

		deleteOpts := metav1.DeleteOptions{
			PropagationPolicy: func() *metav1.DeletionPropagation {
				v := metav1.DeletePropagationForeground
				return &v
			}(),
		}

		// Run pods
		err = clientset.CoreV1().Pods(org.Cloud.KubernetesNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
			LabelSelector: tygerRunLabelSelector,
		})
		if err != nil {
			return fmt.Errorf("failed to delete pods: %w", err)
		}

		// Run jobs
		err = clientset.BatchV1().Jobs(org.Cloud.KubernetesNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
			LabelSelector: tygerRunLabelSelector,
		})
		if err != nil {
			return fmt.Errorf("failed to delete jobs: %w", err)
		}
		// Run statefulsets
		err = clientset.AppsV1().StatefulSets(org.Cloud.KubernetesNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
			LabelSelector: tygerRunLabelSelector,
		})
		if err != nil {
			return fmt.Errorf("failed to delete statefulsets: %w", err)
		}
		// Run secrets
		err = clientset.CoreV1().Secrets(org.Cloud.KubernetesNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
			LabelSelector: tygerRunLabelSelector,
		})
		if err != nil {
			return fmt.Errorf("failed to delete secrets: %w", err)
		}

		// Run services. For some reason, there is no DeleteCollection method for services.
		services, err := clientset.CoreV1().Services(org.Cloud.KubernetesNamespace).List(ctx, metav1.ListOptions{
			LabelSelector: tygerRunLabelSelector,
		})
		if err != nil {
			return fmt.Errorf("failed to list services: %w", err)
		}
		for _, s := range services.Items {
			err = clientset.CoreV1().Services(org.Cloud.KubernetesNamespace).Delete(ctx, s.Name, deleteOpts)
			if err != nil {
				return fmt.Errorf("failed to delete service '%s': %w", s.Name, err)
			}
		}

		// Migration jobs
		err = clientset.BatchV1().Jobs(org.Cloud.KubernetesNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
			LabelSelector: migrationRunnerLabelKey,
		})
		if err != nil {
			return fmt.Errorf("failed to delete jobs: %w", err)
		}

		// Command host pods
		err = clientset.CoreV1().Pods(org.Cloud.KubernetesNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
			LabelSelector: commandHostLabelKey,
		})
		if err != nil {
			return fmt.Errorf("failed to delete pods: %w", err)
		}

		return nil
	})
}
