package install

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"dario.cat/mergo"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	helmclient "github.com/mittwald/go-helm-client"
	"github.com/rs/zerolog/log"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage/driver"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

const (
	TygerNamespace = "tyger"
)

func createTygerNamespace(ctx context.Context, restConfigPromise *Promise[*rest.Config]) (any, error) {
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	clientset := kubernetes.NewForConfigOrDie(restConfig)

	_, err = clientset.CoreV1().Namespaces().Create(ctx, &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "tyger"}}, metav1.CreateOptions{})
	if err == nil || apierrors.IsAlreadyExists(err) {
		return nil, nil
	}

	return nil, fmt.Errorf("failed to create 'tyger' namespace: %w", err)
}

func installTraefik(ctx context.Context, restConfigPromise *Promise[*rest.Config]) (any, error) {
	config := GetConfigFromContext(ctx)

	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	log.Info().Msg("Installing Traefik")

	namespace := "traefik"

	helmOptions := helmclient.RestConfClientOptions{
		RestConfig: restConfig,
		Options: &helmclient.Options{
			DebugLog: func(format string, v ...interface{}) {
				log.Debug().Msgf(format, v...)
			},
			Namespace: namespace,
		},
	}

	traefikConfig := HelmChartConfig{
		ChartRepo:    "https://helm.traefik.io/traefik",
		ChartRef:     "traefik/traefik",
		ChartVersion: "24.0.0",
		Values: map[string]any{
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
					"service.beta.kubernetes.io/azure-dns-label-name": strings.Split(config.Api.DomainName, ".")[0],
				},
			},
		}}

	if config.Api.Helm != nil && config.Api.Helm.Traefik != nil {
		if err := mergo.Merge(&traefikConfig, config.Api.Helm.Traefik, mergo.WithOverride); err != nil {
			return nil, fmt.Errorf("failed to merge traefik config: %w", err)
		}
	}

	values, err := yaml.Marshal(traefikConfig.Values)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal traefik values: %w", err)
	}

	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create helm client: %w", err)
	}

	err = helmClient.AddOrUpdateChartRepo(repo.Entry{Name: "traefik", URL: traefikConfig.ChartRepo})
	if err != nil {
		return nil, fmt.Errorf("failed to add helm repo: %w", err)
	}

	chartSpec := helmclient.ChartSpec{
		ReleaseName:     namespace,
		ChartName:       traefikConfig.ChartRef,
		Version:         traefikConfig.ChartVersion,
		Namespace:       namespace,
		CreateNamespace: true,
		Wait:            true,
		WaitForJobs:     true,
		Atomic:          true,
		UpgradeCRDs:     true,
		Timeout:         2 * time.Minute,
		ValuesYaml:      string(values),
	}

	startTime := time.Now().Add(-10 * time.Second)
	if _, err := helmClient.InstallOrUpgradeChart(ctx, &chartSpec, nil); err != nil {
		installErr := err

		// List warning events in the namespace
		clientset := kubernetes.NewForConfigOrDie(restConfig)
		events, err := clientset.CoreV1().Events(namespace).List(ctx, metav1.ListOptions{})
		if err != nil {
			return nil, fmt.Errorf("failed to install Traefik: %w", installErr)
		}

		sort.SliceStable(events.Items, func(i, j int) bool {
			return events.Items[i].LastTimestamp.After(events.Items[j].LastTimestamp.Time)
		})

		for _, event := range events.Items {
			if event.Type == corev1.EventTypeWarning && event.LastTimestamp.After(startTime) {
				log.Warn().Str("Reason", event.Reason).Msg(event.Message)
			}
		}

		return nil, fmt.Errorf("failed to install Traefik: %w", installErr)
	}

	return nil, nil
}

