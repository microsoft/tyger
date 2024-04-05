package dockerinstall

import (
	"archive/tar"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base32"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"io"
	"os"
	"path"
	"strconv"
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
	defaultPostgresImage = "postgres"

	containerConfigHashLabel = "tyger-container-config-hash"

	databaseDockerContainerName     = "tyger-db"
	dataPlaneDockerContainerName    = "tyger-data-plane"
	controlPlaneDockerContainerName = "tyger-control-plane"

	databaseDockerVolumeName = "tyger-db"
	buffersDockerVolumeName  = "tyger-buffers"
	runLogsDockerVolumeName  = "tyger-run-logs"
)

func InstallTygerInDocker(ctx context.Context) error {
	config := install.GetDockerEnvironmentConfigFromContext(ctx)

	if err := ensureDirectoryExists("/opt/tyger", config); err != nil {
		return err
	}

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("error creating docker client: %w", err)
	}

	if err := createDatabaseContainer(ctx, dockerClient, config); err != nil {
		return fmt.Errorf("error creating database container: %w", err)
	}

	if err := createDataPlaneContainer(ctx, dockerClient, config); err != nil {
		return fmt.Errorf("error creating data plane container: %w", err)
	}

	if err := runMigrationRunnerInDockerfunc(ctx, dockerClient, config); err != nil {
		return fmt.Errorf("error running migration runner: %w", err)
	}

	if err := createControlPlaneContainer(ctx, dockerClient, config); err != nil {
		return fmt.Errorf("error creating control plane container: %w", err)
	}

	return nil
}

func ensureDirectoryExists(path string, config *install.DockerEnvironmentConfig) error {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		if err := os.MkdirAll(path, 0755); err != nil {
			return fmt.Errorf("error creating directory %s: %w", path, err)
		}

		return os.Chown(path, config.GetUserIdInt(), config.GetGroupIdInt())
	} else {
		return err
	}
}

