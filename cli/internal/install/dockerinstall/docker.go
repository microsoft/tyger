// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dockerinstall

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/filters"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/docker/docker/pkg/stdcopy"
	"github.com/docker/go-connections/nat"
	tygerclient "github.com/microsoft/tyger/cli/internal/client"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/psanford/memfs"
	"github.com/rs/zerolog/log"
	"k8s.io/apimachinery/pkg/util/version"
)

const (
	containerSpecHashLabel = "tyger-container-spec-hash"

	databaseContainerSuffix     = "db"
	dataPlaneContainerSuffix    = "data-plane"
	controlPlaneContainerSuffix = "control-plane"
	gatewayContainerSuffix      = "gateway"

	databaseVolumeSuffix = "db"
	buffersVolumeSuffix  = "buffers"
	runLogsVolumeSuffix  = "run-logs"

	dockerSocketPath = "/var/run/docker.sock"
)

type containerSpec struct {
	ContainerConfig  *container.Config         `json:"containerConfig"`
	HostConfig       *container.HostConfig     `json:"hostConfig"`
	NetworkingConfig *network.NetworkingConfig `json:"networkingConfig"`
}

func (s *containerSpec) computeHash() string {
	desiredBytes, err := json.Marshal(s)
	if err != nil {
		panic(err)
	}

	hashBytes := sha256.Sum256(desiredBytes)
	return base32.StdEncoding.EncodeToString(hashBytes[:])
}

type Installer struct {
	Config               *DockerEnvironmentConfig
	client               *client.Client
	hostPathTranslations map[string]string
}

func NewInstaller(config *DockerEnvironmentConfig) (*Installer, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("error creating docker client: %w", err)
	}

	hostPathTranslations := map[string]string{}
	translationsEnvVar := os.Getenv("TYGER_DOCKER_PATH_TRANSLATIONS")
	if translationsEnvVar != "" {
		for _, spec := range strings.Split(translationsEnvVar, ":") {
			tokens := strings.Split(spec, "=")
			if len(tokens) == 2 {
				source := tokens[0]
				if !strings.HasSuffix(source, "/") {
					source += "/"
				}

				dest := tokens[1]
				if !strings.HasSuffix(dest, "/") {
					dest += "/"
				}

				hostPathTranslations[source] = dest
			}
		}
	}

	return &Installer{
		Config:               config,
		client:               dockerClient,
		hostPathTranslations: hostPathTranslations,
	}, nil
}

func (inst *Installer) resourceName(suffix string) string {
	return fmt.Sprintf("tyger-%s-%s", inst.Config.EnvironmentName, suffix)
}

func (inst *Installer) translateToHostPath(path string) string {
	for source, dest := range inst.hostPathTranslations {
		if strings.HasPrefix(path, source) {
			return dest + strings.TrimPrefix(path, source)
		}

		if len(path)+1 == len(source) && path == source[:len(source)-1] {
			return dest[:len(dest)-1]
		}
	}

	return path
}

