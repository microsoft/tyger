package install

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"

	"dario.cat/mergo"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/cloud"
	"github.com/microsoft/tyger/cli/internal/httpclient"
	helmclient "github.com/mittwald/go-helm-client"
	"github.com/rs/zerolog/log"
	"helm.sh/helm/v3/pkg/repo"
	"helm.sh/helm/v3/pkg/storage/driver"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"sigs.k8s.io/yaml"
)

var (
	// Set during build but we provide defaults so that there is some value when debugging.
	// We will need to update these from time to time. Alternatively, you can set the registry
	// values using the --set command-line arugment.
	containerRegistry string = "tyger.azurecr.io"
	containerImageTag string = "v0.1.0-49-g5526c35"
)

func installTraefik(ctx context.Context, restConfigPromise *Promise[*rest.Config]) (any, error) {
	config := GetConfigFromContext(ctx)

	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, errDependencyFailed
	}

	log.Info().Msg("Installing Traefik")

	traefikConfig := HelmChartConfig{
		RepoName:  "traefik",
		Namespace: "traefik",
		RepoUrl:   "https://helm.traefik.io/traefik",
		ChartRef:  "traefik/traefik",
		Version:   "24.0.0",
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

	var overrides *HelmChartConfig
	if config.Api.Helm != nil && config.Api.Helm.Traefik != nil {
		overrides = config.Api.Helm.Traefik
	}

	startTime := time.Now().Add(-10 * time.Second)
	if err := installHelmChart(ctx, restConfig, &traefikConfig, overrides); err != nil {
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

	certManagerConfig := HelmChartConfig{
		Namespace: "cert-manager",
		RepoName:  "jetstack",
		RepoUrl:   "https://charts.jetstack.io",
		ChartRef:  "jetstack/cert-manager",
		Version:   "v1.13.0",
		Values: map[string]any{
			"installCRDs": true,
		},
	}

	var overrides *HelmChartConfig
	if config.Api.Helm != nil && config.Api.Helm.CertManager != nil {
		overrides = config.Api.Helm.CertManager
	}

	if err := installHelmChart(ctx, restConfig, &certManagerConfig, overrides); err != nil {
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

	nvdpConfig := HelmChartConfig{
		Namespace: "nvidia-device-plugin",
		RepoName:  "nvdp",
		RepoUrl:   "https://nvidia.github.io/k8s-device-plugin",
		ChartRef:  "nvdp/nvidia-device-plugin",
		Version:   "0.14.1",
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
	if config.Api.Helm != nil && config.Api.Helm.NvidiaDevicePlugin != nil {
		overrides = config.Api.Helm.NvidiaDevicePlugin
	}

	if err := installHelmChart(ctx, restConfig, &nvdpConfig, overrides); err != nil {
		return nil, fmt.Errorf("failed to install NVIDIA device plugin: %w", err)
	}

	return nil, nil
}

func InstallTyger(ctx context.Context) error {
	if containerRegistry == "" {
		panic("officialContainerRegistry not set during build")
	}

	if containerImageTag == "" {
		panic("officialContainerImageTag not set during build")
	}

	config := GetConfigFromContext(ctx)

	restConfig, err := getUserRESTConfig(ctx)
	if err != nil {
		return err
	}

	clustersConfigJson, err := json.Marshal(config.Cloud.Compute.Clusters)
	if err != nil {
		return fmt.Errorf("failed to marshal cluster configuration: %w", err)
	}

	helmConfig := HelmChartConfig{
		Namespace: TygerNamespace,
		ChartRef:  fmt.Sprintf("oci://%s/helm/tyger", containerRegistry),
		Version:   containerImageTag,
		Values: map[string]any{
			"server": map[string]any{
				"image":              fmt.Sprintf("%s/tyger-server:%s", containerRegistry, containerImageTag),
				"bufferSidecarImage": fmt.Sprintf("%s/buffer-sidecar:%s", containerRegistry, containerImageTag),
				"workerWaiterImage":  fmt.Sprintf("%s/worker-waiter:%s", containerRegistry, containerImageTag),
				"hostname":           config.Api.DomainName,
				"security": map[string]any{
					"enabled":   true,
					"authority": cloud.AzurePublic.ActiveDirectoryAuthorityHost + config.Api.Auth.TenantID,
					"audience":  config.Api.Auth.ApiAppUri,
					"cliAppUri": config.Api.Auth.CliAppUri,
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

	var overrides *HelmChartConfig
	if config.Api.Helm != nil && config.Api.Helm.Tyger != nil {
		overrides = config.Api.Helm.Tyger
	}

	ajustSpec := func(cs *helmclient.ChartSpec, c helmclient.Client) error {
		cs.CreateNamespace = false
		cs.Atomic = true
		_, err = c.GetRelease(helmConfig.Namespace)
		if err != nil {
			if err == driver.ErrReleaseNotFound {
				// This is the the initial release. In case of failure, we do not want to roll back, since the database PVCs
				// are not deleted and the subsequent installation will create new new secrets that will not be the same
				// as the passwords stored in the PVCs.
				cs.Atomic = false
			} else {
				return err
			}
		}
		return nil
	}

	if err := installHelmChart(ctx, restConfig, &helmConfig, overrides, ajustSpec); err != nil {
		return fmt.Errorf("failed to install Tyger Helm chart: %w", err)
	}

	baseEndpoint := fmt.Sprintf("https://%s", config.Api.DomainName)

	healthCheckEndpoint := fmt.Sprintf("%s/healthcheck", baseEndpoint)

	for i := 0; ; i++ {
		resp, err := httpclient.DefaultRetryableClient.Get(healthCheckEndpoint)
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
			log.Info().Msgf("Tyger API up at %s", baseEndpoint)
			break
		}

		if exit {
			return ErrAlreadyLoggedError
		}

		time.Sleep(time.Second)
	}

	return nil
}

func installHelmChart(
	ctx context.Context,
	restConfig *rest.Config,
	helmChartConfig,
	overrideHelmChartConfig *HelmChartConfig,
	customizeSpec ...func(*helmclient.ChartSpec, helmclient.Client) error,
) error {
	helmOptions := helmclient.RestConfClientOptions{
		RestConfig: restConfig,
		Options: &helmclient.Options{
			DebugLog: func(format string, v ...interface{}) {
				log.Debug().Msgf(format, v...)
			},
			Namespace: helmChartConfig.Namespace,
		},
	}

	if overrideHelmChartConfig != nil {
		if err := mergo.Merge(helmChartConfig, overrideHelmChartConfig, mergo.WithOverride); err != nil {
			return fmt.Errorf("failed to merge helm config: %w", err)
		}
	}

	values, err := yaml.Marshal(helmChartConfig.Values)
	if err != nil {
		return fmt.Errorf("failed to marshal helm values: %w", err)
	}

	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return fmt.Errorf("failed to create helm client: %w", err)
	}

	if helmChartConfig.RepoUrl != "" {
		err = helmClient.AddOrUpdateChartRepo(repo.Entry{Name: helmChartConfig.RepoName, URL: helmChartConfig.RepoUrl})
		if err != nil {
			return fmt.Errorf("failed to add helm repo: %w", err)
		}
	}

	chartSpec := helmclient.ChartSpec{
		ReleaseName:     helmChartConfig.Namespace,
		ChartName:       helmChartConfig.ChartRef,
		Version:         helmChartConfig.Version,
		Namespace:       helmChartConfig.Namespace,
		CreateNamespace: true,
		Wait:            true,
		WaitForJobs:     true,
		Atomic:          true,
		UpgradeCRDs:     true,
		Timeout:         2 * time.Minute,
		ValuesYaml:      string(values),
	}

	for _, f := range customizeSpec {
		if err := f(&chartSpec, helmClient); err != nil {
			return err
		}
	}

	for i := 0; ; i++ {
		_, err = helmClient.InstallOrUpgradeChart(ctx, &chartSpec, nil)
		if err == nil || i == 30 {
			break
		}

		if strings.Contains(err.Error(), "the server could not find the requested resource") {
			// we get this transient error from time to time, related to the installation of CRDs
			log.Info().Err(err).Msg("Possible transient error. Will retry...")
			time.Sleep(10 * time.Second)
		} else {
			return err
		}
	}
	return err
}

func UninstallTyger(ctx context.Context) error {
	restConfig, err := getUserRESTConfig(ctx)
	if err != nil {
		return err
	}

	// 1. Remove the Helm chart

	helmOptions := helmclient.RestConfClientOptions{
		RestConfig: restConfig,
		Options: &helmclient.Options{
			DebugLog: func(format string, v ...interface{}) {
				log.Debug().Msgf(format, v...)
			},
			Namespace: TygerNamespace,
		},
	}

	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return fmt.Errorf("failed to create helm client: %w", err)
	}

	if err := helmClient.UninstallReleaseByName(TygerNamespace); err != nil {
		if !errors.Is(err, driver.ErrReleaseNotFound) {
			return fmt.Errorf("failed to uninstall Tyger Helm chart: %w", err)
		}
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create Kubernetes client: %w", err)
	}

	err = clientset.CoreV1().PersistentVolumeClaims(TygerNamespace).DeleteCollection(
		ctx, metav1.DeleteOptions{}, metav1.ListOptions{
			LabelSelector: fmt.Sprintf("app.kubernetes.io/instance=%s", TygerNamespace),
		})
	if err != nil {
		return fmt.Errorf("failed to delete PVCs: %w", err)
	}

	// 2. Remove the finalizer we put on run pods so that they can be deleted

	tygerRunLabelSelector := "tyger-run"

	pods, err := clientset.CoreV1().Pods(TygerNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: tygerRunLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list pods: %w", err)
	}
	patchData := []byte(`[ { "op": "remove", "path": "/metadata/finalizers" } ]`)
	for _, pod := range pods.Items {
		_, err := clientset.CoreV1().Pods(TygerNamespace).Patch(ctx, pod.Name, types.JSONPatchType, patchData, metav1.PatchOptions{})
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

	// Pods
	err = clientset.CoreV1().Pods(TygerNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
		LabelSelector: tygerRunLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to delete pods: %w", err)
	}

	// Jobs
	err = clientset.BatchV1().Jobs(TygerNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
		LabelSelector: tygerRunLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to delete jobs: %w", err)
	}
	// StatefulSets
	err = clientset.AppsV1().StatefulSets(TygerNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
		LabelSelector: tygerRunLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to delete statefulsets: %w", err)
	}
	// Secrets
	err = clientset.CoreV1().Secrets(TygerNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
		LabelSelector: tygerRunLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to delete secrets: %w", err)
	}

	// Services. For some reason, there is no DeleteCollection method for services.
	services, err := clientset.CoreV1().Services(TygerNamespace).List(ctx, metav1.ListOptions{
		LabelSelector: tygerRunLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to list services: %w", err)
	}
	for _, s := range services.Items {
		err = clientset.CoreV1().Services(TygerNamespace).Delete(ctx, s.Name, deleteOpts)
		if err != nil {
			return fmt.Errorf("failed to delete service '%s': %w", s.Name, err)
		}
	}

	return nil
}
