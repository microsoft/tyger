// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package cloudinstall

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"os"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"

	"dario.cat/mergo"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v5"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	goccyyaml "github.com/goccy/go-yaml"
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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/serializer"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const (
	TraefikNamespace              = "traefik"
	TraefikPrivateLinkServiceName = "traefik"

	AzureLinuxImage = "mcr.microsoft.com/azurelinux/base/core:3.0"
)

type MirrorableRegistry string
type MirrorableQualifiedRepository string
type MirrorableRepository string
type MirrorableImageReference string
type MirrorableTag string

func (inst *Installer) installTraefik(ctx context.Context, restConfigPromise *install.Promise[*rest.Config], keyVaultClientManagedIdentityPromise *install.Promise[*armmsi.Identity]) (any, error) {
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	log.Ctx(ctx).Info().Msg("Installing Traefik")

	var serviceAnnotations map[string]any
	if inst.Config.Cloud.PrivateNetworking {
		serviceAnnotations = map[string]any{
			"service.beta.kubernetes.io/azure-load-balancer-internal": "true",
			"service.beta.kubernetes.io/azure-pls-create":             "true",
			"service.beta.kubernetes.io/azure-pls-name":               TraefikPrivateLinkServiceName,
		}
	} else {
		serviceAnnotations = map[string]any{
			"service.beta.kubernetes.io/azure-dns-label-name": inst.Config.Cloud.Compute.DnsLabel,
		}
	}

	if ipServiceTags := inst.Config.Cloud.Compute.GetApiHostCluster().InboundIpServiceTags; len(ipServiceTags) > 0 {
		pairs := []string{}
		for _, t := range ipServiceTags {
			pairs = append(pairs, fmt.Sprintf("%s=%s", t.Type, t.Tag))
		}

		serviceAnnotations["service.beta.kubernetes.io/azure-pip-ip-tags"] = strings.Join(pairs, ",")
	}

	traefikConfig := HelmChartConfig{
		Namespace:   TraefikNamespace,
		ReleaseName: "traefik",
		RepoName:    "traefik",
		RepoUrl:     "https://helm.traefik.io/traefik",
		ChartRef:    "traefik/traefik",
		Version:     "24.0.0",
		Values: map[string]any{
			"image": map[string]any{
				"registry":   MirrorableRegistry("mcr.microsoft.com"),
				"repository": MirrorableRepository("oss/traefik/traefik"),
				"tag":        MirrorableTag("v2.10.7"),
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
				"annotations": serviceAnnotations,
				"spec": map[string]any{
					"externalTrafficPolicy": "Local", // in order to preserve client IP addresses
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

		traefikConfig.Values["deployment"] = map[string]any{
			"additionalVolumes": []any{
				map[string]any{
					"name":     "traefik-dynamic",
					"emptyDir": map[string]any{},
				},
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
			"additionalContainers": []any{
				getConfigReloaderSidecar(),
			},
		}

		traefikConfig.Values["additionalVolumeMounts"] = []any{
			map[string]any{
				"name":      "traefik-dynamic",
				"mountPath": "/config",
				"readOnly":  true,
			},
			map[string]any{
				"name":      "kv-certs",
				"mountPath": "/certs",
				"readOnly":  true,
			},
		}

		traefikConfig.Values["additionalArguments"] = append(
			traefikConfig.Values["additionalArguments"].([]string),
			"--providers.file.directory=/config")
	}

	var overrides *HelmChartConfig
	if inst.Config.Cloud.Compute.Helm != nil && inst.Config.Cloud.Compute.Helm.Traefik != nil {
		overrides = inst.Config.Cloud.Compute.Helm.Traefik
	}

	// Check if there is an existing release to see if should be removed first.
	helmOptions := helmclient.RestConfClientOptions{
		RestConfig: restConfig,
		Options: &helmclient.Options{
			DebugLog: func(format string, v ...any) {
				log.Debug().Msgf(format, v...)
			},
			Namespace: traefikConfig.Namespace,
		},
	}

	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return false, fmt.Errorf("failed to create helm client: %w", err)
	}
	if existingRelease, err := helmClient.GetRelease(traefikConfig.ReleaseName); err != nil {
		if !errors.Is(err, driver.ErrReleaseNotFound) {
			return nil, fmt.Errorf("failed to get existing Traefik release: %w", err)
		}
	} else {
		var existingServiceAnnotations map[string]any
		if svc, _ := existingRelease.Config["service"].(map[string]any); svc != nil {
			existingServiceAnnotations, _ = svc["annotations"].(map[string]any)
		}

		if !maps.Equal(existingServiceAnnotations, serviceAnnotations) {
			log.Ctx(ctx).Warn().Msg("Existing Traefik installation has different service annotations, uninstalling it first")
			if err = helmClient.UninstallReleaseByName(traefikConfig.ReleaseName); err != nil {
				log.Warn().Msgf("Failed to uninstall existing Traefik release: %v", err)
			} else {
				time.Sleep(2 * time.Minute) // Give some time for Azure resources to be removed
			}
		}
	}

	startTime := time.Now().Add(-10 * time.Second)
	if _, _, err := inst.installHelmChart(ctx, restConfig, &traefikConfig, overrides, false); err != nil {
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

// This is a sidecar for the Traefik pod that writes a "dynamic" configuration file containing TLS certificate paths.
// Whenever it detects that the cert has changed, it touches the configuration file to trigger Traefik to reload it.
//
// Returned as a map so that the image reference can flow through mirror rewriting
// (see applyMirrorRewrites) when the user has opted into ACR mirroring.
func getConfigReloaderSidecar() map[string]any {
	script := `
crt_hash=$(sha256sum /certs/tls.crt 2> /dev/null)
echo '{"tls": {"certificates": [{"certFile":"/certs/tls.crt","keyFile":"/certs/tls.key"}]}}' > /config/dynamic-config.yml;

while true; do
	sleep 5m
	new_hash=$(sha256sum /certs/tls.crt 2> /dev/null)
	if [ "$crt_hash" != "$new_hash" ]; then
		echo "Certificate changed, updating Traefik configuration"
		touch /config/dynamic-config.yml
		crt_hash=$new_hash
	fi
done
`

	return map[string]any{
		"name":    "config-reloader",
		"image":   MirrorableImageReference(AzureLinuxImage),
		"command": []any{"bash", "-c", script},
		"volumeMounts": []any{
			map[string]any{
				"name":      "traefik-dynamic",
				"mountPath": "/config",
				"readOnly":  false,
			},
			map[string]any{
				"name":      "kv-certs",
				"mountPath": "/certs",
				"readOnly":  true,
			},
		},
	}
}

func (inst *Installer) installCertManager(ctx context.Context, restConfigPromise *install.Promise[*rest.Config]) (any, error) {
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, install.ErrDependencyFailed
	}

	log.Ctx(ctx).Info().Msg("Installing cert-manager")

	imageTag := MirrorableTag("1.12.15-4.3.0.20251206")
	certManagerConfig := HelmChartConfig{
		Namespace:   "cert-manager",
		ReleaseName: "cert-manager",
		ChartRef:    "oci://mcr.microsoft.com/azurelinux/helm/cert-manager",
		Version:     "1.12.12-12",
		Values: map[string]any{
			"cert-manager": map[string]any{
				"installCRDs": true,
				"image": map[string]any{
					"repository": MirrorableQualifiedRepository("mcr.microsoft.com/azurelinux/base/cert-manager-controller"),
					"tag":        imageTag,
				},
				"acmesolver": map[string]any{
					"image": map[string]any{
						"repository": MirrorableQualifiedRepository("mcr.microsoft.com/azurelinux/base/cert-manager-acmesolver"),
						"tag":        imageTag,
					},
				},
				"cainjector": map[string]any{
					"image": map[string]any{
						"repository": MirrorableQualifiedRepository("mcr.microsoft.com/azurelinux/base/cert-manager-cainjector"),
						"tag":        imageTag,
					},
				},
				"webhook": map[string]any{
					"image": map[string]any{
						"repository": MirrorableQualifiedRepository("mcr.microsoft.com/azurelinux/base/cert-manager-webhook"),
						"tag":        imageTag,
					},
				},
				"startupapicheck": map[string]any{
					"image": map[string]any{
						"repository": MirrorableQualifiedRepository("mcr.microsoft.com/azurelinux/base/cert-manager-cmctl"),
						"tag":        imageTag,
					},
				},
			},
		},
	}

	var overrides *HelmChartConfig
	if inst.Config.Cloud.Compute.Helm != nil && inst.Config.Cloud.Compute.Helm.CertManager != nil {
		overrides = inst.Config.Cloud.Compute.Helm.CertManager
	}

	if _, _, err := inst.installHelmChart(ctx, restConfig, &certManagerConfig, overrides, false); err != nil {
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

	chartVersion := "0.17.0"
	nvdpConfig := HelmChartConfig{
		Namespace:   "nvidia-device-plugin",
		ReleaseName: "nvidia-device-plugin",
		RepoName:    "nvdp",
		RepoUrl:     "https://nvidia.github.io/k8s-device-plugin",
		ChartRef:    "nvdp/nvidia-device-plugin",
		Version:     chartVersion,
		Values: map[string]any{
			"image": map[string]any{
				"repository": MirrorableQualifiedRepository("mcr.microsoft.com/oss/v2/nvidia/k8s-device-plugin"),
				"tag":        MirrorableTag("v" + chartVersion),
			},
			"nodeSelector": map[string]any{
				"kubernetes.azure.com/accelerator": "nvidia",
			},
			"affinity": nil, // the default affinity settings are incompatible with AKS
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

	if _, _, err := inst.installHelmChart(ctx, restConfig, &nvdpConfig, overrides, false); err != nil {
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
		// If the helm install hit transient errors and was retried, helm
		// rolled back and re-installed, bumping the revision past the
		// pre-install +1 we used as the log-watcher's selector. In that case
		// logsMap will be empty. Fall back to a one-shot fetch of pod logs
		// by the actual current release revision.
		if currentRevision, revErr := inst.GetTygerInstallationRevision(ctx, org); revErr == nil && currentRevision != revision+1 {
			if refreshed, fetchErr := fetchPodLogsByLabel(ctx, clientset, org.Cloud.KubernetesNamespace, fmt.Sprintf("tyger-helm-revision=%d", currentRevision)); fetchErr == nil {
				logsMap = refreshed
			}
		}
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
					// We are accepting Tyger.ControlPlane.Database.Migrations.MigrationRunner
					// as well for backwards compatibility.
					switch category {
					case "Tyger.ControlPlane.Database.Migrations.DatabaseVersions[NewerDatabaseVersionsExist]",
						"Tyger.ControlPlane.Database.Migrations.MigrationRunner[NewerDatabaseVersionsExist]":
						log.Ctx(ctx).Warn().Msgf("The database schema should be upgraded. Run `tyger api migrations list` to see the available migrations and `tyger api migrations apply` to apply them.")
						found = true

					case "Tyger.ControlPlane.Database.Migrations.DatabaseVersions[UsingMostRecentDatabaseVersion]",
						"Tyger.ControlPlane.Database.Migrations.MigrationRunner[UsingMostRecentDatabaseVersion]":
						log.Debug().Msg("Database schema is up to date")
						found = true
					}

					if found {
						break
					}
				}
			}
		}

		if !found {
			log.Warn().Msg("Unable to determine if database migrations are required")
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

	clustersConfigYaml, err := goccyyaml.Marshal(inst.Config.Cloud.Compute.Clusters)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal cluster configuration: %w", err)
	}
	clustersConfig := make([]map[string]any, 0)
	if err := goccyyaml.Unmarshal(clustersConfigYaml, &clustersConfig); err != nil {
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
			"image":              MirrorableImageReference(fmt.Sprintf("%s%styger-server:%s", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory(), install.ContainerImageTag)),
			"bufferSidecarImage": MirrorableImageReference(fmt.Sprintf("%s%sbuffer-sidecar:%s", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory(), install.ContainerImageTag)),
			"bufferCopierImage":  MirrorableImageReference(fmt.Sprintf("%s%sbuffer-copier:%s", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory(), install.ContainerImageTag)),
			"workerWaiterImage":  MirrorableImageReference(fmt.Sprintf("%s%sworker-waiter:%s", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory(), install.ContainerImageTag)),
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
			"containerRegistryProxy": inst.Config.Cloud.Compute.ContainerRegistryProxy,
			"accessControl": map[string]any{
				"enabled":   true,
				"tenantId":  org.Api.AccessControl.TenantID,
				"authority": cloud.AzurePublic.ActiveDirectoryAuthorityHost + org.Api.AccessControl.TenantID,
				"audience":  org.Api.AccessControl.ApiAppUri,
				"apiAppId":  org.Api.AccessControl.ApiAppId,
				"apiAppUri": org.Api.AccessControl.ApiAppUri,
				"cliAppUri": org.Api.AccessControl.CliAppUri,
				"cliAppId":  org.Api.AccessControl.CliAppId,
			},
			"tls": map[string]any{
				"letsEncrypt": map[string]any{
					"enabled": org.Api.TlsCertificateProvider == TlsCertificateProviderLetsEncrypt,
				},
			},
			"database": map[string]any{
				"host":           *dbServer.Properties.FullyQualifiedDomainName,
				"databaseName":   org.Cloud.DatabaseName,
				"port":           databasePort,
				"ownersRoleName": getDatabaseRoleName(org, unqualifiedOwnersRole),
			},
			"buffers": map[string]any{
				"storageAccounts":     buffersStorageAccountValues,
				"defaultLocation":     org.Cloud.Storage.DefaultBufferLocation,
				"activeLifetime":      org.Api.Buffers.ActiveLifetime,
				"softDeletedLifetime": org.Api.Buffers.SoftDeletedLifetime,
			},
			"logArchive": map[string]any{
				"storageAccountEndpoint": *logArchiveAccount.Properties.PrimaryEndpoints.Blob,
			},
			"clusterConfiguration": clustersConfig,
		},
	}

	if org.Api.AccessControl.MiseImage != "" {
		helmConfig.Values["accessControl"].(map[string]any)["mise"] = map[string]any{
			"enabled": true,
			"image":   MirrorableImageReference(org.Api.AccessControl.MiseImage),
		}
	}

	var overrides *HelmChartConfig
	if org.Api.Helm != nil && org.Api.Helm.Tyger != nil {
		overrides = org.Api.Helm.Tyger
	}

	adjustSpec := func(cs *helmclient.ChartSpec, c helmclient.Client) error {
		cs.CreateNamespace = false
		return nil
	}

	if manifest, valuesYaml, err = inst.installHelmChart(ctx, restConfig, &helmConfig, overrides, dryRun, adjustSpec); err != nil {
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

func (inst *Installer) installHelmChart(
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

	chartSpec, err := inst.GetChartSpec(ctx, helmChartConfig, helmClient, overrideHelmChartConfig, customizeSpec...)
	if err != nil {
		return "", "", err
	}

	if dryRun {
		manifest, err := helmClient.TemplateChart(&chartSpec, nil)
		if err != nil {
			return "", "", fmt.Errorf("failed to template chart: %w", err)
		}
		if err := inst.validateMirroredManifest(ctx, string(manifest)); err != nil {
			return "", "", err
		}
		return string(manifest), chartSpec.ValuesYaml, nil
	}

	// When mirroring is enabled, verify the final rendered manifest during the
	// real Helm install/upgrade. This catches images that were not wrapped in a
	// Mirrorable* type and so would have escaped the rewrite pass.
	var installOptions *helmclient.GenericHelmOptions
	if mirrorAcr, err := inst.GetResolvedMirrorAcr(ctx); err != nil {
		return "", "", err
	} else if mirrorAcr != nil {
		installOptions = &helmclient.GenericHelmOptions{
			PostRenderer: mirrorValidationPostRenderer{
				validate: func(manifest string) error {
					return inst.validateMirroredManifest(ctx, manifest)
				},
			},
		}
	}

	for i := 0; ; i++ {
		var release *release.Release
		release, err = helmClient.InstallOrUpgradeChart(ctx, &chartSpec, installOptions)
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

type mirrorValidationPostRenderer struct {
	validate func(string) error
}

func (r mirrorValidationPostRenderer) Run(renderedManifests *bytes.Buffer) (*bytes.Buffer, error) {
	if err := r.validate(renderedManifests.String()); err != nil {
		return nil, err
	}
	return renderedManifests, nil
}

// Resolves the helm chart spec for the given config. When the user has opted
// into ACR mirroring (cloud.mirrorAcr), this method also performs the
// necessary ACR copies in parallel and rewrites every Mirrorable* image and
// chart reference in the config to point at the mirror ACR. Mirroring is
// performed lazily and cached, so each artifact is copied at most once per
// installer run even when multiple organizations share the same images.
//
// User overrides supplied via the config file are applied BEFORE mirror
// rewriting, so anything the user explicitly sets wins and is not mirrored.
func (inst *Installer) GetChartSpec(
	ctx context.Context,
	helmChartConfig *HelmChartConfig,
	helmClient helmclient.Client,
	overrideHelmChartConfig *HelmChartConfig,
	customizeSpec ...func(*helmclient.ChartSpec, helmclient.Client) error,
) (helmclient.ChartSpec, error) {
	// Apply user overrides first. Before merging, copy the Mirrorable* marker
	// types from default values onto matching user override paths so overridden
	// image values are still declaratively mirrorable. Arbitrary user-added
	// values remain plain strings and are not mirrored by this pass.
	originalChartRef := helmChartConfig.ChartRef
	if overrideHelmChartConfig != nil {
		overrideHelmChartConfig = cloneHelmChartConfig(overrideHelmChartConfig)
		preserveMirrorableValueTypes(helmChartConfig.Values, overrideHelmChartConfig.Values)
		if err := mergo.Merge(helmChartConfig, overrideHelmChartConfig, mergo.WithOverride); err != nil {
			return helmclient.ChartSpec{}, fmt.Errorf("failed to merge helm config: %w", err)
		}
	}

	// Mirror every artifact still carrying a Mirrorable* marker (or, when
	// mirroring is disabled, simply unwrap typed values during YAML marshaling).
	if err := inst.applyMirrorRewrites(ctx, helmChartConfig, originalChartRef); err != nil {
		return helmclient.ChartSpec{}, err
	}

	if helmChartConfig.RepoUrl != "" {
		if err := helmClient.AddOrUpdateChartRepo(repo.Entry{Name: helmChartConfig.RepoName, URL: helmChartConfig.RepoUrl}); err != nil {
			return helmclient.ChartSpec{}, fmt.Errorf("failed to add helm repo: %w", err)
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
		Timeout:         5 * time.Minute,
		ValuesYaml:      string(values),
	}

	// When pulling an OCI chart from an Azure Container Registry, log helm's
	// registry client in to it using our Azure credential. Helm's registry
	// client reads only its own credentials file (not docker's), so without
	// this it would 401 on any private ACR. Best-effort: an ACR may be
	// configured for anonymous pulls, in which case the caller may not have
	// permission to obtain a refresh token; fall through and let the pull
	// itself succeed anonymously.
	if acrFqdn, ok := acrHostFromOciRef(chartSpec.ChartName); ok {
		if err := inst.loginHelmClientToAcr(ctx, helmClient, acrFqdn); err != nil {
			log.Ctx(ctx).Debug().Err(err).Str("registry", acrFqdn).Msg("Unable to authenticate helm registry client to ACR; proceeding (registry may allow anonymous pulls)")
		}
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
		return inst.uninstallTygerSingleOrg(ctx, restConfig, org)
	})
}

func (inst *Installer) uninstallTygerSingleOrg(ctx context.Context, restConfig *rest.Config, org *OrganizationConfig) error {
	if restConfig == nil {
		var err error
		restConfig, err = inst.GetUserRESTConfig(ctx)
		if err != nil {
			return fmt.Errorf("failed to get user REST config: %w", err)
		}
	}

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

	// Container registry proxy secret
	clientset.CoreV1().Secrets(org.Cloud.KubernetesNamespace).Delete(ctx, "container-registry-proxy", deleteOpts)

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
}

// --- ACR mirroring of helm charts and image references ---

// Repository prefix used for every mirrored artifact in the user's private
// ACR. All sources are placed under this prefix so they don't collide with
// anything else in the registry.
const MirrorRepoPrefix = "tyger"

// Returns the final path segment of a chart reference (e.g. "traefik" for
// "traefik/traefik" or "cert-manager" for "oci://.../helm/cert-manager").
func chartName(chartRef string) string {
	ref := strings.TrimPrefix(chartRef, "oci://")
	parts := strings.Split(ref, "/")
	return parts[len(parts)-1]
}

// Returns the repository path (without registry FQDN) where a chart with the
// given source ref is mirrored in the ACR.
func mirrorChartTargetRepo(chartRef string) string {
	return fmt.Sprintf("%s/helm/%s", MirrorRepoPrefix, chartName(chartRef))
}

// Returns the OCI URL of a mirrored chart.
func mirrorChartTargetRef(chartRef, mirrorAcrFqdn string) string {
	return fmt.Sprintf("oci://%s/%s", mirrorAcrFqdn, mirrorChartTargetRepo(chartRef))
}

// Returns the short name (without the .azurecr.io suffix) of the configured
// mirror ACR, or empty string if mirroring is disabled.
func (c *CloudConfig) GetMirrorAcrName() string {
	if c.MirrorAcr == "" {
		return ""
	}
	name, _, _ := strings.Cut(c.MirrorAcr, ".")
	return name
}

// Holds the installer-scoped, lazily-populated state for ACR mirroring: a
// one-time resolution of the mirror ACR's properties, and per-artifact
// promises that ensure each image/chart is copied at most once even when
// multiple organizations are installed in parallel.
type mirroringState struct {
	mu            sync.Mutex
	resolveOnce   sync.Once
	resolved      *ResolvedAcr
	resolveErr    error
	imagePromises map[string]*install.Promise[any]
	chartPromises map[string]*install.Promise[string]
}

// Returns the resolved mirror ACR properties, looking them up the first time
// it is called and caching the result for subsequent calls. Returns
// (nil, nil) when mirroring is disabled.
func (inst *Installer) GetResolvedMirrorAcr(ctx context.Context) (*ResolvedAcr, error) {
	if inst.Config.Cloud.MirrorAcr == "" {
		return nil, nil
	}

	inst.acrMirroringState.resolveOnce.Do(func() {
		inst.acrMirroringState.resolved, inst.acrMirroringState.resolveErr = inst.resolveAcr(ctx, inst.Config.Cloud.GetMirrorAcrName())
	})

	return inst.acrMirroringState.resolved, inst.acrMirroringState.resolveErr
}

// Imports the image (sourceRegistry/sourceRepo with given tag-or-digest) into
// the configured mirror ACR. The result is cached by source ref via a
// Promise, so concurrent callers share the same in-flight import.
func (inst *Installer) ensureImageMirrored(ctx context.Context, sourceRegistry, sourceRepo, tagOrDigest string) error {
	mirrorAcr, err := inst.GetResolvedMirrorAcr(ctx)
	if err != nil {
		return err
	}

	key := sourceRefString(sourceRegistry, sourceRepo, tagOrDigest)

	inst.acrMirroringState.mu.Lock()
	if inst.acrMirroringState.imagePromises == nil {
		inst.acrMirroringState.imagePromises = map[string]*install.Promise[any]{}
	}
	p, ok := inst.acrMirroringState.imagePromises[key]
	if !ok {
		p = install.NewPromise(ctx, &install.PromiseGroup{}, func(ctx context.Context) (any, error) {
			log.Ctx(ctx).Info().Msgf("Mirroring image %s to ACR '%s'", key, mirrorAcr.Name)
			paths := importPaths(sourceRepo, tagOrDigest, MirrorRepoPrefix+"/"+sourceRepo)
			return nil, inst.importImageToAcr(ctx, mirrorAcr, sourceRegistry, paths)
		})
		inst.acrMirroringState.imagePromises[key] = p
	}
	inst.acrMirroringState.mu.Unlock()

	return p.AwaitErr()
}

// Imports the given source chart into the mirror ACR and returns the OCI URL
// of the mirrored chart. Cached by source ref via a Promise. repoUrl is
// required for traditional (non-OCI) helm repos and ignored for OCI charts.
func (inst *Installer) ensureChartMirrored(ctx context.Context, chartRef, version, repoUrl string) (string, error) {
	mirrorAcr, err := inst.GetResolvedMirrorAcr(ctx)
	if err != nil {
		return "", err
	}

	key := fmt.Sprintf("%s@%s|%s", chartRef, version, repoUrl)

	inst.acrMirroringState.mu.Lock()
	if inst.acrMirroringState.chartPromises == nil {
		inst.acrMirroringState.chartPromises = map[string]*install.Promise[string]{}
	}
	p, ok := inst.acrMirroringState.chartPromises[key]
	if !ok {
		p = install.NewPromise(ctx, &install.PromiseGroup{}, func(ctx context.Context) (string, error) {
			log.Ctx(ctx).Info().Msgf("Mirroring chart %s:%s to ACR '%s'", chartRef, version, mirrorAcr.Name)
			targetRepoPath := mirrorChartTargetRepo(chartRef)
			if after, ok := strings.CutPrefix(chartRef, "oci://"); ok {
				ref := after
				slash := strings.Index(ref, "/")
				if slash < 0 {
					return "", fmt.Errorf("invalid OCI chart reference %q", chartRef)
				}
				registryHost := ref[:slash]
				repoPath := ref[slash+1:]
				if err := inst.importImageToAcr(ctx, mirrorAcr, registryHost, acrImportPaths{
					SourceImage: fmt.Sprintf("%s:%s", repoPath, version),
					TargetTag:   fmt.Sprintf("%s:%s", targetRepoPath, version),
				}); err != nil {
					return "", err
				}
			} else {
				if err := inst.pullAndPushHelmChart(ctx, mirrorAcr, chartName(chartRef), version, repoUrl, targetRepoPath); err != nil {
					return "", err
				}
			}
			return mirrorChartTargetRef(chartRef, mirrorAcr.LoginServer), nil
		})
		inst.acrMirroringState.chartPromises[key] = p
	}
	inst.acrMirroringState.mu.Unlock()

	return p.Await()
}

// Builds the source-image and target strings for an ARM ImportImage call from
// a (sourceRepo, tagOrDigest) source and a targetRepo.
func importPaths(sourceRepo, tagOrDigest, targetRepo string) acrImportPaths {
	if strings.HasPrefix(tagOrDigest, "sha256:") {
		return acrImportPaths{
			SourceImage:               fmt.Sprintf("%s@%s", sourceRepo, tagOrDigest),
			TargetRepositoryForDigest: targetRepo,
		}
	}
	return acrImportPaths{
		SourceImage: fmt.Sprintf("%s:%s", sourceRepo, tagOrDigest),
		TargetTag:   fmt.Sprintf("%s:%s", targetRepo, tagOrDigest),
	}
}

func sourceRefString(registry, repo, tagOrDigest string) string {
	sep := ":"
	if strings.HasPrefix(tagOrDigest, "sha256:") {
		sep = "@"
	}
	return fmt.Sprintf("%s/%s%s%s", registry, repo, sep, tagOrDigest)
}

// Mirrors all baked-in image and chart references in the chart config to the
// user's private ACR (in parallel) and rewrites the corresponding values in
// place. After this call helmChartConfig.ChartRef points at the mirrored
// chart (when applicable) and every detected Mirrorable* image reference is
// replaced with a mirror-pointing string.
//
// originalChartRef is the chart's pre-override default: if c.ChartRef is
// unchanged from originalChartRef, the chart itself is mirrored from the
// post-merge chart source; otherwise the user supplied a chart override and
// the chart is left as-is.
//
// When mirroring is disabled this method is a no-op: the Mirrorable* typed
// values are left as-is and yaml.Marshal serializes them as plain strings
// (they are all named string types).
func (inst *Installer) applyMirrorRewrites(ctx context.Context, c *HelmChartConfig, originalChartRef string) error {
	mirrorAcr, err := inst.GetResolvedMirrorAcr(ctx)
	if err != nil {
		return err
	}
	if mirrorAcr == nil {
		return nil
	}
	mirrorFqdn := mirrorAcr.LoginServer

	// Phase 1: walk the values tree, rewriting Mirrorable* typed values to
	// mirror-pointing strings and collecting the set of images to import.
	images := rewriteMirrorableValues(c.Values, mirrorFqdn)

	// Phase 2: kick off the chart mirror (if applicable) and all image
	// mirrors concurrently, then await them all.
	group := install.PromiseGroup{}

	mirrorChart := shouldMirrorChart(c, originalChartRef)
	var chartPromise *install.Promise[string]
	if mirrorChart {
		chartPromise = install.NewPromise(ctx, &group, func(ctx context.Context) (string, error) {
			return inst.ensureChartMirrored(ctx, c.ChartRef, c.Version, c.RepoUrl)
		})
	}

	for _, img := range images {
		img := img
		install.NewPromise(ctx, &group, func(ctx context.Context) (any, error) {
			return nil, inst.ensureImageMirrored(ctx, img.Registry, img.Repository, img.Tag)
		})
	}

	var errs []error
	for _, p := range group {
		if err := p.AwaitErr(); err != nil {
			errs = append(errs, err)
		}
	}
	if len(errs) > 0 {
		return fmt.Errorf("mirroring failed: %v", errs)
	}

	if mirrorChart {
		targetRef, err := chartPromise.Await()
		if err != nil {
			return fmt.Errorf("failed to mirror chart %s: %w", originalChartRef, err)
		}
		c.ChartRef = targetRef
		c.RepoName = ""
		c.RepoUrl = ""
	}

	return nil
}

// Identifies an image referenced by a helm chart's values that must be
// mirrored: source registry, repository, and tag-or-digest.
type sourceImage struct {
	Registry   string
	Repository string
	Tag        string
}

func shouldMirrorChart(c *HelmChartConfig, originalChartRef string) bool {
	return originalChartRef != "" && c.ChartRef == originalChartRef
}

func cloneHelmChartConfig(c *HelmChartConfig) *HelmChartConfig {
	if c == nil {
		return nil
	}
	clone := *c
	clone.Values = cloneValueMap(c.Values)
	return &clone
}

func cloneValueMap(values map[string]any) map[string]any {
	if values == nil {
		return nil
	}
	clone := make(map[string]any, len(values))
	for k, v := range values {
		clone[k] = cloneValue(v)
	}
	return clone
}

func cloneValue(value any) any {
	switch typed := value.(type) {
	case map[string]any:
		return cloneValueMap(typed)
	case []any:
		clone := make([]any, len(typed))
		for i, item := range typed {
			clone[i] = cloneValue(item)
		}
		return clone
	default:
		return value
	}
}

func preserveMirrorableValueTypes(defaults, overrides map[string]any) {
	if defaults == nil || overrides == nil {
		return
	}

	for key, overrideValue := range overrides {
		defaultValue, ok := defaults[key]
		if !ok {
			continue
		}
		overrides[key] = preserveMirrorableValueType(defaultValue, overrideValue)
	}
}

func preserveMirrorableValueType(defaultValue, overrideValue any) any {
	if overrideString, ok := getStringValue(overrideValue); ok {
		switch defaultValue.(type) {
		case MirrorableRegistry:
			return MirrorableRegistry(overrideString)
		case MirrorableQualifiedRepository:
			return MirrorableQualifiedRepository(overrideString)
		case MirrorableRepository:
			return MirrorableRepository(overrideString)
		case MirrorableImageReference:
			return MirrorableImageReference(overrideString)
		case MirrorableTag:
			return MirrorableTag(overrideString)
		}
	}

	switch defaultTyped := defaultValue.(type) {
	case map[string]any:
		if overrideTyped, ok := overrideValue.(map[string]any); ok {
			preserveMirrorableValueTypes(defaultTyped, overrideTyped)
			return overrideTyped
		}
	case []any:
		if overrideTyped, ok := overrideValue.([]any); ok {
			for i := 0; i < len(defaultTyped) && i < len(overrideTyped); i++ {
				overrideTyped[i] = preserveMirrorableValueType(defaultTyped[i], overrideTyped[i])
			}
			return overrideTyped
		}
	}

	return overrideValue
}

func getStringValue(value any) (string, bool) {
	reflected := reflect.ValueOf(value)
	if !reflected.IsValid() || reflected.Kind() != reflect.String {
		return "", false
	}
	return reflected.String(), true
}

// Walks values, replaces every Mirrorable* typed value with its
// mirror-pointing string equivalent, and returns the set of source images
// discovered.
//
// Within any single map[string]any, the following groupings are recognized:
//   - { MirrorableQualifiedRepository, MirrorableTag } — repository contains the registry
//   - { MirrorableRegistry, MirrorableRepository, MirrorableTag } — registry/repo split
//   - any leaf MirrorableImageReference — full "registry/repo:tag" reference
//
// Mirrorable* values that are not part of an identifiable image group (only a
// tag, or a lone repository) are left as their underlying string and not
// reported as images.
func rewriteMirrorableValues(values map[string]any, mirrorFqdn string) []sourceImage {
	var images []sourceImage
	var walk func(map[string]any)
	walk = func(values map[string]any) {
		if values == nil {
			return
		}

		var (
			regKey, repoKey, qrepoKey, tagKey string
			regVal, repoVal, qrepoVal, tagVal string
		)
		for k, v := range values {
			switch t := v.(type) {
			case MirrorableRegistry:
				regKey, regVal = k, string(t)
			case MirrorableRepository:
				repoKey, repoVal = k, string(t)
			case MirrorableQualifiedRepository:
				qrepoKey, qrepoVal = k, string(t)
			case MirrorableTag:
				tagKey, tagVal = k, string(t)
			case MirrorableImageReference:
				srcRegistry, srcRepo, tag, ok := parseFullImageRef(string(t))
				if !ok {
					continue
				}
				images = append(images, sourceImage{Registry: srcRegistry, Repository: srcRepo, Tag: tag})
				sep := ":"
				if strings.HasPrefix(tag, "sha256:") {
					sep = "@"
				}
				values[k] = fmt.Sprintf("%s/%s/%s%s%s", mirrorFqdn, MirrorRepoPrefix, srcRepo, sep, tag)
			case map[string]any:
				walk(t)
			case []any:
				for _, item := range t {
					if mm, ok := item.(map[string]any); ok {
						walk(mm)
					}
				}
			}
		}

		switch {
		case qrepoKey != "" && tagKey != "":
			slash := strings.Index(qrepoVal, "/")
			if slash > 0 {
				srcRegistry := qrepoVal[:slash]
				srcRepo := qrepoVal[slash+1:]
				images = append(images, sourceImage{Registry: srcRegistry, Repository: srcRepo, Tag: tagVal})
				values[qrepoKey] = fmt.Sprintf("%s/%s/%s", mirrorFqdn, MirrorRepoPrefix, srcRepo)
				values[tagKey] = tagVal
			}
		case regKey != "" && repoKey != "" && tagKey != "":
			images = append(images, sourceImage{Registry: regVal, Repository: repoVal, Tag: tagVal})
			values[regKey] = mirrorFqdn
			values[repoKey] = fmt.Sprintf("%s/%s", MirrorRepoPrefix, repoVal)
			values[tagKey] = tagVal
		}
	}
	walk(values)
	return images
}

// Parses a "registry/repo:tag" or "registry/repo@digest" reference.
func parseFullImageRef(ref string) (registry, repo, tag string, ok bool) {
	slash := strings.Index(ref, "/")
	if slash <= 0 {
		return "", "", "", false
	}
	registry = ref[:slash]
	rest := ref[slash+1:]
	if before, after, ok := strings.Cut(rest, "@"); ok {
		return registry, before, after, true
	}
	if colon := strings.LastIndex(rest, ":"); colon >= 0 {
		return registry, rest[:colon], rest[colon+1:], true
	}
	return "", "", "", false
}

// Verifies that every container image referenced by the rendered helm
// manifest points at the configured mirror ACR. Returns nil when mirroring
// is disabled. Used to catch images that escaped the Mirrorable* rewrite
// pass (e.g. images baked into the chart templates that we forgot to wrap).
func (inst *Installer) validateMirroredManifest(ctx context.Context, manifest string) error {
	mirrorAcr, err := inst.GetResolvedMirrorAcr(ctx)
	if err != nil {
		return err
	}
	if mirrorAcr == nil {
		return nil
	}

	images := extractManifestImages(manifest)
	expectedPrefix := mirrorAcr.LoginServer + "/"
	var unmirrored []string
	for _, img := range images {
		if !strings.HasPrefix(img, expectedPrefix) {
			unmirrored = append(unmirrored, img)
		}
	}
	if len(unmirrored) > 0 {
		sort.Strings(unmirrored)
		return fmt.Errorf("the rendered helm manifest references %d image(s) that do not point at the mirror ACR %q: %s",
			len(unmirrored), mirrorAcr.LoginServer, strings.Join(unmirrored, ", "))
	}
	return nil
}

// Extracts the set of container image references from a multi-document
// kubernetes YAML manifest. Looks for any "image:" string field anywhere in
// the documents.
func extractManifestImages(manifest string) []string {
	seen := map[string]struct{}{}
	var collect func(any)
	collect = func(node any) {
		switch n := node.(type) {
		case map[string]any:
			for k, v := range n {
				if k == "image" {
					if s, ok := v.(string); ok && s != "" {
						seen[s] = struct{}{}
						continue
					}
				}
				collect(v)
			}
		case []any:
			for _, item := range n {
				collect(item)
			}
		}
	}

	dec := goccyyaml.NewDecoder(strings.NewReader(manifest))
	for {
		var obj any
		if err := dec.Decode(&obj); err != nil {
			break
		}
		collect(obj)
	}

	out := make([]string, 0, len(seen))
	for img := range seen {
		out = append(out, img)
	}
	sort.Strings(out)
	return out
}
