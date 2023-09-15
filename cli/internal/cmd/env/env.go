package env

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/authorization/armauthorization"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/containerservice/armcontainerservice/v4"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"helm.sh/helm/v3/pkg/repo"
	rbacv1 "k8s.io/api/rbac/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/client-go/kubernetes"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/clientcmd"

	helmclient "github.com/mittwald/go-helm-client"
)

func NewEnvCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:                   "env",
		Aliases:               []string{"env"},
		Short:                 "Manage environments",
		Long:                  `Manage environments`,
		DisableFlagsInUseLine: true,
		Args:                  cobra.NoArgs,
		Run: func(*cobra.Command, []string) {

			cred, err := azidentity.NewDefaultAzureCredential(nil)
			if err != nil {
				log.Fatal().Err(err).Msg("failed to get credential")
			}

			subName := "biomedicalimaging-nonprod"
			environmentName := "js"

			subId, err := getSubscriptionId(subName, cred)
			if err != nil {
				log.Fatal().Err(err).Msg("failed to get subscription")
			}

			clustersClient, err := armcontainerservice.NewManagedClustersClient(subId, cred, nil)
			if err != nil {
				log.Fatal().Err(err).Msg("failed to get clusters client")
			}

			// containerRegistries := []string{"eminence"}

			// for i, name := range containerRegistries {
			// 	containerRegistries[i], err = getContainerRegistry(name, subId, cred)
			// 	if err != nil {
			// 		log.Fatal().Err(err).Msg("failed to get container registry")
			// 	}
			// }

			// for _, acr := range containerRegistries {
			// 	attachAcr("19ff3c33-d99b-44be-98fb-3402c77f98b1", acr, subId, cred)
			// }

			// clusterPoller, err := createCluster(subId, cred, "js", containerRegistries)
			// if err != nil {
			// 	log.Fatal().Err(err).Msg("failed to create cluster")
			// }

			// clusterResp, err := clusterPoller.PollUntilDone(context.Background(), nil)
			// if err != nil {
			// 	log.Fatal().Err(err).Msg("failed to create cluster")
			// }

			adminRESTConfig, err := getAdminRESTConfig(clustersClient, environmentName)
			if err != nil {
				log.Fatal().Err(err).Msg("failed to get admin client set")
			}
			adminClientset := kubernetes.NewForConfigOrDie(adminRESTConfig)

			err = createClusterRbac(adminClientset)
			if err != nil {
				log.Fatal().Err(err).Msg("failed to create cluster rbac")
			}

			err = addTraefik(adminRESTConfig, adminClientset, environmentName+"-tyger")
			if err != nil {
				log.Fatal().Err(err).Msg("failed to add traefik")
			}
		},
	}

	return cmd
}

func addTraefik(adminRESTConfig *rest.Config, adminClientset *kubernetes.Clientset, dnsLabel string) error {
	helmOptions := helmclient.RestConfClientOptions{
		RestConfig: adminRESTConfig,
		Options:    &helmclient.Options{},
	}
	helmClient, err := helmclient.NewClientFromRestConf(&helmOptions)
	if err != nil {
		return fmt.Errorf("failed to create helm client: %w", err)
	}

	log.Info().Msg("installing traefik")

	err = helmClient.AddOrUpdateChartRepo(repo.Entry{Name: "traefik", URL: "https://helm.traefik.io/traefik"})
	if err != nil {
		return fmt.Errorf("failed to add traefik repo: %w", err)
	}

	values := fmt.Sprintf(`
logs:
  general:
    format: "json"
  access:
    enabled: "true"
    format: "json"
service:
  annotations: # We need to add the azure dns label, otherwise the public IP will not have a DNS name, which we need for cname record later.
    "service.beta.kubernetes.io/azure-dns-label-name": "%s"
`, dnsLabel)

	chartSpec := helmclient.ChartSpec{
		ReleaseName:     "traefik",
		ChartName:       "traefik/traefik",
		Namespace:       "traefik",
		CreateNamespace: true,
		ValuesYaml:      values,
	}

	if _, err := helmClient.InstallOrUpgradeChart(context.Background(), &chartSpec, nil); err != nil {
		return fmt.Errorf("failed to install traefik: %w", err)
	}

	log.Info().Msg("installing cert-manager")

	err = helmClient.AddOrUpdateChartRepo(repo.Entry{Name: "jetstack", URL: "https://charts.jetstack.io"})
	if err != nil {
		return fmt.Errorf("failed to add jetstack repo: %w", err)
	}

	values = "installCRDs: true"
	chartSpec = helmclient.ChartSpec{
		ReleaseName:     "cert-manager",
		ChartName:       "jetstack/cert-manager",
		Namespace:       "cert-manager",
		CreateNamespace: true,
		ValuesYaml:      values,
	}

	if _, err := helmClient.InstallOrUpgradeChart(context.Background(), &chartSpec, nil); err != nil {
		return fmt.Errorf("failed to install cert-manager: %w", err)
	}

	return nil
}

