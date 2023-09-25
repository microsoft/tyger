package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armresources"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/fatih/color"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

const (
	LogsStorageContainerName = "runs"
)

var (
	ErrDependencyFailed = errors.New("dependency failed")
)

func SetupInfrastructure(config *EnvironmentConfig, options *Options) {
	logHook := &LogHook{}
	log.Logger = log.Hook(logHook)

	ctx := context.Background()
	ctx = SetConfigOnContext(ctx, config)

	ctx = SetSetupOptionsOnContext(ctx, options)
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()

	// Set up channel on which to send signal notifications.
	cSignal := make(chan os.Signal, 2)
	signal.Notify(cSignal, os.Interrupt, syscall.SIGTERM)
	go func() {
		<-cSignal
		log.Warn().Msg("Cancelling...")
		cancel()
	}()

	log.Info().Msg("Starting setup")

	quickValidateEnvironmentConfig(config)

	if logHook.HasError() {
		os.Exit(1)
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		log.Error().Err(err).Msg("Failed to get credentials. Make sure the Azure CLI is installed and and you have run `az login`.")
		os.Exit(1)
	}

	ctx = SetAzureCredentialOnContext(ctx, cred)

	// Get the subscription ID if we are given the name.
	if _, err := uuid.Parse(config.Cloud.SubscriptionID); err != nil {
		config.Cloud.SubscriptionID, err = getSubscriptionId(ctx, config.Cloud.SubscriptionID, cred)
		if err != nil {
			log.Error().Err(err).Msg("Failed to get subscription ID.")
			os.Exit(1)
		}
	}

	ensureResourceGroupCreated(ctx)

	allPromises := createPromises(ctx, config)
	for _, p := range allPromises {
		if err := p.AwaitErr(); err != nil && err != ErrDependencyFailed {
			logError(err, "")
		}
	}

	if logHook.HasError() {
		os.Exit(1)
	}

	log.Info().Msg("Setup complete")
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

func getSubscriptionId(ctx context.Context, subName string, cred *azidentity.DefaultAzureCredential) (string, error) {
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

type LogHook struct {
	hasError atomic.Bool
}

func (h *LogHook) HasError() bool {
	return h.hasError.Load()
}

func (h *LogHook) Run(e *zerolog.Event, level zerolog.Level, msg string) {
	if int(level) >= int(zerolog.ErrorLevel) {
		h.hasError.Store(true)
	}
}

func Ptr[T any](t T) *T {
	return &t
}

type configContextKeyType int

const (
	configKey          configContextKeyType = 0
	azureCredentialKey configContextKeyType = 1
	setupOptionsKey    configContextKeyType = 2
)

func GetConfigFromContext(ctx context.Context) *EnvironmentConfig {
	return ctx.Value(configKey).(*EnvironmentConfig)
}

func SetConfigOnContext(ctx context.Context, config *EnvironmentConfig) context.Context {
	return context.WithValue(ctx, configKey, config)
}

func GetAzureCredentialFromContext(ctx context.Context) *azidentity.DefaultAzureCredential {
	return ctx.Value(azureCredentialKey).(*azidentity.DefaultAzureCredential)
}

func SetAzureCredentialOnContext(ctx context.Context, cred *azidentity.DefaultAzureCredential) context.Context {
	return context.WithValue(ctx, azureCredentialKey, cred)
}

func GetSetupOptionsFromContext(ctx context.Context) *Options {
	return ctx.Value(setupOptionsKey).(*Options)
}

func SetSetupOptionsOnContext(ctx context.Context, options *Options) context.Context {
	return context.WithValue(ctx, setupOptionsKey, options)
}

func WaitForPoller[T any](ctx context.Context, promise *Promise[*runtime.Poller[T]]) (T, error) {
	poller, err := promise.Await()
	if err != nil {
		var t T
		return t, ErrDependencyFailed
	}

	return poller.PollUntilDone(ctx, nil)
}