func createControlPlaneContainer(ctx context.Context, dockerClient *client.Client, config *install.DockerEnvironmentConfig) error {
	if err := ensureVolumeCreated(ctx, dockerClient, runLogsDockerVolumeName); err != nil {
		return err
	}

	if err := ensureDirectoryExists("/opt/tyger/control-plane", config); err != nil {
		return err
	}

	if err := pullImage(ctx, dockerClient, config.BufferSidecarImage, false); err != nil {
		return fmt.Errorf("error pulling buffer sidecar image: %w", err)
	}

	image := config.ControlPlaneImage

	primaryPublicCertificatePath := "/app/tyger-data-plane-primary.pem"
	secondaryPublicCertificatePath := "/app/tyger-data-plane-secondary.pem"
	if config.SigningKeys.Secondary == nil {
		secondaryPublicCertificatePath = ""
	}

	desiredContainerConfig := container.Config{
		Image: image,
		User:  fmt.Sprintf("%d:%d", config.GetUserIdInt(), config.GetGroupIdInt()),
		Env: []string{
			"Urls=http://unix:/opt/tyger/control-plane/tyger.sock",
			"SocketPermissions=660",
			"Auth__Enabled=false",
			"Compute__Docker__RunSecretsPath=/opt/tyger/control-plane/run-secrets/",
			"Compute__Docker__EphemeralBuffersPath=/opt/tyger/control-plane/ephemeral-buffers/",
			"LogArchive__LocalStorage__LogsDirectory=/app/logs",
			"Buffers__BufferSidecarImage=" + config.BufferSidecarImage,
			"Buffers__LocalStorage__DataPlaneEndpoint=http+unix:///opt/tyger/data-plane/tyger.data.sock",
			"Buffers__PrimarySigningPrivateKeyPath=" + primaryPublicCertificatePath,
			"Buffers__SecondarySigningPrivateKeyPath=" + secondaryPublicCertificatePath,
			"Database__ConnectionString=Host=/opt/tyger/database; Username=tyger-server",
			"Database__TygerServerRoleName=tyger-server",
		},
		Healthcheck: &container.HealthConfig{
			Test: []string{
				"CMD",
				"/app/bin/curl",
				"--fail",
				"--unix", "/opt/tyger/control-plane/tyger.sock",
				"http://local/healthcheck",
			},
			StartInterval: 2 * time.Second,
			Interval:      10 * time.Second,
		},
	}

	desiredHostConfig := container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   "volume",
				Source: runLogsDockerVolumeName,
				Target: "/app/logs",
			},
			{
				Type:   "bind",
				Source: "/opt/tyger/control-plane",
				Target: "/opt/tyger/control-plane",
			},
			{
				Type:   "bind",
				Source: "/opt/tyger/data-plane",
				Target: "/opt/tyger/data-plane",
			},
			{
				Type:   "bind",
				Source: "/opt/tyger/database",
				Target: "/opt/tyger/database",
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
	}

	// See if there is a group that has access to the docker socket.
	// If there is, add that group to the container.
	_, dockerSocketGroupId, dockerSocketPerms, err := statDockerSocket(ctx, dockerClient)
	if err != nil {
		return fmt.Errorf("error statting docker socket: %w", err)
	}

	if dockerSocketPerms&0060 == 0060 {
		desiredHostConfig.GroupAdd = append(desiredHostConfig.GroupAdd, strconv.Itoa(dockerSocketGroupId))
	}

	desiredNetworkingConfig := network.NetworkingConfig{}

	postCreateAction := func(containerName string) error {
		tarFs := memfs.New()

		primaryPemBytes, err := os.ReadFile(config.SigningKeys.Primary.PrivateKey)
		if err != nil {
			return err
		}

		tarFs.WriteFile(path.Base(primaryPublicCertificatePath), primaryPemBytes, 0777)

		if config.SigningKeys.Secondary != nil {
			secondaryPemBytes, err := os.ReadFile(config.SigningKeys.Primary.PrivateKey)
			if err != nil {
				return err
			}

			tarFs.WriteFile(path.Base(secondaryPublicCertificatePath), secondaryPemBytes, 0777)
		}

		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		tw.AddFS(tarFs)
		tw.Close()

		return dockerClient.CopyToContainer(ctx, containerName, "/app", buf, types.CopyToContainerOptions{})
	}

	if err := createContainer(
		ctx,
		dockerClient,
		controlPlaneDockerContainerName,
		&desiredContainerConfig, &desiredHostConfig, &desiredNetworkingConfig,
		true,
		postCreateAction); err != nil {
		return err
	}

	if err := os.Symlink("/opt/tyger/control-plane/tyger.sock", "/opt/tyger/api.sock"); err != nil && !os.IsExist(err) {
		return fmt.Errorf("error creating symlink: %w", err)
	}

	return nil
}

func runMigrationRunnerInDockerfunc(ctx context.Context, dockerClient *client.Client, config *install.DockerEnvironmentConfig) error {
	desiredContainerConfig := container.Config{
		Image: config.ControlPlaneImage,
		User:  fmt.Sprintf("%d:%d", config.GetUserIdInt(), config.GetGroupIdInt()),
		Env: []string{
			"Urls=http://unix:/opt/tyger/control-plane/tyger.sock",
			"Database__ConnectionString=Host=/opt/tyger/database; Username=tyger-server",
			"Database__AutoMigrate=true",
			"Database__TygerServerRoleName=tyger-server",
			"Compute__Docker__Enabled=true",
		},
		Cmd: []string{"database", "init"},
	}

	desiredHostConfig := container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   "bind",
				Source: "/opt/tyger/",
				Target: "/opt/tyger/",
			},
		},
		NetworkMode: "none",
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyDisabled,
		},
	}

	containerName := "tyger-migration-runner"
	if err := createContainer(ctx, dockerClient, containerName, &desiredContainerConfig, &desiredHostConfig, &network.NetworkingConfig{}, false); err != nil {
		return fmt.Errorf("error creating migration runner container: %w", err)
	}

	defer func() {
		if err := dockerClient.ContainerRemove(ctx, containerName, container.RemoveOptions{Force: true}); err != nil {
			panic(err)
		}
	}()

	if err := dockerClient.ContainerStart(ctx, containerName, container.StartOptions{}); err != nil {
		return err
	}

	// Wait for the container to finish
	statusCh, errCh := dockerClient.ContainerWait(ctx, containerName, container.WaitConditionNotRunning)
	select {
	case err := <-errCh:
		if err != nil {
			return err
		}
	case waitReponse := <-statusCh:
		if waitReponse.StatusCode == 0 {
			return nil
		}
	}

	out, err := dockerClient.ContainerLogs(ctx, containerName, container.LogsOptions{
		ShowStdout: true,
		ShowStderr: true,
	})

	if err != nil {
		return err
	}

	defer out.Close()

	// Read the output
	stdOutput, errOutput := &bytes.Buffer{}, &bytes.Buffer{}
	_, err = stdcopy.StdCopy(stdOutput, errOutput, out)
	if err != nil {
		return err
	}

	return fmt.Errorf("migration runner failed: %s", errOutput.String())
}