func createClusterRbac(clientset *kubernetes.Clientset) error {
	role := rbacv1.ClusterRole{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tyger-cluster-user-role",
		},
		Rules: []rbacv1.PolicyRule{
			{
				APIGroups: []string{"", "extensions", "apps"},
				Resources: []string{"*"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{"batch"},
				Resources: []string{"jobs", "cronjobs"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{""},
				Resources: []string{"secrets"},
				Verbs:     []string{"create", "delete", "deletecollection", "list"},
			},
			{
				APIGroups: []string{"rbac.authorization.k8s.io"},
				Resources: []string{"roles", "rolebindings"},
				Verbs:     []string{"*"},
			},
			{
				APIGroups: []string{"traefik.containo.us"},
				Resources: []string{"ingressroutes"},
				Verbs:     []string{"*"},
			},
		},
	}

	if _, err := clientset.RbacV1().ClusterRoles().Create(context.TODO(), &role, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err = clientset.RbacV1().ClusterRoles().Update(context.TODO(), &role, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to update cluster role: %w", err)
			}
		} else {
			return fmt.Errorf("failed to create cluster role: %w", err)
		}
	}

	roleBinding := rbacv1.ClusterRoleBinding{
		ObjectMeta: metav1.ObjectMeta{
			Name: "tyger-cluster-user-rolebinding",
		},
		RoleRef: rbacv1.RoleRef{
			APIGroup: "rbac.authorization.k8s.io",
			Kind:     "ClusterRole",
			Name:     role.Name,
		},
		Subjects: []rbacv1.Subject{
			{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "Group",
				Name:     "c0e60aba-35f0-4778-bc9b-fc5d2af14687",
			},
			{
				APIGroup: "rbac.authorization.k8s.io",
				Kind:     "User",
				Name:     "5b60f594-a0eb-410c-a3fc-dd3c6f4e28d1",
			},
		},
	}

	if _, err := clientset.RbacV1().ClusterRoleBindings().Create(context.TODO(), &roleBinding, metav1.CreateOptions{}); err != nil {
		if apierrors.IsAlreadyExists(err) {
			_, err = clientset.RbacV1().ClusterRoleBindings().Update(context.TODO(), &roleBinding, metav1.UpdateOptions{})
			if err != nil {
				return fmt.Errorf("failed to update cluster role binding: %w", err)
			}
		} else {
			return fmt.Errorf("failed to create cluster role binding: %w", err)
		}
	}

	return nil
}

