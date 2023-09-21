package setup

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"sync/atomic"
	"syscall"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore/runtime"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/resources/armsubscriptions"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/storage/armstorage"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
	"k8s.io/client-go/rest"
)

const (
	LogsStorageContainerName = "runs"
)

var (
	ErrDependencyFailed = errors.New("dependency failed")
)

func SetupInfrastructure(config *Config, options *Options) {
	logHook := &LogHook{}
	log.Logger = log.Hook(logHook)

	log.Info().Msg("Starting setup")

	QuickValidateConfig(config)

	if logHook.HasError() {
		log.Fatal().Msg("Configuration validation failed.")
	}

	cred, err := azidentity.NewDefaultAzureCredential(nil)
	if err != nil {
		log.Fatal().Err(err).Msg("Failed to get credentials. Make sure the Azure CLI is installed and and you have run `az login`.")
	}

	ctx := context.Background()
	ctx = SetConfigOnContext(ctx, config)
	ctx = SetAzureCredentialOnContext(ctx, cred)
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

	// Get the subscription ID if we are given the name.
	if _, err := uuid.Parse(config.SubscriptionID); err != nil {
		config.SubscriptionID, err = getSubscriptionId(ctx, config.SubscriptionID, cred)
		if err != nil {
			log.Fatal().Err(err).Msg("Failed to get subscription ID.")
		}
	}

	allPromises := createPromises(ctx, config)

	for _, p := range allPromises {
		if err := p.AwaitErr(); err != nil && err != ErrDependencyFailed {
			log.Error().Err(err).Send()
		}
	}

	if logHook.HasError() {
		log.Fatal().Msg("Setup was not successful")
	} else {
		log.Info().Msg("Setup complete")
	}
}

func createPromises(ctx context.Context, config *Config) []UntypedPromise {
	allPromises := make([]UntypedPromise, 0)

	var getAdminCredsPromise *Promise[*rest.Config]
	var createTygerNamespacePromise *Promise[any]

	for _, clusterConfig := range config.Clusters {
		createClusterPromise := NewPromise(
			ctx,
			func(ctx context.Context) (any, error) {
				return createCluster(ctx, clusterConfig)
			})

		allPromises = append(allPromises, createClusterPromise)

		if clusterConfig.ControlPlane != nil {
			if getAdminCredsPromise != nil {
				panic("there can only be one control-plane cluster - this should have been caught by validation")
			}

			getAdminCredsPromise = NewPromiseAfter(ctx, getAdminRESTConfig, createClusterPromise)
			installTraefikPromise := NewPromise(ctx, func(ctx context.Context) (any, error) {
				return installTraefik(ctx, getAdminCredsPromise)
			})
			installCertManagerPromise := NewPromise(ctx, func(ctx context.Context) (any, error) {
				return installCertManager(ctx, getAdminCredsPromise)
			})
			installNvidiaDevicePluginPromise := NewPromise(ctx, func(ctx context.Context) (any, error) {
				return installNvidiaDevicePlugin(ctx, getAdminCredsPromise)
			})

			createTygerNamespacePromise = NewPromise(ctx, func(ctx context.Context) (any, error) {
				return createTygerNamespace(ctx, getAdminCredsPromise)
			})

			createLogStorageAccountPromise := NewPromiseAfter(ctx, func(ctx context.Context) (any, error) {
				return CreateStorageAccount(ctx, clusterConfig.ControlPlane.LogStorage, getAdminCredsPromise, LogsStorageContainerName)
			}, createTygerNamespacePromise)

			allPromises = append(
				allPromises,
				getAdminCredsPromise,
				installTraefikPromise,
				installCertManagerPromise,
				installNvidiaDevicePluginPromise,
				createLogStorageAccountPromise,
				createTygerNamespacePromise,
			)
		}
	}

	for _, buf := range config.Buffers {
		createBufferStorageAccountPromise := NewPromiseAfter(ctx, func(ctx context.Context) (any, error) {
			return CreateStorageAccount(ctx, buf, getAdminCredsPromise)
		}, createTygerNamespacePromise)
		allPromises = append(allPromises, createBufferStorageAccountPromise)
	}

	return allPromises
}

var (
	resourceNameRegex       = regexp.MustCompile(`^[a-z][a-z\-0-9]*$`)
	storageAccountNameRegex = regexp.MustCompile(`^[a-z0-9]{3,24}$`)
	dnsRegex                = regexp.MustCompile(`^[a-zA-Z]([a-zA-Z0-9\-]{0,61}[a-zA-Z0-9])?$`)
)

