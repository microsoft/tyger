package install

import (
	"bufio"
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
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/postgresql/armpostgresqlflexibleservers/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/hashicorp/go-retryablehttp"
	"github.com/microsoft/tyger/cli/internal/httpclient"
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
		RepoName:    "traefik",
		Namespace:   "traefik",
		ReleaseName: "traefik",
		RepoUrl:     "https://helm.traefik.io/traefik",
		ChartRef:    "traefik/traefik",
		Version:     "24.0.0",
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
		Namespace:   "cert-manager",
		ReleaseName: "cert-manager",
		RepoName:    "jetstack",
		RepoUrl:     "https://charts.jetstack.io",
		ChartRef:    "jetstack/cert-manager",
		Version:     "v1.13.0",
		Values: map[string]any{
			"installCRDs": true,
		},
	}

	var overrides *HelmChartConfig
	if config.Api.Helm != nil && config.Api.Helm.CertManager != nil {
		overrides = config.Api.Helm.CertManager
	}

	if _, _, err := installHelmChart(ctx, restConfig, &certManagerConfig, overrides, false); err != nil {
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
	if config.Api.Helm != nil && config.Api.Helm.NvidiaDevicePlugin != nil {
		overrides = config.Api.Helm.NvidiaDevicePlugin
	}

	if _, _, err := installHelmChart(ctx, restConfig, &nvdpConfig, overrides, false); err != nil {
		return nil, fmt.Errorf("failed to install NVIDIA device plugin: %w", err)
	}

	return nil, nil
}

func InstallTyger(ctx context.Context) error {
	restConfig, err := GetUserRESTConfig(ctx)
	if err != nil {
		return err
	}

	manifest, _, err := InstallTygerHelmChart(ctx, restConfig, false)
	if err != nil {
		return err
	}

	config := GetConfigFromContext(ctx)
	baseEndpoint := fmt.Sprintf("https://%s", config.Api.DomainName)
	healthCheckEndpoint := fmt.Sprintf("%s/healthcheck", baseEndpoint)

	client := httpclient.NewRetryableClient()
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
			errorLogger = log.Error()
		}
		if err != nil {
			if errors.Is(err, ctx.Err()) {
				return err
			}
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

	migrationRunnerJob, err := getMigrationRunnerJobDefinitionFromManifest(manifest)
	if err != nil {
		return fmt.Errorf("failed to get migration worker job from manifest: %w", err)
	}

	clientset, err := kubernetes.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create kubernetes client: %w", err)
	}

	migrationRunnerJob, err = clientset.BatchV1().Jobs(TygerNamespace).Get(ctx, migrationRunnerJob.Name, metav1.GetOptions{})
	if err != nil {
		return fmt.Errorf("failed to get migration worker job: %w", err)
	}

	podList, err := clientset.CoreV1().Pods(migrationRunnerJob.Namespace).List(ctx, metav1.ListOptions{
		LabelSelector: metav1.FormatLabelSelector(migrationRunnerJob.Spec.Selector),
	})
	if err != nil {
		return fmt.Errorf("failed to list migration runner pods: %w", err)
	}

	if len(podList.Items) == 0 {
		return errors.New("no migration runner pods found")
	}

	sort.Slice(podList.Items, func(i, j int) bool {
		return podList.Items[i].CreationTimestamp.Before(&podList.Items[j].CreationTimestamp)
	})

	pod := podList.Items[len(podList.Items)-1]
	logsRequest := clientset.CoreV1().Pods(pod.Namespace).GetLogs(pod.Name, &corev1.PodLogOptions{})
	logsReader, err := logsRequest.Stream(ctx)
	if err != nil {
		return fmt.Errorf("failed to get migration runner logs: %w", err)
	}
	defer logsReader.Close()

	found := false
	scanner := bufio.NewScanner(logsReader)
	for scanner.Scan() {
		line := scanner.Text()
		var parsedLine map[string]any

		if err := json.Unmarshal([]byte(line), &parsedLine); err != nil {
			return fmt.Errorf("failed to parse migration runner log line: %w", err)
		}

		if category, ok := parsedLine["category"].(string); ok {
			if category == "Tyger.Server.Database.Migrations.MigrationRunner[NewerDatabaseVersionsExist]" {
				log.Warn().Msgf("The database schema should be upgraded. Run `tyger api migrations list` to see the available migrations and `tyger api migrations apply` to apply them.")
				found = true
				break
			}

			if category == "Tyger.Server.Database.Migrations.MigrationRunner[UsingMostRecentDatabaseVersion]" {
				log.Debug().Msg("Database schema is up to date")
				found = true
				break
			}
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("failed to split logsResult into lines: %w", err)
	}

	if !found {
		return errors.New("failed to find expected migration log message")
	}

	return nil
}