func createCluster(mcc *armcontainerservice.ManagedClustersClient, subscriptionId string, credential azcore.TokenCredential, environmentName string, attachedContainerRegistries []string) (*runtime.Poller[armcontainerservice.ManagedClustersClientCreateOrUpdateResponse], error) {
	location := "westus2"

	mc := armcontainerservice.ManagedCluster{
		Location: Ptr(location),
		Identity: &armcontainerservice.ManagedClusterIdentity{
			Type: Ptr(armcontainerservice.ResourceIdentityTypeSystemAssigned),
		},
		Properties: &armcontainerservice.ManagedClusterProperties{
			AgentPoolProfiles: []*armcontainerservice.ManagedClusterAgentPoolProfile{
				{
					Name:              Ptr("system"),
					Mode:              Ptr(armcontainerservice.AgentPoolModeSystem),
					VMSize:            Ptr("Standard_DS2_v2"),
					EnableAutoScaling: Ptr(true),
					Count:             Ptr(int32(1)),
					MinCount:          Ptr(int32(1)),
					MaxCount:          Ptr(int32(3)),
				},
				{
					Name:              Ptr("cpunp"),
					Mode:              Ptr(armcontainerservice.AgentPoolModeUser),
					VMSize:            Ptr("Standard_DS2_v2"),
					EnableAutoScaling: Ptr(true),
					Count:             Ptr(int32(0)),
					MinCount:          Ptr(int32(0)),
					MaxCount:          Ptr(int32(10)),
					NodeLabels: map[string]*string{
						"tyger": Ptr("run"),
					},
					NodeTaints: []*string{
						Ptr("tyger=run:NoSchedule"),
						Ptr("sku=gpu:NoSchedule"),
					},
				},
				{
					Name:              Ptr("gpunp"),
					Mode:              Ptr(armcontainerservice.AgentPoolModeUser),
					VMSize:            Ptr("Standard_DS2_v2"),
					EnableAutoScaling: Ptr(true),
					Count:             Ptr(int32(0)),
					MinCount:          Ptr(int32(0)),
					MaxCount:          Ptr(int32(10)),
					NodeLabels: map[string]*string{
						"tyger": Ptr("run"),
					},
					NodeTaints: []*string{
						Ptr("tyger=run:NoSchedule"),
					},
				},
			},
			AddonProfiles: map[string]*armcontainerservice.ManagedClusterAddonProfile{
				"omsagent": {
					Enabled: Ptr(true),
					Config: map[string]*string{
						"logAnalyticsWorkspaceResourceID": Ptr("/subscriptions/87d8acb3-5176-4651-b457-6ab9cefd8e3d/resourceGroups/eminence/providers/Microsoft.OperationalInsights/workspaces/eminence"),
						"useAADAuth":                      Ptr("true"),
					},
				},
			},
			DNSPrefix:                Ptr(getClusterDnsPrefix(environmentName, subscriptionId)),
			CurrentKubernetesVersion: Ptr("1.25.6"),
			EnableRBAC:               Ptr(true),
			AADProfile: &armcontainerservice.ManagedClusterAADProfile{
				Managed:         Ptr(true),
				EnableAzureRBAC: Ptr(false),
			},
		},
	}

	poller, err := mcc.BeginCreateOrUpdate(context.TODO(), environmentName, environmentName, mc, nil)
	if err != nil {
		return nil, err
	}

	var kubeletObjectId string
	for {
		time.Sleep(10 * time.Second)
		getResp, err := mcc.Get(context.TODO(), environmentName, environmentName, nil)
		if err != nil {
			return nil, err
		}

		if getResp.Properties.IdentityProfile != nil {
			if kubeletIdentity := getResp.Properties.IdentityProfile["kubeletidentity"]; kubeletIdentity != nil {
				kubeletObjectId = *kubeletIdentity.ObjectID
				break
			}
		}
	}

	for _, containerRegistry := range attachedContainerRegistries {
		attachAcr(kubeletObjectId, containerRegistry, subscriptionId, credential)
	}

	return poller, nil
}