func installCertManager(ctx context.Context, restConfigPromise *Promise[*rest.Config]) (any, error) {
	config := GetConfigFromContext(ctx)
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	log.Info().Msg("Installing cert-manager")

	namespace := "cert-manager"

	helmOptions := helmclient.RestConfClientOptions{
		RestConfig: restConfig,
		Options: &helmclient.Options{
			DebugLog: func(format string, v ...interface{}) {
				log.Debug().Msgf(format, v...)
			},
			Namespace: namespace,
		},
	}

	certManagerConfig := HelmChartConfig{
		ChartRepo:    "https://charts.jetstack.io",
		ChartRef:     "jetstack/cert-manager",
		ChartVersion: "v1.13.0",
		Values: map[string]any{
			"installCRDs": true,
		},
	}

	if config.Api.Helm != nil && config.Api.Helm.CertManager != nil {
		if err := mergo.Merge(&certManagerConfig, config.Api.Helm.CertManager, mergo.WithOverride); err != nil {
			return nil, fmt.Errorf("failed to merge cert-manager config: %w", err)
		}
	}

	values, err := yaml.Marshal(certManagerConfig.Values)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal cert-manager values: %w", err)
	}

	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create helm client: %w", err)
	}

	err = helmClient.AddOrUpdateChartRepo(repo.Entry{Name: "jetstack", URL: certManagerConfig.ChartRepo})
	if err != nil {
		return nil, fmt.Errorf("failed to add helm repo: %w", err)
	}

	chartSpec := helmclient.ChartSpec{
		ReleaseName:     namespace,
		ChartName:       certManagerConfig.ChartRef,
		Namespace:       namespace,
		CreateNamespace: true,
		Version:         certManagerConfig.ChartVersion,
		Wait:            true,
		WaitForJobs:     true,
		Atomic:          true,
		UpgradeCRDs:     true,
		Timeout:         5 * time.Minute,
		ValuesYaml:      string(values),
	}

	if _, err := helmClient.InstallOrUpgradeChart(ctx, &chartSpec, nil); err != nil {
		return nil, fmt.Errorf("failed to install cert-manager: %w", err)
	}

	return nil, nil
}

func installNvidiaDevicePlugin(ctx context.Context, restConfigPromise *Promise[*rest.Config]) (any, error) {
	config := GetConfigFromContext(ctx)

	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	log.Info().Msg("Installing nvidia-device-plugin")

	namespace := "nvidia-device-plugin"

	helmOptions := helmclient.RestConfClientOptions{
		RestConfig: restConfig,
		Options: &helmclient.Options{
			DebugLog: func(format string, v ...interface{}) {
				log.Debug().Msgf(format, v...)
			},
			Namespace: namespace,
		},
	}
	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create helm client: %w", err)
	}

	nvdpConfig := HelmChartConfig{
		ChartRepo:    "https://nvidia.github.io/k8s-device-plugin",
		ChartRef:     "nvdp/nvidia-device-plugin",
		ChartVersion: "0.14.1",
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
	if config.Api.Helm != nil && config.Api.Helm.NvidiaDevicePlugin != nil {
		if err := mergo.Merge(&nvdpConfig, config.Api.Helm.NvidiaDevicePlugin, mergo.WithOverride); err != nil {
			return nil, fmt.Errorf("failed to merge NVDIA device plugin config: %w", err)
		}
	}

	values, err := yaml.Marshal(nvdpConfig.Values)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal traefik values: %w", err)
	}

	chartSpec := helmclient.ChartSpec{
		ReleaseName:     namespace,
		ChartName:       nvdpConfig.ChartRef,
		Namespace:       namespace,
		CreateNamespace: true,
		Version:         nvdpConfig.ChartVersion,
		Wait:            true,
		WaitForJobs:     true,
		Atomic:          true,
		UpgradeCRDs:     true,
		Timeout:         5 * time.Minute,
		ValuesYaml:      string(values),
	}

	err = helmClient.AddOrUpdateChartRepo(repo.Entry{Name: "nvdp", URL: nvdpConfig.ChartRepo})
	if err != nil {
		return nil, fmt.Errorf("failed to add helm repo: %w", err)
	}

	if _, err := helmClient.InstallOrUpgradeChart(ctx, &chartSpec, nil); err != nil {
		return nil, fmt.Errorf("failed to install NVIDIA device plugin: %w", err)
	}

	return nil, nil
}

