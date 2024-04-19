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
	"path"
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
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/psanford/memfs"
	"github.com/rs/zerolog/log"
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
	Config *DockerEnvironmentConfig
	client *client.Client
}

func NewInstaller(config *DockerEnvironmentConfig) (*Installer, error) {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return nil, fmt.Errorf("error creating docker client: %w", err)
	}
	return &Installer{
		Config: config,
		client: dockerClient,
	}, nil
}

func (i *Installer) resourceName(suffix string) string {
	return fmt.Sprintf("tyger-%s-%s", i.Config.EnvironmentName, suffix)
}

func (i *Installer) InstallTyger(ctx context.Context) error {
	if runtime.GOOS == "windows" {
		log.Error().Msg("Installing Tyger in Docker on Windows must be done from a WSL2 shell. Once installed, other commands can be run from within Windows.")
		return install.ErrAlreadyLoggedError
	}

	if err := i.ensureDirectoryExists(i.Config.InstallationPath); err != nil {
		return err
	}

	pg := &install.PromiseGroup{}

	dbPromise := install.NewPromise(ctx, pg, func(ctx context.Context) (any, error) {
		if err := i.createDatabaseContainer(ctx); err != nil {
			return nil, fmt.Errorf("error creating database container: %w", err)
		}
		return nil, nil
	})

	dataPlanePromise := install.NewPromiseAfter(ctx, pg, func(ctx context.Context) (any, error) {

		if err := i.createDataPlaneContainer(ctx); err != nil {
			return nil, fmt.Errorf("error creating data plane container: %w", err)
		}
		return nil, nil
	})

	migrationRunnerPromise := install.NewPromiseAfter(ctx, pg, func(ctx context.Context) (any, error) {
		if err := i.initializeDatabase(ctx); err != nil {
			return nil, fmt.Errorf("error initializing database: %w", err)
		}
		return nil, nil
	}, dbPromise)

	install.NewPromiseAfter(ctx, pg, func(ctx context.Context) (any, error) {
		if err := i.createControlPlaneContainer(ctx); err != nil {
			return nil, fmt.Errorf("error creating control plane container: %w", err)
		}
		return nil, nil
	}, migrationRunnerPromise, dataPlanePromise)

	if i.Config.UseGateway != nil && *i.Config.UseGateway {
		install.NewPromise(ctx, pg, func(ctx context.Context) (any, error) {
			if err := i.createGatewayContainer(ctx); err != nil {
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

func (i *Installer) ensureDirectoryExists(path string) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("error creating directory %s: %w", path, err)
		}

		return os.Chown(path, i.Config.GetUserIdInt(), i.Config.GetGroupIdInt())
	} else {
		return err
	}
}

func (i *Installer) createControlPlaneContainer(ctx context.Context) error {
	if err := i.ensureVolumeCreated(ctx, i.resourceName(runLogsVolumeSuffix)); err != nil {
		return err
	}

	if err := i.ensureDirectoryExists(fmt.Sprintf("%s/control-plane", i.Config.InstallationPath)); err != nil {
		return err
	}

	if err := i.pullImage(ctx, i.Config.BufferSidecarImage, false); err != nil {
		return fmt.Errorf("error pulling buffer sidecar image: %w", err)
	}

	image := i.Config.ControlPlaneImage

	primaryPublicKeyHash, err := fileHash(i.Config.SigningKeys.Primary.PublicKey)
	if err != nil {
		return fmt.Errorf("error hashing primary public key: %w", err)
	}

	primaryPublicCertificatePath := fmt.Sprintf("/app/tyger-data-plane-primary-%s.pem", primaryPublicKeyHash)
	secondaryPublicCertificatePath := ""
	if i.Config.SigningKeys.Secondary != nil {
		secondaryPublicKeyHash, err := fileHash(i.Config.SigningKeys.Secondary.PublicKey)
		if err != nil {
			return fmt.Errorf("error hashing secondary public key: %w", err)
		}

		secondaryPublicCertificatePath = fmt.Sprintf("/app/tyger-data-plane-secondary-%s.pem", secondaryPublicKeyHash)
	}

	containerSpec := containerSpec{
		ContainerConfig: &container.Config{
			Image: image,
			User:  fmt.Sprintf("%d:%d", i.Config.GetUserIdInt(), i.Config.GetGroupIdInt()),
			Env: []string{
				fmt.Sprintf("Urls=http://unix:%s/control-plane/tyger.sock", i.Config.InstallationPath),
				"SocketPermissions=660",
				"Auth__Enabled=false",
				fmt.Sprintf("Compute__Docker__RunSecretsPath=%s/control-plane/run-secrets/", i.Config.InstallationPath),
				fmt.Sprintf("Compute__Docker__EphemeralBuffersPath=%s/control-plane/ephemeral-buffers/", i.Config.InstallationPath),
				"LogArchive__LocalStorage__LogsDirectory=/app/logs",
				"Buffers__BufferSidecarImage=" + i.Config.BufferSidecarImage,
				fmt.Sprintf("Buffers__LocalStorage__DataPlaneEndpoint=http+unix://%s/data-plane/tyger.data.sock", i.Config.InstallationPath),
				"Buffers__PrimarySigningPrivateKeyPath=" + primaryPublicCertificatePath,
				"Buffers__SecondarySigningPrivateKeyPath=" + secondaryPublicCertificatePath,
				fmt.Sprintf("Database__ConnectionString=Host=%s/database; Username=tyger-server", i.Config.InstallationPath),
				"Database__TygerServerRoleName=tyger-server",
			},
			Healthcheck: &container.HealthConfig{
				Test: []string{
					"CMD",
					"/app/bin/curl",
					"--fail",
					"--unix", fmt.Sprintf("%s/control-plane/tyger.sock", i.Config.InstallationPath),
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
					Source: i.resourceName(runLogsVolumeSuffix),
					Target: "/app/logs",
				},
				{
					Type:   "bind",
					Source: fmt.Sprintf("%s/control-plane", i.Config.InstallationPath),
					Target: fmt.Sprintf("%s/control-plane", i.Config.InstallationPath),
				},
				{
					Type:   "bind",
					Source: fmt.Sprintf("%s/data-plane", i.Config.InstallationPath),
					Target: fmt.Sprintf("%s/data-plane", i.Config.InstallationPath),
				},
				{
					Type:   "bind",
					Source: fmt.Sprintf("%s/database", i.Config.InstallationPath),
					Target: fmt.Sprintf("%s/database", i.Config.InstallationPath),
				},
				{
					Type:   "bind",
					Source: "/var/run/docker.sock",
					Target: "/var/run/docker.sock",
				},
			},
			NetworkMode: "none",
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyUnlessStopped,
			},
		},
	}

	// See if there is a group that has access to the docker socket.
	// If there is, add that group to the container.
	_, dockerSocketGroupId, dockerSocketPerms, err := i.statDockerSocket(ctx)
	if err != nil {
		return fmt.Errorf("error statting docker socket: %w", err)
	}

	if dockerSocketPerms&0060 == 0060 {
		containerSpec.HostConfig.GroupAdd = append(containerSpec.HostConfig.GroupAdd, strconv.Itoa(dockerSocketGroupId))
	}

	postCreateAction := func(containerName string) error {
		tarFs := memfs.New()

		primaryPemBytes, err := os.ReadFile(i.Config.SigningKeys.Primary.PrivateKey)
		if err != nil {
			return err
		}

		tarFs.WriteFile(path.Base(primaryPublicCertificatePath), primaryPemBytes, 0777)

		if i.Config.SigningKeys.Secondary != nil {
			secondaryPemBytes, err := os.ReadFile(i.Config.SigningKeys.Secondary.PrivateKey)
			if err != nil {
				return err
			}

			tarFs.WriteFile(path.Base(secondaryPublicCertificatePath), secondaryPemBytes, 0777)
		}

		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		tw.AddFS(tarFs)
		tw.Close()

		return i.client.CopyToContainer(ctx, containerName, "/app", buf, types.CopyToContainerOptions{})
	}

	if err := i.createContainer(
		ctx,
		i.resourceName(controlPlaneContainerSuffix),
		&containerSpec,
		true,
		postCreateAction); err != nil {
		return err
	}

	if err := os.Symlink(fmt.Sprintf("%s/control-plane/tyger.sock", i.Config.InstallationPath), fmt.Sprintf("%s/api.sock", i.Config.InstallationPath)); err != nil && !os.IsExist(err) {
		return fmt.Errorf("error creating symlink: %w", err)
	}

	return nil
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

func (i *Installer) waitForContainerToComplete(ctx context.Context, containerName string) (int, error) {
	statusCh, errCh := i.client.ContainerWait(ctx, containerName, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		return 0, err
	case waitReponse := <-statusCh:
		return int(waitReponse.StatusCode), nil
	}
}

func (i *Installer) getContainerLogs(ctx context.Context, containerName string, dstout io.Writer, dsterr io.Writer) error {
	out, err := i.client.ContainerLogs(ctx, containerName, container.LogsOptions{
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

func (i *Installer) UninstallTyger(ctx context.Context, deleteData bool) error {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("error creating docker client: %w", err)
	}

	if err := i.removeContainer(ctx, i.resourceName(databaseContainerSuffix)); err != nil {
		return fmt.Errorf("error removing database container: %w", err)
	}

	if err := i.removeContainer(ctx, i.resourceName(dataPlaneContainerSuffix)); err != nil {
		return fmt.Errorf("error removing data plane container: %w", err)
	}

	if err := i.removeContainer(ctx, i.resourceName(controlPlaneContainerSuffix)); err != nil {
		return fmt.Errorf("error removing control plane container: %w", err)
	}

	if err := i.removeContainer(ctx, i.resourceName(migrationRunnerContainerSuffix)); err != nil {
		return fmt.Errorf("error removing control plane container: %w", err)
	}

	if err := i.removeContainer(ctx, i.resourceName(gatewayContainerSuffix)); err != nil {
		return fmt.Errorf("error removing gateway container: %w", err)
	}

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
		if err := i.removeContainer(ctx, runContainer.ID); err != nil {
			return fmt.Errorf("error removing run container: %w", err)
		}
	}

	entries, err := os.ReadDir(i.Config.InstallationPath)
	if err != nil {
		return fmt.Errorf("error reading %s: %w", i.Config.InstallationPath, err)
	}

	for _, entry := range entries {
		path := path.Join(i.Config.InstallationPath, entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("error removing %s: %w", path, err)
		}
	}

	if deleteData {
		if err := i.client.VolumeRemove(ctx, i.resourceName(databaseVolumeSuffix), true); err != nil {
			return fmt.Errorf("error removing database volume: %w", err)
		}

		if err := i.client.VolumeRemove(ctx, i.resourceName(buffersVolumeSuffix), true); err != nil {
			return fmt.Errorf("error removing buffers volume: %w", err)
		}

		if err := i.client.VolumeRemove(ctx, i.resourceName(runLogsVolumeSuffix), true); err != nil {
			return fmt.Errorf("error removing run logs volume: %w", err)
		}
	}

	return nil
}

func (i *Installer) createDatabaseContainer(ctx context.Context) error {
	if err := i.ensureVolumeCreated(ctx, i.resourceName(databaseVolumeSuffix)); err != nil {
		return err
	}

	if err := i.ensureDirectoryExists(fmt.Sprintf("%s/database", i.Config.InstallationPath)); err != nil {
		return err
	}

	image := i.Config.PostgresImage

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
					Source: i.resourceName(databaseVolumeSuffix),
					Target: "/var/lib/postgresql/data",
				},
				{
					Type:   "bind",
					Source: fmt.Sprintf("%s/database", i.Config.InstallationPath),
					Target: "/var/run/postgresql/",
				},
			},
			NetworkMode: "none",
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyUnlessStopped,
			},
		},
	}

	if err := i.createContainer(ctx, i.resourceName(databaseContainerSuffix), &containerSpec, true); err != nil {
		return err
	}

	return nil
}

