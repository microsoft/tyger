// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dockerinstall

import (
	"context"
	"fmt"
	"net"
	"os/user"
	"regexp"
	"strconv"

	"github.com/a8m/envsubst"
	"github.com/microsoft/tyger/cli/internal/common"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
)

var (
	NameRegex = regexp.MustCompile(`^[a-z][a-z\-0-9]{1,23}$`)

	DefaultEnvironmentName  = "local"
	DefaultInstallationPath = "/opt/tyger"
	DefaultPostgresImage    = "postgres:16.2"
	DefaultMarinerImage     = "mcr.microsoft.com/azurelinux/base/core:3.0"

	InstallationPathMaxLength = 70
)

func (envConfig *DockerEnvironmentConfig) QuickValidateConfig(ctx context.Context) error {
	success := true

	if envConfig.EnvironmentName == "" {
		envConfig.EnvironmentName = DefaultEnvironmentName
	} else if !NameRegex.MatchString(envConfig.EnvironmentName) {
		validationError(ctx, &success, "The `environmentName` field must match the pattern %s", NameRegex)
	}

	if envConfig.InstallationPath == "" {
		envConfig.InstallationPath = DefaultInstallationPath
	} else if envConfig.InstallationPath[len(envConfig.InstallationPath)-1] == '/' {
		envConfig.InstallationPath = envConfig.InstallationPath[:len(envConfig.InstallationPath)-1]
	}

	if len(envConfig.InstallationPath) > InstallationPathMaxLength {
		validationError(ctx, &success, "The `installationPath` field must be at most %d characters long", InstallationPathMaxLength)
	}

	if _, err := strconv.Atoi(envConfig.UserId); err != nil {
		if envConfig.UserId == "" {
			currentUser, err := user.Current()
			if err != nil {
				validationError(ctx, &success, "Unable to determine the current user for the `userId` field")
			} else {
				envConfig.UserId = currentUser.Uid
			}
		} else {
			u, err := user.Lookup(envConfig.UserId)
			if err != nil {
				validationError(ctx, &success, "The `userId` field must be a valid user ID or name")
			} else {
				envConfig.UserId = u.Uid
			}
		}
	}

	if _, err := strconv.Atoi(envConfig.AllowedGroupId); err != nil {
		if envConfig.AllowedGroupId == "" {
			currentUser, err := user.Current()
			if err != nil {
				validationError(ctx, &success, "Unable to determine the current user for the `userId` field")
			} else {
				envConfig.AllowedGroupId = currentUser.Gid
			}
		} else {
			g, err := user.LookupGroup(envConfig.AllowedGroupId)
			if err != nil {
				validationError(ctx, &success, "The `groupId` field must be a valid group ID or name")
			} else {
				envConfig.AllowedGroupId = g.Gid
			}
		}
	}

	if envConfig.SigningKeys.Primary == nil {
		validationError(ctx, &success, "The `signingKeys.primary` field is required")
	} else {
		if envConfig.SigningKeys.Primary.PublicKey == "" {
			validationError(ctx, &success, "The `signingKeys.primary.publicKey` field is required to be the path to a public key file PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(envConfig.SigningKeys.Primary.PublicKey, true, false); err != nil {
				validationError(ctx, &success, "Error expanding `signingKeys.primary.publicKey`: %s", err)
			} else {
				envConfig.SigningKeys.Primary.PublicKey = expanded
			}
		}
		if envConfig.SigningKeys.Primary.PrivateKey == "" {
			validationError(ctx, &success, "The `signingKeys.primary.privateKey` field is required to be the path to a private key PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(envConfig.SigningKeys.Primary.PrivateKey, true, false); err != nil {
				validationError(ctx, &success, "Error expanding `signingKeys.primary.privateKey`: %s", err)
			} else {
				envConfig.SigningKeys.Primary.PrivateKey = expanded
			}
		}
	}

	if envConfig.SigningKeys.Secondary != nil {
		if envConfig.SigningKeys.Secondary.PublicKey == "" {
			validationError(ctx, &success, "The `signingKeys.secondary.publicKey` field is required to be the path to a public key PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(envConfig.SigningKeys.Secondary.PublicKey, true, false); err != nil {
				validationError(ctx, &success, "Error expanding `signingKeys.secondary.publicKey`: %s", err)
			} else {
				envConfig.SigningKeys.Secondary.PublicKey = expanded
			}
		}
		if envConfig.SigningKeys.Secondary.PrivateKey == "" {
			validationError(ctx, &success, "The `signingKeys.secondary.privateKey` field is required to be the path to a private key PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(envConfig.SigningKeys.Secondary.PrivateKey, true, false); err != nil {
				validationError(ctx, &success, "Error expanding `signingKeys.secondary.privateKey`: %s", err)
			} else {
				envConfig.SigningKeys.Secondary.PrivateKey = expanded
			}
		}
	}

	if envConfig.PostgresImage == "" {
		envConfig.PostgresImage = DefaultPostgresImage
	}
	if envConfig.MarinerImage == "" {
		envConfig.MarinerImage = DefaultMarinerImage
	}
	if envConfig.ControlPlaneImage == "" {
		envConfig.ControlPlaneImage = defaultImage("tyger-server")
	}
	if envConfig.DataPlaneImage == "" {
		envConfig.DataPlaneImage = defaultImage("tyger-data-plane-server")
	}
	if envConfig.BufferSidecarImage == "" {
		envConfig.BufferSidecarImage = defaultImage("buffer-sidecar")
	}
	if envConfig.GatewayImage == "" {
		envConfig.GatewayImage = defaultImage("tyger-cli")
	}

	if envConfig.UseGateway == nil {
		useGateway := defaultUseGateway()
		envConfig.UseGateway = &useGateway
	}

	if envConfig.Network != nil {
		if envConfig.Network.Subnet != "" {
			if _, _, err := net.ParseCIDR(envConfig.Network.Subnet); err != nil {
				validationError(ctx, &success, "The `network.subnet` field must be a valid CIDR block if specified")
			}
		}
	}

	if envConfig.Buffers == nil {
		envConfig.Buffers = &BuffersConfig{}
	}
	buffersConfig := envConfig.Buffers
	if buffersConfig.ActiveLifetime == "" {
		buffersConfig.ActiveLifetime = "0.00:00"
	}
	if buffersConfig.SoftDeletedLifetime == "" {
		buffersConfig.SoftDeletedLifetime = "1.00:00"
	}

	if _, err := common.ParseTimeToLive(buffersConfig.ActiveLifetime); err != nil {
		validationError(ctx, &success, "The `buffers.activeLifetime` field must be a valid TTL (D.HH:MM:SS)")
	}

	if _, err := common.ParseTimeToLive(buffersConfig.SoftDeletedLifetime); err != nil {
		validationError(ctx, &success, "The `buffers.softDeletedLifetime` field must be a valid TTL (D.HH:MM:SS)")
	}

	if success {
		return nil
	}

	return install.ErrAlreadyLoggedError
}

func defaultImage(repo string) string {
	return fmt.Sprintf("%s%s%s:%s", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory(), repo, install.ContainerImageTag)
}

func validationError(ctx context.Context, success *bool, format string, args ...any) {
	*success = false
	log.Ctx(ctx).Error().Msgf(format, args...)
}