func QuickValidateConfig(config *Config) {
	log.Debug().Msg("Validating configuration")

	if config.SubscriptionID == "" {
		log.Error().Msg("The `subscriptionId` field is required")
	}

	if config.EnvironmentName == "" {
		log.Error().Msg("The `environmentName` field is required")
	} else if !resourceNameRegex.MatchString(config.EnvironmentName) {
		log.Error().Msg("The `environmentName` field must match the pattern " + resourceNameRegex.String())
	}

	if config.SubscriptionID == "" {
		log.Error().Msg("The `subscriptionId` field is required")
	}

	if config.Location == "" {
		log.Error().Msg("The `location` field is required")
	}

	if len(config.Clusters) == 0 {
		log.Error().Msg("At least one cluster must be specified")
	}

	hasControlPlane := false
	clusterNames := make(map[string]any)
	for i, cluster := range config.Clusters {
		if cluster.Name == "" {
			log.Error().Msg("The `name` field is required on a cluster")
		} else if !resourceNameRegex.MatchString(cluster.Name) {
			log.Error().Msg("The cluster `name` field must match the pattern " + resourceNameRegex.String())
		} else {
			if _, ok := clusterNames[cluster.Name]; ok {
				log.Error().Msg("Cluster names must be unique")
			}
			clusterNames[cluster.Name] = nil
		}

		if cluster.Location == "" {
			cluster.Location = config.Location
		}

		if len(cluster.UserNodePools) == 0 {
			log.Error().Msg("At least one user node pool must be specified")
		}
		for _, np := range cluster.UserNodePools {
			if np.Name == "" {
				log.Error().Msg("The `name` field is required on a node pool")
			} else if !resourceNameRegex.MatchString(np.Name) {
				log.Error().Msg("The node pool `name` field must match the pattern " + resourceNameRegex.String())
			}

			if np.VMSize == "" {
				log.Error().Msg("The `vmSize` field is required on a node pool")
			}

			if np.MinCount < 0 {
				log.Error().Msg("The `minCount` field must be greater than or equal to zero")
			}

			if np.MaxCount < 0 {
				log.Error().Msg("The `maxCount` field must be greater than or equal to zero")
			}

			if np.Count < 0 {
				log.Error().Msg("The `count` field must be greater than or equal to zero")
			}

			if np.MinCount > np.MaxCount {
				log.Error().Msg("The `minCount` field must be less than or equal to the `maxCount` field")
			}

			if np.Count < np.MinCount || np.Count > np.MaxCount {
				log.Error().Msg("The `count` field must be between the `minCount` and `maxCount` fields")
			}
		}

		if cluster.ControlPlane != nil {
			if hasControlPlane {
				log.Error().Msg("Only one cluster can be a control plane")
			} else {
				hasControlPlane = true

				if cluster.ControlPlane.LogStorage == nil {
					log.Error().Msg("The `logStorage` field is required on a control plane cluster")
				} else {
					quickValidateStorageAccountConfig(config, fmt.Sprintf("clusters[%d].controlPlane.logStorage", i), cluster.ControlPlane.LogStorage)
				}

				if cluster.ControlPlane.DnsLabel == "" {
					log.Error().Msg("The `dnsLabel` field is required on a control plane")
				} else if !dnsRegex.MatchString(cluster.ControlPlane.DnsLabel) {
					log.Error().Msg("The control plane `dnsLabel` field must match the pattern " + dnsRegex.String())
				}
			}
		}
	}

	if !hasControlPlane {
		log.Error().Msg("One cluster must have a control plane")
	}

	if len(config.Buffers) == 0 {
		log.Error().Msg("At least one buffer storage account is required")
	}

	if len(config.Buffers) > 1 {
		log.Error().Msg("Only one buffer storage account is currently supported")
	}

	for i, buf := range config.Buffers {
		quickValidateStorageAccountConfig(config, fmt.Sprintf("buffers[%d]", i), buf)
	}
}

func quickValidateStorageAccountConfig(config *Config, path string, storageConfig *StorageAccountConfig) {
	if storageConfig.Name == "" {
		log.Error().Msgf("The `%s.name` field is required", path)
	} else if !storageAccountNameRegex.MatchString(storageConfig.Name) {
		log.Error().Msg("The `%s.name` field must match the pattern " + storageAccountNameRegex.String())
	}

	if storageConfig.Location == "" {
		storageConfig.Location = config.Location
	}

	if storageConfig.Sku == "" {
		storageConfig.Sku = string(armstorage.SKUNameStandardLRS)
	} else {
		match := false
		for _, at := range armstorage.PossibleSKUNameValues() {
			if storageConfig.Sku == string(at) {
				match = true
				break
			}
		}
		if !match {
			log.Error().Msgf("The `%s.sku` field must be one of %v", path, armstorage.PossibleSKUNameValues())
		}
	}
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

func GetConfigFromContext(ctx context.Context) *Config {
	return ctx.Value(configKey).(*Config)
}

func SetConfigOnContext(ctx context.Context, config *Config) context.Context {
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