func (inst *Installer) InstallTyger(ctx context.Context) error {
	if runtime.GOOS == "windows" {
		log.Error().Msg("Installing Tyger in Docker on Windows must be done from a WSL shell. Once installed, other commands can be run from within Windows.")
		return install.ErrAlreadyLoggedError
	}

	if os.Getenv("WSL_DISTRO_NAME") != "" {
		// Check Windows Docker Desktop version for compatibility
		versionInfo, err := exec.Command("docker.exe", "version").Output()
		if err == nil {
			re := regexp.MustCompile(`Server: Docker Desktop (\d+\.\d+(\.\d+)?)`)
			matches := re.FindStringSubmatch(string(versionInfo))
			if len(matches) > 1 {
				if dockerDesktopVersion, err := version.ParseGeneric(matches[1]); err == nil {
					// versions  in range [4.28, 4.31) are known to not work
					warnIfLessThan := version.MustParseGeneric("4.28.0")
					minimumKnownGoodVersion := version.MustParseGeneric("4.31.0")
					if dockerDesktopVersion.LessThan(warnIfLessThan) {
						log.Warn().Msgf("Tyger may not be compatible with this version of Docker Desktop. You may need to upgrade to version %v or later.", minimumKnownGoodVersion)
					} else if dockerDesktopVersion.LessThan(minimumKnownGoodVersion) {
						log.Error().Msgf("Tyger is not compatible with Docker Desktop version %v. Please upgrade to version %v or later.", dockerDesktopVersion, minimumKnownGoodVersion)
						return install.ErrAlreadyLoggedError
					}
				}
			}
		}
	}

	if err := inst.ensureDirectoryExists(inst.Config.InstallationPath); err != nil {
		return err
	}

	pg := &install.PromiseGroup{}

	install.NewPromise(ctx, pg, func(ctx context.Context) (any, error) {
		err := inst.createNetwork(ctx)
		return nil, err
	})

	dbPromise := install.NewPromise(ctx, pg, func(ctx context.Context) (any, error) {
		if err := inst.createDatabaseContainer(ctx); err != nil {
			return nil, fmt.Errorf("error creating database container: %w", err)
		}
		return nil, nil
	})

	dataPlanePromise := install.NewPromise(ctx, pg, func(ctx context.Context) (any, error) {
		if err := inst.createDataPlaneContainer(ctx); err != nil {
			return nil, fmt.Errorf("error creating data plane container: %w", err)
		}
		return nil, nil
	})

	migrationRunnerPromise := install.NewPromiseAfter(ctx, pg, func(ctx context.Context) (any, error) {
		if err := inst.initializeDatabase(ctx); err != nil {
			return nil, fmt.Errorf("error initializing database: %w", err)
		}
		return nil, nil
	}, dbPromise)

	checkGpuPromise := install.NewPromise(ctx, pg, inst.checkGpuAvailability)

	install.NewPromiseAfter(ctx, pg, func(ctx context.Context) (any, error) {
		if err := inst.createControlPlaneContainer(ctx, checkGpuPromise); err != nil {
			return nil, fmt.Errorf("error creating control plane container: %w", err)
		}
		return nil, nil
	}, migrationRunnerPromise, dataPlanePromise)

	if inst.Config.UseGateway != nil && *inst.Config.UseGateway {
		install.NewPromise(ctx, pg, func(ctx context.Context) (any, error) {
			if err := inst.createGatewayContainer(ctx); err != nil {
				return nil, fmt.Errorf("error creating gateway container: %w", err)
			}
			return nil, nil
		})
	}

	for _, p := range *pg {
		if promiseErr := p.AwaitErr(); promiseErr != nil && promiseErr != install.ErrDependencyFailed {
			return promiseErr
		}
	}

	return nil
}

func (inst *Installer) ensureDirectoryExists(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("error creating directory %s: %w", path, err)
		}

		return os.Chown(path, inst.Config.GetUserIdInt(), inst.Config.GetGroupIdInt())
	} else {
		return err
	}
}