func UninstallTygerInDocker(ctx context.Context) error {
	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("error creating docker client: %w", err)
	}

	if err := removeContainer(ctx, dockerClient, databaseDockerContainerName); err != nil {
		return fmt.Errorf("error removing database container: %w", err)
	}

	if err := removeContainer(ctx, dockerClient, dataPlaneDockerContainerName); err != nil {
		return fmt.Errorf("error removing data plane container: %w", err)
	}

	if err := removeContainer(ctx, dockerClient, controlPlaneDockerContainerName); err != nil {
		return fmt.Errorf("error removing control plane container: %w", err)
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
		if err := removeContainer(ctx, dockerClient, runContainer.ID); err != nil {
			return fmt.Errorf("error removing run container: %w", err)
		}
	}

	entries, err := os.ReadDir("/opt/tyger")
	if err != nil {
		return fmt.Errorf("error reading /opt/tyger: %w", err)
	}

	for _, entry := range entries {
		path := path.Join("/opt/tyger", entry.Name())
		if err := os.RemoveAll(path); err != nil {
			return fmt.Errorf("error removing %s: %w", path, err)
		}
	}

	return nil
}

func createDatabaseContainer(ctx context.Context, dockerClient *client.Client, config *install.DockerEnvironmentConfig) error {
	if err := ensureVolumeCreated(ctx, dockerClient, databaseDockerVolumeName); err != nil {
		return err
	}

	if err := ensureDirectoryExists("/opt/tyger/database", config); err != nil {
		return err
	}

	image := config.PostgresImage
	if image == "" {
		image = defaultPostgresImage
	}

	desiredContainerConfig := container.Config{
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
	}
	desiredHostConfig := container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   "volume",
				Source: databaseDockerVolumeName,
				Target: "/var/lib/postgresql/data",
			},
			{
				Type:   "bind",
				Source: "/opt/tyger/database",
				Target: "/var/run/postgresql/",
			},
		},
		NetworkMode: "none",
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyUnlessStopped,
		},
	}
	desiredNetworkingConfig := network.NetworkingConfig{}

	if err := createContainer(ctx, dockerClient, databaseDockerContainerName, &desiredContainerConfig, &desiredHostConfig, &desiredNetworkingConfig, true); err != nil {
		return err
	}

	return nil
}

