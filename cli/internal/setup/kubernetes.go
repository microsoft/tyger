package setup

import (
	"bytes"
	"context"
	"fmt"
	"sort"
	"text/template"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice"
	helmclient "github.com/mittwald/go-helm-client"
	"github.com/rs/zerolog/log"
	"helm.sh/helm/v3/pkg/repo"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"
)

const (
	TygerNamespace = "tyger"
)

func getAdminRESTConfig(ctx context.Context) (*rest.Config, error) {
	config := GetConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	clustersClient, err := armcontainerservice.NewManagedClustersClient(config.SubscriptionID, cred, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create clusters client: %w", err)
	}

	credResp, err := clustersClient.ListClusterAdminCredentials(ctx, config.EnvironmentName, config.GetControlPlaneCluster().Name, nil)
	if err != nil {
		return nil, err
	}

	return clientcmd.RESTConfigFromKubeConfig(credResp.Kubeconfigs[0].Value)
}

func installTraefik(ctx context.Context, restConfigPromise *Promise[*rest.Config]) (any, error) {
	config := GetConfigFromContext(ctx)

	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, err
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
	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create helm client: %w", err)
	}

	err = helmClient.AddOrUpdateChartRepo(repo.Entry{Name: "traefik", URL: "https://helm.traefik.io/traefik"})
	if err != nil {
		return nil, fmt.Errorf("failed to add traefik repo: %w", err)
	}

	valuesTemplateText := `
logs:
  general:
    format: "json"
  access:
    enabled: "true"
    format: "json"
service:
  annotations: # We need to add the azure dns label, otherwise the public IP will not have a DNS name, which we need for cname record later.
    "service.beta.kubernetes.io/azure-dns-label-name": "{{.DnsLabel}}"
`
	valueParams := struct{ DnsLabel string }{config.GetControlPlaneCluster().ControlPlane.DnsLabel}

	valuesTemplate := template.Must(template.New("values").Parse(valuesTemplateText))

	var values bytes.Buffer
	err = valuesTemplate.Execute(&values, valueParams)
	if err != nil {
		return nil, fmt.Errorf("failed to execute values template: %w", err)
	}

	chartSpec := helmclient.ChartSpec{
		ReleaseName:     namespace,
		ChartName:       "traefik/traefik",
		Namespace:       namespace,
		CreateNamespace: true,
		Wait:            true,
		WaitForJobs:     true,
		Atomic:          true,
		UpgradeCRDs:     true,
		Timeout:         2 * time.Minute,
		ValuesYaml:      values.String(),
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
	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return nil, err
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
	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return nil, fmt.Errorf("failed to create helm client: %w", err)
	}

	err = helmClient.AddOrUpdateChartRepo(repo.Entry{Name: "jetstack", URL: "https://charts.jetstack.io"})
	if err != nil {
		return nil, fmt.Errorf("failed to add jetstack repo: %w", err)
	}

	certManagerValues := "installCRDs: true"

	chartSpec := helmclient.ChartSpec{
		ReleaseName:     namespace,
		ChartName:       "jetstack/cert-manager",
		Namespace:       namespace,
		CreateNamespace: true,
		Wait:            true,
		WaitForJobs:     true,
		Atomic:          true,
		UpgradeCRDs:     true,
		Timeout:         5 * time.Minute,
		ValuesYaml:      certManagerValues,
	}

	if _, err := helmClient.InstallOrUpgradeChart(context.Background(), &chartSpec, nil); err != nil {
		return nil, fmt.Errorf("failed to install cert-manager: %w", err)
	}

	return nil, nil
}