func (inst *Installer) createControlPlaneContainer(ctx context.Context, checkGpuPromise *install.Promise[bool]) error {
	if err := inst.ensureVolumeCreated(ctx, inst.resourceName(runLogsVolumeSuffix)); err != nil {
		return err
	}

	if err := inst.ensureDirectoryExists(fmt.Sprintf("%s/control-plane", inst.Config.InstallationPath)); err != nil {
		return err
	}

	if err := inst.ensureDirectoryExists(fmt.Sprintf("%s/ephemeral", inst.Config.InstallationPath)); err != nil {
		return err
	}

	if err := inst.pullImage(ctx, inst.Config.BufferSidecarImage, false); err != nil {
		return fmt.Errorf("error pulling buffer sidecar image: %w", err)
	}

	image := inst.Config.ControlPlaneImage

	primaryPublicKeyHash, err := fileHash(inst.Config.SigningKeys.Primary.PublicKey)
	if err != nil {
		return fmt.Errorf("error hashing primary public key: %w", err)
	}

	primaryPublicCertificatePath := fmt.Sprintf("/app/tyger-data-plane-primary-%s.pem", primaryPublicKeyHash)
	secondaryPublicCertificatePath := ""
	if inst.Config.SigningKeys.Secondary != nil {
		secondaryPublicKeyHash, err := fileHash(inst.Config.SigningKeys.Secondary.PublicKey)
		if err != nil {
			return fmt.Errorf("error hashing secondary public key: %w", err)
		}

		secondaryPublicCertificatePath = fmt.Sprintf("/app/tyger-data-plane-secondary-%s.pem", secondaryPublicKeyHash)
	}

	gpuAvailable, err := checkGpuPromise.Await()
	if err != nil {
		return install.ErrDependencyFailed
	}

	if !gpuAvailable {
		log.Warn().Msg("GPU support is not available.")
	}

	hostInstallationPath := inst.translateToHostPath(inst.Config.InstallationPath)
	containerSpec := containerSpec{
		ContainerConfig: &container.Config{
			Image: image,
			User:  fmt.Sprintf("%d:%d", inst.Config.GetUserIdInt(), inst.Config.GetGroupIdInt()),
			Env: []string{
				fmt.Sprintf("Urls=http://unix:%s/control-plane/tyger.sock", inst.Config.InstallationPath),
				"SocketPermissions=660",
				"Auth__Enabled=false",
				fmt.Sprintf("Compute__Docker__RunSecretsPath=%s/control-plane/run-secrets", inst.Config.InstallationPath),
				fmt.Sprintf("Compute__Docker__EphemeralBuffersPath=%s/ephemeral", inst.Config.InstallationPath),
				fmt.Sprintf("Compute__Docker__GpuSupport=%t", gpuAvailable),
				fmt.Sprintf("Compute__Docker__NetworkName=%s", inst.resourceName("network")),
				"LogArchive__LocalStorage__LogsDirectory=/app/logs",
				"Buffers__BufferSidecarImage=" + inst.Config.BufferSidecarImage,
				fmt.Sprintf("Buffers__LocalStorage__DataPlaneEndpoint=http+unix://%s/data-plane/tyger.data.sock", inst.Config.InstallationPath),
				fmt.Sprintf("Buffers__LocalStorage__TcpDataPlaneEndpoint=http://localhost:%d", inst.Config.DataPlanePort),
				"Buffers__PrimarySigningPrivateKeyPath=" + primaryPublicCertificatePath,
				"Buffers__SecondarySigningPrivateKeyPath=" + secondaryPublicCertificatePath,
				fmt.Sprintf("Database__Host=%s/database", inst.Config.InstallationPath),
				"Database__Username=tyger-server",
				"Database__TygerServerRoleName=tyger-server",
			},
			Healthcheck: &container.HealthConfig{
				Test: []string{
					"CMD",
					"/app/bin/curl",
					"--fail",
					"--unix", fmt.Sprintf("%s/control-plane/tyger.sock", inst.Config.InstallationPath),
					"http://local/healthcheck",
				},
				StartInterval: 2 * time.Second,
				Interval:      10 * time.Second,
			},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   "volume",
					Source: inst.resourceName(runLogsVolumeSuffix),
					Target: "/app/logs",
				},
				{
					Type:   "bind",
					Source: fmt.Sprintf("%s/control-plane", hostInstallationPath),
					Target: fmt.Sprintf("%s/control-plane", inst.Config.InstallationPath),
				},
				{
					Type:   "bind",
					Source: fmt.Sprintf("%s/ephemeral", hostInstallationPath),
					Target: fmt.Sprintf("%s/ephemeral", inst.Config.InstallationPath),
				},
				{
					Type:   "bind",
					Source: fmt.Sprintf("%s/data-plane", hostInstallationPath),
					Target: fmt.Sprintf("%s/data-plane", inst.Config.InstallationPath),
				},
				{
					Type:   "bind",
					Source: fmt.Sprintf("%s/database", hostInstallationPath),
					Target: fmt.Sprintf("%s/database", inst.Config.InstallationPath),
				},
				{
					Type:   "bind",
					Source: dockerSocketPath,
					Target: dockerSocketPath,
				},
			},
			NetworkMode: "none",
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyUnlessStopped,
			},
		},
	}

	for source, dest := range inst.hostPathTranslations {
		containerSpec.ContainerConfig.Env = append(containerSpec.ContainerConfig.Env, fmt.Sprintf("Compute__Docker__HostPathTranslations__%s=%s", source, dest))
	}

	// See if there is a group that has access to the docker socket.
	// If there is, add that group to the container.
	_, dockerSocketGroupId, dockerSocketPerms, err := inst.statDockerSocket(ctx)
	if err != nil {
		return fmt.Errorf("error statting docker socket: %w", err)
	}

	if dockerSocketPerms&0060 == 0060 {
		containerSpec.HostConfig.GroupAdd = append(containerSpec.HostConfig.GroupAdd, strconv.Itoa(dockerSocketGroupId))
	}

	postCreateAction := func(containerName string) error {
		tarFs := memfs.New()

		primaryPemBytes, err := os.ReadFile(inst.Config.SigningKeys.Primary.PrivateKey)
		if err != nil {
			return err
		}

		tarFs.WriteFile(path.Base(primaryPublicCertificatePath), primaryPemBytes, 0777)

		if inst.Config.SigningKeys.Secondary != nil {
			secondaryPemBytes, err := os.ReadFile(inst.Config.SigningKeys.Secondary.PrivateKey)
			if err != nil {
				return err
			}

			tarFs.WriteFile(path.Base(secondaryPublicCertificatePath), secondaryPemBytes, 0777)
		}

		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		tw.AddFS(tarFs)
		tw.Close()

		return inst.client.CopyToContainer(ctx, containerName, "/app", buf, types.CopyToContainerOptions{})
	}

	if err := inst.createContainer(
		ctx,
		inst.resourceName(controlPlaneContainerSuffix),
		&containerSpec,
		true,
		postCreateAction); err != nil {
		return err
	}

	linkPath, err := inst.getApiSocketPath()
	if err != nil {
		return err
	}

	relativeTargetPath := "control-plane/tyger.sock"

	if existingTarget, err := os.Readlink(linkPath); err == nil {
		if existingTarget == relativeTargetPath {
			return nil
		}
		if err := os.Remove(linkPath); err != nil {
			return fmt.Errorf("error removing existing symlink: %w", err)
		}
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("error reading existing symlink: %w", err)
	}

	if err := os.Symlink(relativeTargetPath, linkPath); err != nil {
		return fmt.Errorf("error creating symlink: %w", err)
	}

	return nil
}

