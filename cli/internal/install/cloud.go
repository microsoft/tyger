package install

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/fatih/color"
	"github.com/rs/zerolog/log"
)

const (
	LogsStorageContainerName = "runs"
)

var (
	ErrAlreadyLoggedError = errors.New("already logged error")
	errDependencyFailed   = errors.New("dependency failed")
)

func InstallCloud(ctx context.Context) (err error) {
	config := GetConfigFromContext(ctx)

	ensureResourceGroupCreated(ctx)

	allPromises := createPromises(ctx, config)
	for _, p := range allPromises {
		if promiseErr := p.AwaitErr(); promiseErr != nil && promiseErr != errDependencyFailed {
			logError(promiseErr, "")
			err = ErrAlreadyLoggedError
		}
	}

	return err
}

func ensureResourceGroupCreated(ctx context.Context) error {
	config := GetConfigFromContext(ctx)
	cred := GetAzureCredentialFromContext(ctx)

	c, err := armresources.NewResourceGroupsClient(config.Cloud.SubscriptionID, cred, nil)
	if err != nil {
		return fmt.Errorf("failed to create resource groups client: %w", err)
	}

	resp, err := c.CheckExistence(ctx, config.Cloud.ResourceGroup, nil)
	if err != nil {
		return fmt.Errorf("failed to check resource group existence: %w", err)
	}

	if resp.Success {
		return nil
	}

	log.Debug().Msgf("Creating resource group '%s'", config.Cloud.ResourceGroup)
	_, err = c.CreateOrUpdate(ctx, config.Cloud.ResourceGroup,
		armresources.ResourceGroup{
			Location: Ptr(config.Cloud.DefaultLocation),
		}, nil)

	if err != nil {
		return fmt.Errorf("failed to create resource group: %w", err)
	}

	return nil
}

func logError(err error, msg string) {
	errorString := err.Error()
	if strings.Contains(errorString, "\n") {
		if msg == "" {
			msg = "Encountered error:"
		}

		log.Error().Msg(msg)
		color.New(color.FgRed).FprintfFunc()(os.Stderr, "Error: %s", err.Error())
	} else {
		log.Error().Err(err).Msg(msg)
	}
}

func createPromises(ctx context.Context, config *EnvironmentConfig) PromiseGroup {
	group := &PromiseGroup{}

	var createApiHostClusterPromise *Promise[any]

	for _, clusterConfig := range config.Cloud.Compute.Clusters {
		createClusterPromise := NewPromise(
			ctx,
			group,
			func(ctx context.Context) (any, error) {
				return createCluster(ctx, clusterConfig)
			})
		if clusterConfig.ApiHost {
			createApiHostClusterPromise = createClusterPromise
		}
	}

	getAdminCredsPromise := NewPromiseAfter(ctx, group, getAdminRESTConfig, createApiHostClusterPromise)

	createTygerNamespacePromise := NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return createTygerNamespace(ctx, getAdminCredsPromise)
	})

	NewPromiseAfter(ctx, group, func(ctx context.Context) (any, error) {
		return CreateStorageAccount(ctx, config.Cloud.Storage.Logs, getAdminCredsPromise, LogsStorageContainerName)
	}, createTygerNamespacePromise)

	for _, buf := range config.Cloud.Storage.Buffers {
		NewPromiseAfter(ctx, group, func(ctx context.Context) (any, error) {
			return CreateStorageAccount(ctx, buf, getAdminCredsPromise)
		}, createTygerNamespacePromise)
	}

	NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return installTraefik(ctx, getAdminCredsPromise)
	})

	NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return installCertManager(ctx, getAdminCredsPromise)
	})

	NewPromise(ctx, group, func(ctx context.Context) (any, error) {
		return installNvidiaDevicePlugin(ctx, getAdminCredsPromise)
	})

	return *group
}

func GetSubscriptionId(ctx context.Context, subName string, cred *azidentity.DefaultAzureCredential) (string, error) {
	lowerSubName := strings.ToLower(subName)
	c, err := armsubscriptions.NewClient(cred, nil)
	if err != nil {
		return "", err
	}

	pager := c.NewListPager(nil)
	for subId := ""; pager.More() && subId == ""; {
		p, err := pager.NextPage(ctx)
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

func Ptr[T any](t T) *T {
	return &t
}
