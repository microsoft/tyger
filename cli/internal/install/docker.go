package install

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
	"os/user"
	"path"
	"time"

	"github.com/docker/docker/api/types"
	"github.com/docker/docker/api/types/container"
	"github.com/docker/docker/api/types/image"
	"github.com/docker/docker/api/types/mount"
	"github.com/docker/docker/api/types/network"
	"github.com/docker/docker/api/types/volume"
	"github.com/docker/docker/client"
	"github.com/psanford/memfs"
	"github.com/rs/zerolog/log"
)

const (
	defaultPostgresImage = "postgres"

	containerConfigHashLabel = "tyger-container-config-hash"
)

func InstallTygerInDocker(ctx context.Context) error {
	config := GetDockerEnvironmentConfigFromContext(ctx)

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

	if err := createControlPlaneContainer(ctx, dockerClient, config); err != nil {
		return fmt.Errorf("error creating control plane container: %w", err)
	}

	return nil
}

func createControlPlaneContainer(ctx context.Context, dockerClient *client.Client, config *DockerEnvironmentConfig) error {
	if err := ensureVolumeCreated(ctx, dockerClient, runLogsDockerVolumeName(config)); err != nil {
		return err
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

func UninstallTygerInDocker(ctx context.Context) error {
	config := GetDockerEnvironmentConfigFromContext(ctx)

	dockerClient, err := client.NewClientWithOpts(client.FromEnv, client.WithAPIVersionNegotiation())
	if err != nil {
		return fmt.Errorf("error creating docker client: %w", err)
	}

	if err := removeContainer(ctx, dockerClient, databaseDockerContainerName(config)); err != nil {
		return fmt.Errorf("error removing database container: %w", err)
	}

	if err := removeContainer(ctx, dockerClient, dataPlaneDockerContainerName(config)); err != nil {
		return fmt.Errorf("error removing data plane container: %w", err)
	}

	return nil
}

func createDatabaseContainer(ctx context.Context, dockerClient *client.Client, config *DockerEnvironmentConfig) error {
	if err := ensureVolumeCreated(ctx, dockerClient, databaseDockerVolumeName(config)); err != nil {
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
				Source: databaseDockerVolumeName(config),
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

	if err := createContainer(ctx, dockerClient, databaseDockerContainerName(config), &desiredContainerConfig, &desiredHostConfig, &desiredNetworkingConfig); err != nil {
		return err
	}

	return nil
}

func createDataPlaneContainer(ctx context.Context, dockerClient *client.Client, config *DockerEnvironmentConfig) error {
	if err := ensureVolumeCreated(ctx, dockerClient, buffersDockerVolumeName(config)); err != nil {
		return err
	}

	image := config.DataPlaneImage
	if image == "" {
		image = "eminence.azurecr.io/tyger-data-plane-server:dev"
	}

	primaryPublicCertificatePath := "/app/tyger-data-plane-public-primary.pem"
	secondaryPublicCertificatePath := "/app/tyger-data-plane-public-secondary.pem"
	if config.DataPlaneSecondarySigningCertificate == "" {
		secondaryPublicCertificatePath = ""
	}

	currentUser, err := user.Current()
	if err != nil {
		return fmt.Errorf("error getting current user: %w", err)
	}

	desiredContainerConfig := container.Config{
		Image: image,
		User:  currentUser.Uid,
		Env: []string{
			"Urls=http://unix:/opt/tyger/data-plane/tyger.data.sock",
			"Auth__Enabled=false",
			"DataDirectory=/app/data",
			"PrimarySigningPublicCertificatePath=" + primaryPublicCertificatePath,
			"SecondarySigningPublicCertificatePath=" + secondaryPublicCertificatePath,
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
				Source: buffersDockerVolumeName(config),
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

		publicPemBytes, err := getPublicCertificatePemBytes(config.DataPlanePrimarySigningCertificate)
		if err != nil {
			return err
		}

		tarFs.WriteFile(path.Base(primaryPublicCertificatePath), publicPemBytes, 0777)

		if config.DataPlaneSecondarySigningCertificate != "" {
			publicPemBytes, err = getPublicCertificatePemBytes(config.DataPlaneSecondarySigningCertificate)
			if err != nil {
				return err
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
		dataPlaneDockerContainerName(config),
		&desiredContainerConfig, &desiredHostConfig, &desiredNetworkingConfig,
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

func databaseDockerContainerName(config *DockerEnvironmentConfig) string {
	return fmt.Sprintf("tyger-%s-db", config.EnvironmentName)
}

func dataPlaneDockerContainerName(config *DockerEnvironmentConfig) string {
	return fmt.Sprintf("tyger-%s-data-plane", config.EnvironmentName)
}

func controlPlaneDockerContainerName(config *DockerEnvironmentConfig) string {
	return fmt.Sprintf("tyger-%s-control-plane", config.EnvironmentName)
}

func databaseDockerVolumeName(config *DockerEnvironmentConfig) string {
	return fmt.Sprintf("tyger-%s-db", config.EnvironmentName)
}

func buffersDockerVolumeName(config *DockerEnvironmentConfig) string {
	return fmt.Sprintf("tyger-%s-buffers", config.EnvironmentName)
}

func runLogsDockerVolumeName(config *DockerEnvironmentConfig) string {
	return fmt.Sprintf("tyger-%s-run-logs", config.EnvironmentName)
}