func (inst *Installer) getApiSocketPath() (string, error) {
	path := fmt.Sprintf("%s/api.sock", inst.Config.InstallationPath)
	return filepath.Abs(path)
}

func fileHash(path string) (string, error) {
	hash := sha256.New()
	f, err := os.Open(path)
	if err != nil {
		return "", fmt.Errorf("error opening file: %w", err)
	}

	defer f.Close()

	if _, err := io.Copy(hash, f); err != nil {
		return "", fmt.Errorf("error hashing file: %w", err)
	}

	return base32.StdEncoding.EncodeToString(hash.Sum(nil)), nil
}

func (inst *Installer) waitForContainerToComplete(ctx context.Context, containerName string) (int, error) {
	statusCh, errCh := inst.client.ContainerWait(ctx, containerName, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return 0, err
	case waitReponse := <-statusCh:
		return int(waitReponse.StatusCode), nil
	}
}

func (inst *Installer) getContainerLogs(ctx context.Context, containerName string, dstout io.Writer, dsterr io.Writer) error {
	out, err := inst.client.ContainerLogs(ctx, containerName, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})

	if err != nil {
		return err
	}

	defer out.Close()

	// Read the output
	_, err = stdcopy.StdCopy(dstout, dsterr, out)
	return err
}

func (inst *Installer) UninstallTyger(ctx context.Context, deleteData bool, preserveRunContainers bool) error {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("error creating docker client: %w", err)
	}

	if err := inst.removeContainer(ctx, inst.resourceName(databaseContainerSuffix)); err != nil {
		return fmt.Errorf("error removing database container: %w", err)
	}

	if err := inst.removeContainer(ctx, inst.resourceName(dataPlaneContainerSuffix)); err != nil {
		return fmt.Errorf("error removing data plane container: %w", err)
	}

	if err := inst.removeContainer(ctx, inst.resourceName(controlPlaneContainerSuffix)); err != nil {
		return fmt.Errorf("error removing control plane container: %w", err)
	}

	if err := inst.removeContainer(ctx, inst.resourceName(migrationRunnerContainerSuffix)); err != nil {
		return fmt.Errorf("error removing control plane container: %w", err)
	}

	if err := inst.removeContainer(ctx, inst.resourceName(gatewayContainerSuffix)); err != nil {
		return fmt.Errorf("error removing gateway container: %w", err)
	}

	if !preserveRunContainers {
		runContainers, err := dockerClient.ContainerList(
			ctx,
			container.ListOptions{
				All:     true,
				Filters: filters.NewArgs(filters.Arg("label", "tyger-run")),
			})

		if err != nil {
			return fmt.Errorf("error listing run containers: %w", err)
		}

		for _, runContainer := range runContainers {
			if err := inst.removeContainer(ctx, runContainer.ID); err != nil {
				return fmt.Errorf("error removing run container: %w", err)
			}
		}
	}

	if err := inst.removeNetwork(ctx); err != nil {
		return err
	}

	entries, err := os.ReadDir(inst.Config.InstallationPath)
	if err != nil {
		return fmt.Errorf("error reading %s: %w", inst.Config.InstallationPath, err)
	}

	for _, entry := range entries {
		path := path.Join(inst.Config.InstallationPath, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("error removing %s: %w", path, err)
		}
	}

	if deleteData {
		if err := inst.client.VolumeRemove(ctx, inst.resourceName(databaseVolumeSuffix), true); err != nil {
			return fmt.Errorf("error removing database volume: %w", err)
		}

		if err := inst.client.VolumeRemove(ctx, inst.resourceName(buffersVolumeSuffix), true); err != nil {
			return fmt.Errorf("error removing buffers volume: %w", err)
		}

		if err := inst.client.VolumeRemove(ctx, inst.resourceName(runLogsVolumeSuffix), true); err != nil {
			return fmt.Errorf("error removing run logs volume: %w", err)
		}
	}

	return nil
}