func InstallTygerHelmChart(ctx context.Context, restConfig *rest.Config, dryRun bool) (manifest string, valuesYaml string, err error) {
	if containerRegistry == "" {
		panic("officialContainerRegistry not set during build")
	}

	if containerImageTag == "" {
		panic("officialContainerImageTag not set during build")
	}

	config := GetConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	clustersConfigJson, err := json.Marshal(config.Cloud.Compute.Clusters)
	if err != nil {
		return "", "", fmt.Errorf("failed to marshal cluster configuration: %w", err)
	}

	identitiesClient, err := armmsi.NewUserAssignedIdentitiesClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create managed identities client: %w", err)
	}

	tygerServerIdentity, err := identitiesClient.Get(ctx, config.Cloud.ResourceGroup, tygerServerManagedIdentityName, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to get managed identity: %w", err)
	}

	migrationRunnerIdentity, err := identitiesClient.Get(ctx, config.Cloud.ResourceGroup, migrationRunnerManagedIdentityName, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to get managed identity: %w", err)
	}

	storageClient, err := armstorage.NewAccountsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create storage client: %w", err)
	}

	buffersStorageAccountValues := make([]map[string]any, 0)
	for _, accountConfig := range config.Cloud.Storage.Buffers {
		acc, err := storageClient.GetProperties(ctx, config.Cloud.ResourceGroup, accountConfig.Name, nil)
		if err != nil {
			return "", "", fmt.Errorf("failed to get buffer storage account properties: %w", err)
		}

		accountValues := map[string]any{
			"name":     accountConfig.Name,
			"location": accountConfig.Location,
			"endpoint": *acc.Properties.PrimaryEndpoints.Blob,
		}

		buffersStorageAccountValues = append(buffersStorageAccountValues, accountValues)
	}

	logArchiveAccount, err := storageClient.GetProperties(ctx, config.Cloud.ResourceGroup, config.Cloud.Storage.Logs.Name, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to get logs storage account properties: %w", err)
	}

	dbServersClient, err := armpostgresqlflexibleservers.NewServersClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to create PostgreSQL server client: %w", err)
	}

	dbServerName, err := getDatabaseServerName(ctx, config, cred, false)
	if err != nil {
		return "", "", fmt.Errorf("failed to get database server name: %w", err)
	}

	dbServer, err := dbServersClient.Get(ctx, config.Cloud.ResourceGroup, dbServerName, nil)
	if err != nil {
		return "", "", fmt.Errorf("failed to get PostgreSQL server: %w", err)
	}

	helmConfig := HelmChartConfig{
		Namespace:   TygerNamespace,
		ReleaseName: DefaultTygerReleaseName,
		ChartRef:    fmt.Sprintf("oci://%s/helm/tyger", containerRegistry),
		Version:     containerImageTag,
		Values: map[string]any{
			"image":              fmt.Sprintf("%s/tyger-server:%s", containerRegistry, containerImageTag),
			"bufferSidecarImage": fmt.Sprintf("%s/buffer-sidecar:%s", containerRegistry, containerImageTag),
			"workerWaiterImage":  fmt.Sprintf("%s/worker-waiter:%s", containerRegistry, containerImageTag),
			"hostname":           config.Api.DomainName,
			"identity": map[string]any{
				"tygerServer": map[string]any{
					"name":     tygerServerIdentity.Name,
					"clientId": tygerServerIdentity.Properties.ClientID,
				},
				"migrationRunner": map[string]any{
					"name":     migrationRunnerIdentity.Name,
					"clientId": migrationRunnerIdentity.Properties.ClientID,
				},
			},
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
			"database": map[string]any{
				"host":         *dbServer.Properties.FullyQualifiedDomainName,
				"databaseName": databaseName,
				"port":         databasePort,
			},
			"buffers": map[string]any{
				"storageAccounts": buffersStorageAccountValues,
			},
			"logArchive": map[string]any{
				"storageAccountEndpoint": *logArchiveAccount.Properties.PrimaryEndpoints.Blob,
			},
			"clusterConfigurationJson": string(clustersConfigJson),
		},
	}

	var overrides *HelmChartConfig
	if config.Api.Helm != nil && config.Api.Helm.Tyger != nil {
		overrides = config.Api.Helm.Tyger
	}

	ajustSpec := func(cs *helmclient.ChartSpec, c helmclient.Client) error {
		cs.CreateNamespace = false
		return nil
	}

	if manifest, valuesYaml, err = installHelmChart(ctx, restConfig, &helmConfig, overrides, dryRun, ajustSpec); err != nil {
		return "", "", fmt.Errorf("failed to install Tyger Helm chart: %w", err)
	}

	return manifest, valuesYaml, nil
}

func getMigrationRunnerJobDefinition(ctx context.Context, restConfig *rest.Config) (*batchv1.Job, error) {
	dryRun := true
	manifest, _, err := InstallTygerHelmChart(ctx, restConfig, dryRun)
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
			if release != nil {
				manifest = release.Manifest
			}
			break
		}

		if strings.Contains(err.Error(), "the server could not find the requested resource") {
			// we get this transient error from time to time, related to the installation of CRDs
			log.Info().Err(err).Msg("Possible transient error. Will retry...")
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

func UninstallTyger(ctx context.Context) error {
	restConfig, err := GetUserRESTConfig(ctx)
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

	// Run pods
	err = clientset.CoreV1().Pods(TygerNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
		LabelSelector: tygerRunLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to delete pods: %w", err)
	}

	// Run jobs
	err = clientset.BatchV1().Jobs(TygerNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
		LabelSelector: tygerRunLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to delete jobs: %w", err)
	}
	// Run statefulsets
	err = clientset.AppsV1().StatefulSets(TygerNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
		LabelSelector: tygerRunLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to delete statefulsets: %w", err)
	}
	// Run secrets
	err = clientset.CoreV1().Secrets(TygerNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
		LabelSelector: tygerRunLabelSelector,
	})
	if err != nil {
		return fmt.Errorf("failed to delete secrets: %w", err)
	}

	// Run services. For some reason, there is no DeleteCollection method for services.
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

	// Migration jobs
	err = clientset.BatchV1().Jobs(TygerNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
		LabelSelector: migrationRunnerLabelKey,
	})
	if err != nil {
		return fmt.Errorf("failed to delete jobs: %w", err)
	}

	// Command host pods
	err = clientset.CoreV1().Pods(TygerNamespace).DeleteCollection(ctx, deleteOpts, metav1.ListOptions{
		LabelSelector: commandHostLabelKey,
	})
	if err != nil {
		return fmt.Errorf("failed to delete pods: %w", err)
	}

	return nil
}
