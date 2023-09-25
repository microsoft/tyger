package setup

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"dario.cat/mergo"
	helmclient "github.com/mittwald/go-helm-client"
	"github.com/rs/zerolog/log"
	"helm.sh/helm/v3/pkg/repo"
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
		return nil, ErrDependencyFailed
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
		return nil, ErrDependencyFailed
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
		return nil, fmt.Errorf("failed to add traefik repo: %w", err)
	}

	chartSpec := helmclient.ChartSpec{
		ReleaseName:     namespace,
		ChartName:       "traefik/traefik",
		Namespace:       namespace,
		CreateNamespace: true,
		Version:         traefikConfig.ChartVersion,
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
		return nil, ErrDependencyFailed
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
		return nil, fmt.Errorf("failed to add jetstack repo: %w", err)
	}

	chartSpec := helmclient.ChartSpec{
		ReleaseName:     namespace,
		ChartName:       "jetstack/cert-manager",
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

	if _, err := helmClient.InstallOrUpgradeChart(context.Background(), &chartSpec, nil); err != nil {
		return nil, fmt.Errorf("failed to install cert-manager: %w", err)
	}

	return nil, nil
}

func installNvidiaDevicePlugin(ctx context.Context, restConfigPromise *Promise[*rest.Config]) (any, error) {
	config := GetConfigFromContext(ctx)

	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, ErrDependencyFailed
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

	err = helmClient.AddOrUpdateChartRepo(repo.Entry{Name: "nvdp", URL: "https://nvidia.github.io/k8s-device-plugin"})
	if err != nil {
		return nil, fmt.Errorf("failed to add jetstack repo: %w", err)
	}

	nvdpConfig := HelmChartConfig{
		ChartRepo:    "https://nvidia.github.io/k8s-device-plugin",
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
		ChartName:       "nvdp/nvidia-device-plugin",
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

	if _, err := helmClient.InstallOrUpgradeChart(context.Background(), &chartSpec, nil); err != nil {
		return nil, fmt.Errorf("failed to install NVIDIA device plugin: %w", err)
	}

	return nil, nil
}