func (inst *Installer) createDatabaseContainer(ctx context.Context) error {
	if err := inst.ensureVolumeCreated(ctx, inst.resourceName(databaseVolumeSuffix)); err != nil {
		return err
	}

	if err := inst.ensureDirectoryExists(fmt.Sprintf("%s/database", inst.Config.InstallationPath)); err != nil {
		return err
	}

	image := inst.Config.PostgresImage

	containerSpec := containerSpec{
		ContainerConfig: &container.Config{
			Image: image,
			Cmd: []string{
				"-c", "listen_addresses=", // only unix socket
			},
			Env: []string{
				"POSTGRES_HOST_AUTH_METHOD=trust",
				"POSTGRES_USER=tyger-server",
			},
			Healthcheck: &container.HealthConfig{
				Test:          []string{"CMD", "pg_isready", "-U", "tyger-server"},
				StartInterval: 2 * time.Second,
				Interval:      10 * time.Second,
			},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   "volume",
					Source: inst.resourceName(databaseVolumeSuffix),
					Target: "/var/lib/postgresql/data",
				},
				{
					Type:   "bind",
					Source: fmt.Sprintf("%s/database", inst.translateToHostPath(inst.Config.InstallationPath)),
					Target: "/var/run/postgresql/",
				},
			},
			NetworkMode: "none",
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyUnlessStopped,
			},
		},
	}

	if err := inst.createContainer(ctx, inst.resourceName(databaseContainerSuffix), &containerSpec, true); err != nil {
		return err
	}

	return nil
}

func (inst *Installer) createDataPlaneContainer(ctx context.Context) error {
	if err := inst.ensureVolumeCreated(ctx, inst.resourceName(buffersVolumeSuffix)); err != nil {
		return err
	}

	if err := inst.ensureDirectoryExists(fmt.Sprintf("%s/data-plane", inst.Config.InstallationPath)); err != nil {
		return err
	}

	image := inst.Config.DataPlaneImage

	primaryPublicKeyHash, err := fileHash(inst.Config.SigningKeys.Primary.PublicKey)
	if err != nil {
		return fmt.Errorf("error hashing primary public key: %w", err)
	}

	primaryPublicCertificatePath := fmt.Sprintf("/app/tyger-data-plane-public-primary-%s.pem", primaryPublicKeyHash)
	secondaryPublicCertificatePath := ""
	if inst.Config.SigningKeys.Secondary != nil {
		secondaryPublicKeyHash, err := fileHash(inst.Config.SigningKeys.Secondary.PublicKey)
		if err != nil {
			return fmt.Errorf("error hashing secondary public key: %w", err)
		}

		secondaryPublicCertificatePath = fmt.Sprintf("/app/tyger-data-plane-public-secondary-%s.pem", secondaryPublicKeyHash)
	}

	spec := containerSpec{
		ContainerConfig: &container.Config{
			Image: image,
			User:  inst.Config.UserId,
			Env: []string{
				fmt.Sprintf("Urls=http://unix:%s/data-plane/tyger.data.sock;http://0.0.0.0:8080", inst.Config.InstallationPath),
				"SocketPermissions=666",
				"DataDirectory=/app/data",
				"PrimarySigningPublicKeyPath=" + primaryPublicCertificatePath,
				"SecondarySigningPublicKeyPath=" + secondaryPublicCertificatePath,
			},
			Healthcheck: &container.HealthConfig{
				Test: []string{
					"CMD",
					"/app/bin/curl",
					"--fail",
					"--unix", fmt.Sprintf("%s/data-plane/tyger.data.sock", inst.Config.InstallationPath),
					"http://local/healthcheck",
				},
				StartInterval: 2 * time.Second,
				Interval:      10 * time.Second,
			},
			ExposedPorts: nat.PortSet{
				"8080/tcp": struct{}{},
			},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   "volume",
					Source: inst.resourceName(buffersVolumeSuffix),
					Target: "/app/data",
				},
				{
					Type:   "bind",
					Source: fmt.Sprintf("%s/data-plane", inst.translateToHostPath(inst.Config.InstallationPath)),
					Target: fmt.Sprintf("%s/data-plane", inst.Config.InstallationPath),
				},
			},
			PortBindings: nat.PortMap{
				"8080/tcp": []nat.PortBinding{
					{
						HostIP:   "127.0.0.1",
						HostPort: strconv.Itoa(inst.Config.DataPlanePort),
					},
				},
			},
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyUnlessStopped,
			},
		},
	}

	postCreateAction := func(containerName string) error {
		tarFs := memfs.New()

		publicPemBytes, err := os.ReadFile(inst.Config.SigningKeys.Primary.PublicKey)
		if err != nil {
			return fmt.Errorf("error reading primary public key: %w", err)
		}

		tarFs.WriteFile(path.Base(primaryPublicCertificatePath), publicPemBytes, 0777)

		if inst.Config.SigningKeys.Secondary != nil {
			publicPemBytes, err = os.ReadFile(inst.Config.SigningKeys.Secondary.PublicKey)
			if err != nil {
				return fmt.Errorf("error reading secondary public key: %w", err)
			}

			tarFs.WriteFile(path.Base(secondaryPublicCertificatePath), publicPemBytes, 0777)
		}

		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		tw.AddFS(tarFs)
		tw.Close()

		return inst.client.CopyToContainer(ctx, containerName, "/app", buf, types.CopyToContainerOptions{})
	}

	if err := inst.createContainer(
		ctx,
		inst.resourceName(dataPlaneContainerSuffix),
		&spec,
		true,
		postCreateAction); err != nil {
		return err
	}

	return nil
}