func InstallTyger(ctx context.Context) error {
	config := GetConfigFromContext(ctx)

	restConfig, err := getAdminRESTConfig(ctx)
	if err != nil {
		return err
	}

	namespace := "tyger"

	helmOptions := helmclient.RestConfClientOptions{
		RestConfig: restConfig,
		Options: &helmclient.Options{
			DebugLog: func(format string, v ...interface{}) {
				log.Debug().Msgf(format, v...)
			},
			Namespace: namespace,
		},
	}
	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return fmt.Errorf("failed to create helm client: %w", err)
	}

	clustersConfigJson, err := json.Marshal(config.Cloud.Compute.Clusters)
	if err != nil {
		return fmt.Errorf("failed to marshal cluster configuration: %w", err)
	}

	helmConfig := HelmChartConfig{
		ChartRepo:    "",
		ChartRef:     "tyger/tyger",
		ChartVersion: "",
		Values: map[string]any{
			"server": map[string]any{
				"image":              "TODO",
				"bufferSidecarImage": "TODO",
				"workerWaiterImage":  "TODO",
				"hostname":           config.Api.DomainName,
				"security": map[string]any{
					"enabled":   true,
					"authority": cloud.AzurePublic.ActiveDirectoryAuthorityHost + config.Api.Auth.TenantID,
					"audience":  config.Api.Auth.ApiAppUri,
				},
				"tls": map[string]any{
					"letsEncrypt": map[string]any{
						"enabled": true,
					},
				},
				"storageAccountConnectionStringSecretName":     config.Cloud.Storage.Buffers[0].Name, // TODO: multiple buffers
				"logsStorageAccountConnectionStringSecretName": config.Cloud.Storage.Logs.Name,
				"clusterConfigurationJson":                     string(clustersConfigJson),
			},
		},
	}

	if config.Api.Helm != nil && config.Api.Helm.Tyger != nil {
		if err := mergo.Merge(&helmConfig, config.Api.Helm.Tyger, mergo.WithOverride); err != nil {
			return fmt.Errorf("failed to merge Tyger Helm config: %w", err)
		}
	}

	values, err := yaml.Marshal(helmConfig.Values)
	if err != nil {
		return fmt.Errorf("failed to marshal Tyger Helm values: %w", err)
	}

	if helmConfig.ChartRepo != "" {
		err = helmClient.AddOrUpdateChartRepo(repo.Entry{Name: "tyger", URL: helmConfig.ChartRepo})
		if err != nil {
			return fmt.Errorf("failed to add helm repo: %w", err)
		}
	}

	atomic := true
	_, err = helmClient.GetRelease(namespace)
	if err != nil {
		if err == driver.ErrReleaseNotFound {
			// This is the the initial release. In case of failure, we do not want to roll back, since the database PVCs
			// are not deleted and the subsequent installation will create new new secrets that will not be the same
			// as the passwords stored in the PVCs.
			atomic = false
		} else {
			return err
		}
	}

	chartSpec := helmclient.ChartSpec{
		ReleaseName:     namespace,
		ChartName:       helmConfig.ChartRef,
		Namespace:       namespace,
		CreateNamespace: true,
		Version:         helmConfig.ChartVersion,
		Wait:            true,
		WaitForJobs:     true,
		Atomic:          atomic,
		Timeout:         5 * time.Minute,
		ValuesYaml:      string(values),
	}

	go func() {
		<-ctx.Done()
		log.Warn().Msg("HELM Cancelling...")
	}()

	if _, err := helmClient.InstallOrUpgradeChart(ctx, &chartSpec, nil); err != nil {
		return fmt.Errorf("failed to install Tyger Helm chart: %w", err)
	}

	healthCheckEndpoint := fmt.Sprintf("https://%s/healthcheck", config.Api.DomainName)

	for i := 0; ; i++ {
		resp, err := http.Get(healthCheckEndpoint)
		errorLogger := log.Debug()
		exit := false
		if i == 30 {
			exit = true
			errorLogger = log.Error()
		}
		if err != nil {
			errorLogger.Err(err).Msg("Tyger health check failed")
		} else if resp.StatusCode != http.StatusOK {
			errorLogger.Msgf("Tyger health check failed with status code %d", resp.StatusCode)
		} else {
			log.Info().Msgf("Tyger API up at %s", healthCheckEndpoint)
			break
		}

		if exit {
			return ErrAlreadyLoggedError
		}

		time.Sleep(time.Second)
	}

	return nil
}