func (i *Installer) createDataPlaneContainer(ctx context.Context) error {
	if err := i.ensureVolumeCreated(ctx, i.resourceName(buffersVolumeSuffix)); err != nil {
		return err
	}

	if err := i.ensureDirectoryExists(fmt.Sprintf("%s/data-plane", i.Config.InstallationPath)); err != nil {
		return err
	}

	image := i.Config.DataPlaneImage

	primaryPublicKeyHash, err := fileHash(i.Config.SigningKeys.Primary.PublicKey)
	if err != nil {
		return fmt.Errorf("error hashing primary public key: %w", err)
	}

	primaryPublicCertificatePath := fmt.Sprintf("/app/tyger-data-plane-public-primary-%s.pem", primaryPublicKeyHash)
	secondaryPublicCertificatePath := ""
	if i.Config.SigningKeys.Secondary != nil {
		secondaryPublicKeyHash, err := fileHash(i.Config.SigningKeys.Secondary.PublicKey)
		if err != nil {
			return fmt.Errorf("error hashing secondary public key: %w", err)
		}

		secondaryPublicCertificatePath = fmt.Sprintf("/app/tyger-data-plane-public-secondary-%s.pem", secondaryPublicKeyHash)
	}

	spec := containerSpec{
		ContainerConfig: &container.Config{
			Image: image,
			User:  i.Config.UserId,
			Env: []string{
				fmt.Sprintf("Urls=http://unix:%s/data-plane/tyger.data.sock", i.Config.InstallationPath),
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
					"--unix", fmt.Sprintf("%s/data-plane/tyger.data.sock", i.Config.InstallationPath),
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
					Source: i.resourceName(buffersVolumeSuffix),
					Target: "/app/data",
				},
				{
					Type:   "bind",
					Source: fmt.Sprintf("%s/data-plane", i.Config.InstallationPath),
					Target: fmt.Sprintf("%s/data-plane", i.Config.InstallationPath),
				},
			},
			NetworkMode: "none",
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyUnlessStopped,
			},
		},
	}

	postCreateAction := func(containerName string) error {
		tarFs := memfs.New()

		publicPemBytes, err := os.ReadFile(i.Config.SigningKeys.Primary.PublicKey)
		if err != nil {
			return fmt.Errorf("error reading primary public key: %w", err)
		}

		tarFs.WriteFile(path.Base(primaryPublicCertificatePath), publicPemBytes, 0777)

		if i.Config.SigningKeys.Secondary != nil {
			publicPemBytes, err = os.ReadFile(i.Config.SigningKeys.Secondary.PublicKey)
			if err != nil {
				return fmt.Errorf("error reading secondary public key: %w", err)
			}

			tarFs.WriteFile(path.Base(secondaryPublicCertificatePath), publicPemBytes, 0777)
		}

		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		tw.AddFS(tarFs)
		tw.Close()

		return i.client.CopyToContainer(ctx, containerName, "/app", buf, types.CopyToContainerOptions{})
	}

	if err := i.createContainer(
		ctx,
		i.resourceName(dataPlaneContainerSuffix),
		&spec,
		true,
		postCreateAction); err != nil {
		return err
	}

	return nil
}