func (inst *Installer) createGatewayContainer(ctx context.Context) error {
	socketPath, err := inst.getApiSocketPath()
	if err != nil {
		return err
	}

	spec := containerSpec{
		ContainerConfig: &container.Config{
			Image: inst.Config.GatewayImage,
			User:  inst.Config.UserId,
			Cmd:   []string{"stdio-proxy", "sleep"},
			Env: []string{
				fmt.Sprintf("%s=%s", tygerclient.DefaultControlPlaneSocketPathEnvVar, socketPath),
			},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   "bind",
					Source: inst.translateToHostPath(inst.Config.InstallationPath),
					Target: inst.Config.InstallationPath,
				},
			},
			NetworkMode: "none",
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyUnlessStopped,
			},
		},
	}

	if err := inst.createContainer(ctx, inst.resourceName(gatewayContainerSuffix), &spec, false); err != nil {
		return err
	}

	return nil
}

func (inst *Installer) createContainer(
	ctx context.Context,
	containerName string,
	containerSpec *containerSpec,
	waitForHealthy bool,
	postCreateActions ...func(containerName string) error,
) error {
	specHash := containerSpec.computeHash()
	if containerSpec.ContainerConfig.Labels == nil {
		containerSpec.ContainerConfig.Labels = make(map[string]string)
	}

	containerSpec.ContainerConfig.Labels[containerSpecHashLabel] = specHash

	containerExists := true
	existingContainer, err := inst.client.ContainerInspect(ctx, containerName)
	if err != nil {
		if !client.IsErrNotFound(err) {
			return fmt.Errorf("error checking for container: %w", err)
		}

		containerExists = false
	}

	if containerExists && (existingContainer.Config.Labels[containerSpecHashLabel] != specHash || !existingContainer.State.Running) {
		if err := inst.removeContainer(ctx, containerName); err != nil {
			return fmt.Errorf("error removing existing container: %w", err)
		}

		containerExists = false
	}

	if !containerExists {
		containerImage := containerSpec.ContainerConfig.Image
		if err := inst.pullImage(ctx, containerImage, false); err != nil {
			return fmt.Errorf("error pulling image: %w", err)
		}

		if containerSpec.ContainerConfig.Healthcheck != nil &&
			(containerSpec.ContainerConfig.Healthcheck.StartInterval != 0 || containerSpec.ContainerConfig.Healthcheck.StartPeriod != 0) {
			// these properties are not supported in older versions of the docker API
			r, err := inst.client.Ping(ctx)
			if err != nil {
				return fmt.Errorf("error pinging server: %w", err)
			}

			if r.APIVersion != "" && compareVersions(r.APIVersion, "1.44") < 0 {
				containerSpec.ContainerConfig.Healthcheck.StartPeriod = 0
				containerSpec.ContainerConfig.Healthcheck.StartInterval = 0
			}
		}

		resp, err := inst.client.ContainerCreate(ctx, containerSpec.ContainerConfig, containerSpec.HostConfig, containerSpec.NetworkingConfig, nil, containerName)

		if err != nil {
			return fmt.Errorf("error creating container: %w", err)
		}

		for _, a := range postCreateActions {
			if err := a(containerName); err != nil {
				return fmt.Errorf("error running post-create action: %w", err)
			}
		}

		if err := inst.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			return fmt.Errorf("error starting container: %w", err)
		}
	}

	if waitForHealthy {
		waitStartTime := time.Now()

		for {
			c, err := inst.client.ContainerInspect(ctx, containerName)
			if err != nil {
				return fmt.Errorf("error inspecting container: %w", err)
			}

			if c.State.Health.Status == "healthy" {
				break
			}

			if time.Since(waitStartTime) > 60*time.Second {
				return fmt.Errorf("timed out waiting for container to become healthy. Current status: %s", c.State.Health.Status)
			}
		}
	}

	return nil
}