func attachAcr(kubeletObjectId, containerRegistry, subscriptionId string, credential azcore.TokenCredential) error {
	roleDefClient, err := armauthorization.NewRoleDefinitionsClient(credential, nil)
	if err != nil {
		return err
	}

	pager := roleDefClient.NewListPager(containerRegistry, &armauthorization.RoleDefinitionsClientListOptions{Filter: Ptr("rolename eq 'acrpull")})

	var acrPullRoleId string
	for pager.More() && acrPullRoleId == "" {
		page, err := pager.NextPage(context.TODO())
		if err != nil {
			return err
		}

		for _, rd := range page.Value {
			acrPullRoleId = *rd.ID
			break
		}
	}

	if acrPullRoleId == "" {
		return fmt.Errorf("unable to find 'AcrPull' role")
	}

	roleAssignmentClient, err := armauthorization.NewRoleAssignmentsClient(subscriptionId, credential, nil)
	if err != nil {
		return err
	}

	_, err = roleAssignmentClient.Create(
		context.TODO(),
		containerRegistry,
		uuid.New().String(),
		armauthorization.RoleAssignmentCreateParameters{
			Properties: &armauthorization.RoleAssignmentProperties{
				RoleDefinitionID: Ptr(acrPullRoleId),
				PrincipalID:      Ptr(kubeletObjectId),
			},
		}, nil)

	return err
}

func getSubscriptionId(subName string, cred *azidentity.DefaultAzureCredential) (string, error) {
	lowerSubName := strings.ToLower(subName)
	c, err := armsubscriptions.NewClient(cred, nil)
	if err != nil {
		return "", err
	}

	pager := c.NewListPager(nil)
	for subId := ""; pager.More() && subId == ""; {
		p, err := pager.NextPage(context.TODO())
		if err != nil {
			return "", err
		}
		for _, s := range p.Value {
			if strings.ToLower(*s.DisplayName) == lowerSubName {
				return *s.SubscriptionID, nil
			}
		}
	}

	return "", fmt.Errorf("subscription with name '%s' not found", subName)
}

func getContainerRegistry(name string, subscriptionId string, credential azcore.TokenCredential) (string, error) {
	resourceClient, err := armresources.NewClient(subscriptionId, credential, nil)
	if err != nil {
		return "", err
	}
	pager := resourceClient.NewListPager(&armresources.ClientListOptions{
		Filter: Ptr(fmt.Sprintf("resourceType eq 'Microsoft.ContainerRegistry/registries' and name eq '%s'", "eminence")),
	})

	for pager.More() {
		p, err := pager.NextPage(context.TODO())
		if err != nil {
			return "", err
		}
		for _, s := range p.Value {
			return *s.ID, nil
		}
	}

	return "", fmt.Errorf("container registry '%s' not found in subscription", name)
}

func getAdminRESTConfig(mcc *armcontainerservice.ManagedClustersClient, environmentName string) (*rest.Config, error) {
	credResp, err := mcc.ListClusterAdminCredentials(context.TODO(), environmentName, environmentName, nil)
	if err != nil {
		return nil, err
	}

	return clientcmd.RESTConfigFromKubeConfig(credResp.Kubeconfigs[0].Value)
}

func getUserRESTConfig(mcc *armcontainerservice.ManagedClustersClient, environmentName string) (*rest.Config, error) {
	credResp, err := mcc.ListClusterUserCredentials(context.TODO(), environmentName, environmentName, nil)
	if err != nil {
		return nil, err
	}

	tempKubeconfig, err := os.CreateTemp("", "kubeconfsig")
	if err != nil {
		return nil, err
	}
	defer os.Remove(tempKubeconfig.Name())

	if err := os.WriteFile(tempKubeconfig.Name(), []byte(credResp.Kubeconfigs[0].Value), 0600); err != nil {
		return nil, err
	}

	if err := exec.Command("kubelogin", "convert-kubeconfig", "--login", "azurecli", "--kubeconfig", tempKubeconfig.Name()).Run(); err != nil {
		return nil, err
	}

	kubeconfig, err := os.ReadFile(tempKubeconfig.Name())
	if err != nil {
		return nil, err
	}

	return clientcmd.RESTConfigFromKubeConfig(kubeconfig)
}

func getClusterDnsPrefix(environmentName, subId string) string {
	return fmt.Sprintf("%s-tyger-%s", regexp.MustCompile("[^a-zA-Z0-9-]").ReplaceAllString(environmentName, ""), subId[0:8])
}

func Ptr[T any](t T) *T {
	return &t
}