func createDataPlaneContainer(ctx context.Context, dockerClient *client.Client, config *install.DockerEnvironmentConfig) error {
	if err := ensureVolumeCreated(ctx, dockerClient, buffersDockerVolumeName); err != nil {
		return err
	}

	if err := ensureDirectoryExists("/opt/tyger/data-plane", config); err != nil {
		return err
	}

	image := config.DataPlaneImage
	if image == "" {
		image = "eminence.azurecr.io/tyger-data-plane-server:dev"
	}

	primaryPublicCertificatePath := "/app/tyger-data-plane-public-primary.pem"
	secondaryPublicCertificatePath := "/app/tyger-data-plane-public-secondary.pem"
	if config.SigningKeys.Secondary == nil {
		secondaryPublicCertificatePath = ""
	}

	desiredContainerConfig := container.Config{
		Image: image,
		User:  config.UserId,
		Env: []string{
			"Urls=http://unix:/opt/tyger/data-plane/tyger.data.sock",
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
				"--unix", "/opt/tyger/data-plane/tyger.data.sock",
				"http://local/healthcheck",
			},
			StartInterval: 2 * time.Second,
			Interval:      10 * time.Second,
		},
	}

	desiredHostConfig := container.HostConfig{
		Mounts: []mount.Mount{
			{
				Type:   "volume",
				Source: buffersDockerVolumeName,
				Target: "/app/data",
			},
			{
				Type:   "bind",
				Source: "/opt/tyger/data-plane",
				Target: "/opt/tyger/data-plane",
			},
		},
		NetworkMode: "none",
		RestartPolicy: container.RestartPolicy{
			Name: container.RestartPolicyUnlessStopped,
		},
	}
	desiredNetworkingConfig := network.NetworkingConfig{}

	postCreateAction := func(containerName string) error {
		tarFs := memfs.New()

		publicPemBytes, err := os.ReadFile(config.SigningKeys.Primary.PublicKey)
		if err != nil {
			return fmt.Errorf("error reading primary public key: %w", err)
		}

		tarFs.WriteFile(path.Base(primaryPublicCertificatePath), publicPemBytes, 0777)

		if config.SigningKeys.Secondary != nil {
			publicPemBytes, err = os.ReadFile(config.SigningKeys.Secondary.PublicKey)
			if err != nil {
				return fmt.Errorf("error reading secondary public key: %w", err)
			}

			tarFs.WriteFile(path.Base(secondaryPublicCertificatePath), publicPemBytes, 0777)
		}

		buf := &bytes.Buffer{}
		tw := tar.NewWriter(buf)
		tw.AddFS(tarFs)
		tw.Close()

		return dockerClient.CopyToContainer(ctx, containerName, "/app", buf, types.CopyToContainerOptions{})
	}

	if err := createContainer(
		ctx,
		dockerClient,
		dataPlaneDockerContainerName,
		&desiredContainerConfig, &desiredHostConfig, &desiredNetworkingConfig,
		true,
		postCreateAction); err != nil {
		return err
	}

	return nil
}

func getPublicCertificatePemBytes(pemPath string) ([]byte, error) {
	pemBytes, err := os.ReadFile(pemPath)
	if err != nil {
		return nil, fmt.Errorf("error reading certificate at '%s': %w", pemPath, err)
	}

	var publicBlock *pem.Block
	for {
		// Decode a block
		var block *pem.Block
		block, pemBytes = pem.Decode(pemBytes)
		if block == nil {
			return nil, fmt.Errorf("no certificate block found in %s", pemPath)
		}

		if block.Type == "CERTIFICATE" {
			publicBlock = block
			break
		}
	}

	return pem.EncodeToMemory(publicBlock), nil
}

