// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dockerinstall

import (
	"fmt"
	"net"
	"os/user"
	"regexp"
	"strconv"

	"github.com/a8m/envsubst"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/rs/zerolog/log"
)

var (
	NameRegex = regexp.MustCompile(`^[a-z][a-z\-0-9]{1,23}$`)

	DefaultEnvironmentName = "local"
	DefaultPostgresImage   = "postgres:16.2"
	DefaultMarinerImage    = "mcr.microsoft.com/azurelinux/base/core:3.0"
)

func (inst *Installer) QuickValidateConfig() bool {
	success := true

	if inst.Config.EnvironmentName == "" {
		inst.Config.EnvironmentName = DefaultEnvironmentName
	} else if !NameRegex.MatchString(inst.Config.EnvironmentName) {
		validationError(&success, "The `environmentName` field must match the pattern "+NameRegex.String())
	}

	if inst.Config.InstallationPath == "" {
		inst.Config.InstallationPath = "/opt/tyger"
	} else if inst.Config.InstallationPath[len(inst.Config.InstallationPath)-1] == '/' {
		inst.Config.InstallationPath = inst.Config.InstallationPath[:len(inst.Config.InstallationPath)-1]
	}

	if _, err := strconv.Atoi(inst.Config.UserId); err != nil {
		if inst.Config.UserId == "" {
			currentUser, err := user.Current()
			if err != nil {
				validationError(&success, "Unable to determine the current user for the `userId` field")
			} else {
				inst.Config.UserId = currentUser.Uid
			}
		} else {
			u, err := user.Lookup(inst.Config.UserId)
			if err != nil {
				validationError(&success, "The `userId` field must be a valid user ID or name")
			} else {
				inst.Config.UserId = u.Uid
			}
		}
	}

	if _, err := strconv.Atoi(inst.Config.AllowedGroupId); err != nil {
		if inst.Config.AllowedGroupId == "" {
			currentUser, err := user.Current()
			if err != nil {
				validationError(&success, "Unable to determine the current user for the `userId` field")
			} else {
				inst.Config.AllowedGroupId = currentUser.Gid
			}
		} else {
			g, err := user.LookupGroup(inst.Config.AllowedGroupId)
			if err != nil {
				validationError(&success, "The `groupId` field must be a valid group ID or name")
			} else {
				inst.Config.AllowedGroupId = g.Gid
			}
		}
	}

	if inst.Config.SigningKeys.Primary == nil {
		validationError(&success, "The `signingKeys.primary` field is required")
	} else {
		if inst.Config.SigningKeys.Primary.PublicKey == "" {
			validationError(&success, "The `signingKeys.primary.publicKey` field is required to be the path to a public key file PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(inst.Config.SigningKeys.Primary.PublicKey, true, false); err != nil {
				validationError(&success, fmt.Sprintf("Error expanding `signingKeys.primary.publicKey`: %s", err))
			} else {
				inst.Config.SigningKeys.Primary.PublicKey = expanded
			}
		}
		if inst.Config.SigningKeys.Primary.PrivateKey == "" {
			validationError(&success, "The `signingKeys.primary.privateKey` field is required to be the path to a private key PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(inst.Config.SigningKeys.Primary.PrivateKey, true, false); err != nil {
				validationError(&success, fmt.Sprintf("Error expanding `signingKeys.primary.privateKey`: %s", err))
			} else {
				inst.Config.SigningKeys.Primary.PrivateKey = expanded
			}
		}
	}

	if inst.Config.SigningKeys.Secondary != nil {
		if inst.Config.SigningKeys.Secondary.PublicKey == "" {
			validationError(&success, "The `signingKeys.secondary.publicKey` field is required to be the path to a public key PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(inst.Config.SigningKeys.Secondary.PublicKey, true, false); err != nil {
				validationError(&success, fmt.Sprintf("Error expanding `signingKeys.secondary.publicKey`: %s", err))
			} else {
				inst.Config.SigningKeys.Secondary.PublicKey = expanded
			}
		}
		if inst.Config.SigningKeys.Secondary.PrivateKey == "" {
			validationError(&success, "The `signingKeys.secondary.privateKey` field is required to be the path to a private key PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(inst.Config.SigningKeys.Secondary.PrivateKey, true, false); err != nil {
				validationError(&success, fmt.Sprintf("Error expanding `signingKeys.secondary.privateKey`: %s", err))
			} else {
				inst.Config.SigningKeys.Secondary.PrivateKey = expanded
			}
		}
	}

	if inst.Config.PostgresImage == "" {
		inst.Config.PostgresImage = DefaultPostgresImage
	}
	if inst.Config.MarinerImage == "" {
		inst.Config.MarinerImage = DefaultMarinerImage
	}
	if inst.Config.ControlPlaneImage == "" {
		inst.Config.ControlPlaneImage = defaultImage("tyger-server")
	}
	if inst.Config.DataPlaneImage == "" {
		inst.Config.DataPlaneImage = defaultImage("tyger-data-plane-server")
	}
	if inst.Config.BufferSidecarImage == "" {
		inst.Config.BufferSidecarImage = defaultImage("buffer-sidecar")
	}
	if inst.Config.GatewayImage == "" {
		inst.Config.GatewayImage = defaultImage("tyger-cli")
	}

	if inst.Config.UseGateway == nil {
		useGateway := defaultUseGateway()
		inst.Config.UseGateway = &useGateway
	}

	if inst.Config.Network != nil {
		if inst.Config.Network.Subnet != "" {
			if _, _, err := net.ParseCIDR(inst.Config.Network.Subnet); err != nil {
				validationError(&success, "The `network.subnet` field must be a valid CIDR block if specified")
			}
		}
	}

	return success
}

func defaultImage(repo string) string {
	return fmt.Sprintf("%s%s%s:%s", install.ContainerRegistry, install.GetNormalizedContainerRegistryDirectory(), repo, install.ContainerImageTag)
}

func validationError(success *bool, format string, args ...any) {
	*success = false
	log.Error().Msgf(format, args...)
}