func (inst *Installer) pullImage(ctx context.Context, containerImage string, always bool) error {
	if !always {
		_, _, err := inst.client.ImageInspectWithRaw(ctx, containerImage)
		if err == nil {
			return nil
		}
	}

	reader, err := inst.client.ImagePull(ctx, containerImage, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("error pulling image: %w", err)
	}

	defer reader.Close()
	log.Info().Msgf("Pulling image %s", containerImage)
	io.Copy(io.Discard, reader)
	log.Info().Msgf("Done pulling image %s", containerImage)

	return nil
}

func (inst *Installer) removeContainer(ctx context.Context, containerName string) error {
	if err := inst.client.ContainerStop(ctx, containerName, container.StopOptions{}); err != nil {
		if client.IsErrNotFound(err) {
			return nil
		}
		return fmt.Errorf("error stopping container: %w", err)
	}

	if err := inst.client.ContainerRemove(ctx, containerName, container.RemoveOptions{
		Force: true,
	}); err != nil {
		return fmt.Errorf("error removing container: %w", err)
	}

	return nil
}

func (inst *Installer) ensureVolumeCreated(ctx context.Context, volumeName string) error {
	if _, err := inst.client.VolumeInspect(ctx, volumeName); err != nil {
		if client.IsErrNotFound(err) {
			if _, err := inst.client.VolumeCreate(ctx, volume.CreateOptions{
				Name: volumeName,
			}); err != nil {
				return fmt.Errorf("error creating volume: %w", err)
			}
		} else {
			return fmt.Errorf("error checking for volume: %w", err)
		}
	}

	return nil
}

func (inst *Installer) checkGpuAvailability(ctx context.Context) (bool, error) {
	info, err := inst.client.Info(ctx)
	if err != nil {
		return false, fmt.Errorf("error getting docker info: %w", err)
	}

	if _, ok := info.Runtimes["nvidia"]; !ok {
		return false, nil
	}

	// The NVIDIA runtime is available, but we need to check if it is working
	// and that there are GPUs available

	containerConfig := &container.Config{
		Image: inst.Config.MarinerImage,
		Cmd:   []string{"bash", "-c", "[[ $(nvidia-smi -L | wc -l) > 0 ]]"},
		Tty:   false,
	}

	hostConfig := &container.HostConfig{
		Resources: container.Resources{
			DeviceRequests: []container.DeviceRequest{
				{
					Count:        -1,
					Capabilities: [][]string{{"gpu"}},
				},
			},
		},
	}

	if err := inst.pullImage(ctx, containerConfig.Image, false); err != nil {
		return false, fmt.Errorf("error pulling image: %w", err)
	}

	resp, err := inst.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return false, fmt.Errorf("error creating container: %w", err)
	}

	defer func() {
		if err := inst.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
			panic(err)
		}
	}()

	if err := inst.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		// this will return an error if the nvidia runtime is not available
		log.Debug().Msgf("Error starting test container with GPU: %v", err)
		return false, nil
	}

	// Wait for the container to finish
	statusCh, errCh := inst.client.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return false, err
	case r := <-statusCh:
		return r.StatusCode == 0, nil
	}
}