func createContainer(
	ctx context.Context,
	dockerClient *client.Client,
	containerName string,
	desiredContainerConfig *container.Config,
	desiredHostConfig *container.HostConfig,
	desiredNetworkingConfig *network.NetworkingConfig,
	waitForHealthy bool,
	postCreateActions ...func(containerName string) error,
) error {
	configHash := computeContainerConfigHash(desiredContainerConfig, desiredHostConfig, desiredNetworkingConfig)
	desiredContainerConfig.Labels = map[string]string{
		containerConfigHashLabel: configHash,
	}

	containerExists := true
	existingContainer, err := dockerClient.ContainerInspect(ctx, containerName)
	if err != nil {
		if !client.IsErrNotFound(err) {
			return fmt.Errorf("error checking for container: %w", err)
		}

		containerExists = false
	}

	if containerExists && (existingContainer.Config.Labels[containerConfigHashLabel] != configHash || !existingContainer.State.Running) {
		if err := removeContainer(ctx, dockerClient, containerName); err != nil {
			return fmt.Errorf("error removing existing container: %w", err)
		}

		containerExists = false
	}

	if !containerExists {
		containerImage := desiredContainerConfig.Image
		if err := pullImage(ctx, dockerClient, containerImage, false); err != nil {
			return fmt.Errorf("error pulling image: %w", err)
		}

		resp, err := dockerClient.ContainerCreate(ctx, desiredContainerConfig, desiredHostConfig, desiredNetworkingConfig, nil, containerName)

		if err != nil {
			return fmt.Errorf("error creating container: %w", err)
		}

		for _, a := range postCreateActions {
			if err := a(containerName); err != nil {
				return fmt.Errorf("error running post-create action: %w", err)
			}
		}

		if err := dockerClient.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
			return fmt.Errorf("error starting container: %w", err)
		}
	}

	if waitForHealthy {
		waitStartTime := time.Now()

		for {
			c, err := dockerClient.ContainerInspect(ctx, containerName)
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

func pullImage(ctx context.Context, dockerClient *client.Client, containerImage string, always bool) error {
	if !always {
		_, _, err := dockerClient.ImageInspectWithRaw(ctx, containerImage)
		if err == nil {
			return nil
		}
	}

	reader, err := dockerClient.ImagePull(ctx, containerImage, image.PullOptions{})
	if err != nil {
		return fmt.Errorf("error pulling image: %w", err)
	}

	defer reader.Close()
	log.Info().Msgf("Pulling image %s", containerImage)
	io.Copy(io.Discard, reader)
	log.Info().Msgf("Done pulling image %s", containerImage)

	return nil
}

func computeContainerConfigHash(desiredConfig *container.Config, desiredHostConfig *container.HostConfig, desiredNetworkingConfig *network.NetworkingConfig) string {
	combinedDesiredConfig := struct {
		Config  *container.Config
		Host    *container.HostConfig
		Network *network.NetworkingConfig
	}{
		Config:  desiredConfig,
		Host:    desiredHostConfig,
		Network: desiredNetworkingConfig,
	}

	desiredBytes, err := json.Marshal(combinedDesiredConfig)
	if err != nil {
		panic(err)
	}

	hashBytes := sha256.Sum256(desiredBytes)
	return base32.StdEncoding.EncodeToString(hashBytes[:])
}

func removeContainer(ctx context.Context, dockerClient *client.Client, containerName string) error {
	if err := dockerClient.ContainerStop(ctx, containerName, container.StopOptions{}); err != nil {
		if client.IsErrNotFound(err) {
			return nil
		}
		return fmt.Errorf("error stopping container: %w", err)
	}

	if err := dockerClient.ContainerRemove(ctx, containerName, container.RemoveOptions{
		Force: true,
	}); err != nil {
		return fmt.Errorf("error removing container: %w", err)
	}

	return nil
}

func ensureVolumeCreated(ctx context.Context, dockerClient *client.Client, volumeName string) error {
	if _, err := dockerClient.VolumeInspect(ctx, volumeName); err != nil {
		if client.IsErrNotFound(err) {
			if _, err := dockerClient.VolumeCreate(ctx, volume.CreateOptions{
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

func statDockerSocket(ctx context.Context, dockerClient *client.Client) (userId int, groupId int, permissions int, err error) {
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

	if err := pullImage(ctx, dockerClient, containerConfig.Image, false); err != nil {
		return 0, 0, 0, fmt.Errorf("error pulling image: %w", err)
	}

	resp, err := dockerClient.ContainerCreate(ctx, containerConfig, hostConfig, nil, nil, "")
	if err != nil {
		return 0, 0, 0, err
	}

	defer func() {
		if err := dockerClient.ContainerRemove(ctx, resp.ID, container.RemoveOptions{Force: true}); err != nil {
			panic(err)
		}
	}()

	if err := dockerClient.ContainerStart(ctx, resp.ID, container.StartOptions{}); err != nil {
		return 0, 0, 0, err
	}

	// Wait for the container to finish
	statusCh, errCh := dockerClient.ContainerWait(ctx, resp.ID, container.WaitConditionNotRunning)
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

	out, err := dockerClient.ContainerLogs(ctx, resp.ID, container.LogsOptions{
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