func (i *Installer) createGatewayContainer(ctx context.Context) error {
	image := i.Config.GatewayImage

	spec := containerSpec{
		ContainerConfig: &container.Config{
			Image: image,
			User:  i.Config.UserId,
			Cmd:   []string{"stdio-proxy", "sleep"},
		},
		HostConfig: &container.HostConfig{
			Mounts: []mount.Mount{
				{
					Type:   "bind",
					Source: i.Config.InstallationPath,
					Target: i.Config.InstallationPath,
				},
			},
			NetworkMode: "none",
			RestartPolicy: container.RestartPolicy{
				Name: container.RestartPolicyUnlessStopped,
			},
		},
	}

	if err := i.createContainer(ctx, i.resourceName(gatewayContainerSuffix), &spec, false); err != nil {
		return err
	}

	return nil
}

func (i *Installer) createContainer(
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
	existingContainer, err := i.client.ContainerInspect(ctx, containerName)
	if err != nil {
		if !client.IsErrNotFound(err) {
			return fmt.Errorf("error checking for container: %w", err)
		}

		containerExists = false
	}

	if containerExists && (existingContainer.Config.Labels[containerSpecHashLabel] != specHash || !existingContainer.State.Running) {
		if err := i.removeContainer(ctx, containerName); err != nil {
			return fmt.Errorf("error removing existing container: %w", err)
		}

		containerExists = false
	}

	if !containerExists {
		containerImage := containerSpec.ContainerConfig.Image
		if err := i.pullImage(ctx, containerImage, false); err != nil {
			return fmt.Errorf("error pulling image: %w", err)
		}

		if containerSpec.ContainerConfig.Healthcheck != nil &&
			(containerSpec.ContainerConfig.Healthcheck.StartInterval != 0 || containerSpec.ContainerConfig.Healthcheck.StartPeriod != 0) {
			// these properties are not supported in older versions of the docker API
			r, err := i.client.Ping(ctx)
			if err != nil {
				return fmt.Errorf("error pinging server: %w", err)
			}

			if r.APIVersion != "" && compareVersions(r.APIVersion, "1.44") < 0 {
				containerSpec.ContainerConfig.Healthcheck.StartPeriod = 0
				containerSpec.ContainerConfig.Healthcheck.StartInterval = 0
			}
		}

		resp, err := i.client.ContainerCreate(ctx, containerSpec.ContainerConfig, containerSpec.HostConfig, containerSpec.NetworkingConfig, nil, containerName)

		if err != nil {
			return fmt.Errorf("error creating container: %w", err)
		}

		for _, a := range postCreateActions {
			if err := a(containerName); err != nil {
				return fmt.Errorf("error running post-create action: %w", err)
			}
		}

		if err := i.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			return fmt.Errorf("error starting container: %w", err)
		}
	}

	if waitForHealthy {
		waitStartTime := time.Now()

		for {
			c, err := i.client.ContainerInspect(ctx, containerName)
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

func (i *Installer) pullImage(ctx context.Context, containerImage string, always bool) error {
	if !always {
		_, _, err := i.client.ImageInspectWithRaw(ctx, containerImage)
		if err == nil {
			return nil
		}
	}

	reader, err := i.client.ImagePull(ctx, containerImage, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("error pulling image: %w", err)
	}

	defer reader.Close()
	log.Info().Msgf("Pulling image %s", containerImage)
	io.Copy(io.Discard, reader)
	log.Info().Msgf("Done pulling image %s", containerImage)

	return nil
}

func (i *Installer) removeContainer(ctx context.Context, containerName string) error {
	if err := i.client.ContainerStop(ctx, containerName, container.StopOptions{}); err != nil {
		if client.IsErrNotFound(err) {
			return nil
		}
		return fmt.Errorf("error stopping container: %w", err)
	}

	if err := i.client.ContainerRemove(ctx, containerName, container.RemoveOptions{
		Force: true,
	}); err != nil {
		return fmt.Errorf("error removing container: %w", err)
	}

	return nil
}

func (i *Installer) ensureVolumeCreated(ctx context.Context, volumeName string) error {
	if _, err := i.client.VolumeInspect(ctx, volumeName); err != nil {
		if client.IsErrNotFound(err) {
			if _, err := i.client.VolumeCreate(ctx, volume.CreateOptions{
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

func (i *Installer) statDockerSocket(ctx context.Context) (userId int, groupId int, permissions int, err error) {
	// Define the container configuration
	containerConfig := &container.Config{
		Image: "mcr.microsoft.com/cbl-mariner/base/core:2.0",
		Cmd:   []string{"stat", "-c", "%u %g %a", "/var/run/docker.sock"},
		Tty:   false, // not interactive
	}

	// Define the host configuration (volume mounts)
	hostConfig := &container.HostConfig{
		Binds: []string{"/var/run/docker.sock:/var/run/docker.sock"},
	}

	if err := i.pullImage(ctx, containerConfig.Image, false); err != nil {
		return 0, 0, 0, fmt.Errorf("error pulling image: %w", err)
	}

	resp, err := i.client.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return 0, 0, 0, err
	}

	defer func() {
		if err := i.client.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
			panic(err)
		}
	}()

	if err := i.client.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return 0, 0, 0, err
	}

	// Wait for the container to finish
	statusCh, errCh := i.client.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
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

	out, err := i.client.ContainerLogs(ctx, resp.ID, container.LogsOptions{
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