func (inst *Installer) statDockerSocket(ctx context.Context) (userId int, groupId int, permissions int, err error) {
	// Define the container configuration
	containerConfig := &container.Config{
		Image: inst.Config.MarinerImage,
		Cmd:   []string{"stat", "-c", "%u %g %a", dockerSocketPath},
		Tty:   false,
	}

	// Define the host configuration (volume mounts)
	hostConfig := &container.HostConfig{
		Binds: []string{"/var/run/docker.sock:/var/run/docker.sock"},
	}

	if err := inst.pullImage(ctx, containerConfig.Image, false); err != nil {
		return 0, 0, 0, fmt.Errorf("error pulling image: %w", err)
	}

	resp, err := inst.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return 0, 0, 0, err
	}

	defer func() {
		if err := inst.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
			panic(err)
		}
	}()

	if err := inst.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return 0, 0, 0, err
	}

	// Wait for the container to finish
	statusCh, errCh := inst.client.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return 0, 0, 0, err
		}
	case r := <-statusCh:
		if r.StatusCode != 0 {
			return 0, 0, 0, fmt.Errorf("unable to stat docker socket: container exited with status %d", r.StatusCode)
		}
	}

	out, err := inst.client.ContainerLogs(ctx, resp.ID, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})

	if err != nil {
		return 0, 0, 0, err
	}

	defer out.Close()

	// Read the output
	stdOutput, errOutput := &bytes.Buffer{}, &bytes.Buffer{}
	_, err = stdcopy.StdCopy(stdOutput, errOutput, out)
	if err != nil {
		return 0, 0, 0, err
	}

	if _, err := fmt.Sscanf(stdOutput.String(), "%d %d %o", &userId, &groupId, &permissions); err != nil {
		return 0, 0, 0, fmt.Errorf("error parsing stat output: %w", err)
	}

	return userId, groupId, permissions, nil
}

func (inst *Installer) createNetwork(ctx context.Context) error {
	networkName := inst.resourceName("network")
	existingNetwork, err := inst.client.NetworkInspect(ctx, networkName, types.NetworkInspectOptions{})
	if err != nil && !client.IsErrNotFound(err) {
		return fmt.Errorf("error checking for network: %w", err)
	}

	existingNetworkExists := !client.IsErrNotFound(err)

	networkCreateOptions := types.NetworkCreate{
		Driver: "bridge",
	}

	if inst.Config.Network == nil || inst.Config.Network.Subnet == "" {
		if existingNetworkExists {
			return nil
		}
	} else {
		if existingNetworkExists {
			if existingNetwork.IPAM.Config[0].Subnet == inst.Config.Network.Subnet {
				return nil
			}

			return fmt.Errorf("network %s already exists with a different subnet", networkName)
		}

		networkCreateOptions.IPAM = &network.IPAM{
			Config: []network.IPAMConfig{
				{
					Subnet: inst.Config.Network.Subnet,
				},
			},
		}
	}

	if _, err := inst.client.NetworkCreate(ctx, networkName, networkCreateOptions); err != nil {
		return fmt.Errorf("error creating network: %w", err)
	}

	return nil
}

func (inst *Installer) removeNetwork(ctx context.Context) error {
	networkName := inst.resourceName("network")
	if err := inst.client.NetworkRemove(ctx, networkName); err != nil {
		if client.IsErrNotFound(err) {
			return nil
		}

		return fmt.Errorf("error removing network: %w", err)
	}

	return nil
}

// -1 if v1 < v2, 0 if v1 == v2, 1 if v1 > v2
func compareVersions(v1, v2 string) int {
	parts1 := strings.Split(v1, ".")
	parts2 := strings.Split(v2, ".")

	maxLen := len(parts1)
	if len(parts2) > maxLen {
		maxLen = len(parts2)
	}

	for i := 0; i < maxLen; i++ {
		var num1, num2 int
		var err error

		if i < len(parts1) {
			num1, err = strconv.Atoi(parts1[i])
			if err != nil {
				panic(fmt.Sprintf("Invalid version number in %s: %s\n", v1, parts1[i]))
			}
		}

		if i < len(parts2) {
			num2, err = strconv.Atoi(parts2[i])
			if err != nil {
				panic(fmt.Sprintf("Invalid version number in %s: %s\n", v2, parts2[i]))
			}
		}

		if num1 < num2 {
			return -1
		} else if num1 > num2 {
			return 1
		}
	}

	return 0
}
