// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dockerinstall

import (
	"fmt"
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
)

func (i *Installer) QuickValidateConfig() bool {
	success := true

	if i.Config.EnvironmentName == "" {
		i.Config.EnvironmentName = DefaultEnvironmentName
	} else if !NameRegex.MatchString(i.Config.EnvironmentName) {
		validationError(&success, "The `environmentName` field must match the pattern "+NameRegex.String())
	}

	if i.Config.InstallationPath == "" {
		i.Config.InstallationPath = "/opt/tyger"
	} else if i.Config.InstallationPath[len(i.Config.InstallationPath)-1] == '/' {
		i.Config.InstallationPath = i.Config.InstallationPath[:len(i.Config.InstallationPath)-1]
	}

	if _, err := strconv.Atoi(i.Config.UserId); err != nil {
		if i.Config.UserId == "" {
			currentUser, err := user.Current()
			if err != nil {
				validationError(&success, "Unable to determine the current user for the `userId` field")
			} else {
				i.Config.UserId = currentUser.Uid
			}
		} else {
			u, err := user.Lookup(i.Config.UserId)
			if err != nil {
				validationError(&success, "The `userId` field must be a valid user ID or name")
			} else {
				i.Config.UserId = u.Uid
			}
		}
	}

	if _, err := strconv.Atoi(i.Config.AllowedGroupId); err != nil {
		if i.Config.AllowedGroupId == "" {
			currentUser, err := user.Current()
			if err != nil {
				validationError(&success, "Unable to determine the current user for the `userId` field")
			} else {
				i.Config.AllowedGroupId = currentUser.Gid
			}
		} else {
			g, err := user.LookupGroup(i.Config.AllowedGroupId)
			if err != nil {
				validationError(&success, "The `groupId` field must be a valid group ID or name")
			} else {
				i.Config.AllowedGroupId = g.Gid
			}
		}
	}

	if i.Config.SigningKeys.Primary == nil {
		validationError(&success, "The `signingKeys.primary` field is required")
	} else {
		if i.Config.SigningKeys.Primary.PublicKey == "" {
			validationError(&success, "The `signingKeys.primary.publicKey` field is required to be the path to a public key file PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(i.Config.SigningKeys.Primary.PublicKey, true, false); err != nil {
				validationError(&success, fmt.Sprintf("Error expanding `signingKeys.primary.publicKey`: %s", err))
			} else {
				i.Config.SigningKeys.Primary.PublicKey = expanded
			}
		}
		if i.Config.SigningKeys.Primary.PrivateKey == "" {
			validationError(&success, "The `signingKeys.primary.privateKey` field is required to be the path to a private key PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(i.Config.SigningKeys.Primary.PrivateKey, true, false); err != nil {
				validationError(&success, fmt.Sprintf("Error expanding `signingKeys.primary.privateKey`: %s", err))
			} else {
				i.Config.SigningKeys.Primary.PrivateKey = expanded
			}
		}
	}

	if i.Config.SigningKeys.Secondary != nil {
		if i.Config.SigningKeys.Secondary.PublicKey == "" {
			validationError(&success, "The `signingKeys.secondary.publicKey` field is required to be the path to a public key PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(i.Config.SigningKeys.Secondary.PublicKey, true, false); err != nil {
				validationError(&success, fmt.Sprintf("Error expanding `signingKeys.secondary.publicKey`: %s", err))
			} else {
				i.Config.SigningKeys.Secondary.PublicKey = expanded
			}
		}
		if i.Config.SigningKeys.Secondary.PrivateKey == "" {
			validationError(&success, "The `signingKeys.secondary.privateKey` field is required to be the path to a private key PEM file")
		} else {
			if expanded, err := envsubst.StringRestricted(i.Config.SigningKeys.Secondary.PrivateKey, true, false); err != nil {
				validationError(&success, fmt.Sprintf("Error expanding `signingKeys.secondary.privateKey`: %s", err))
			} else {
				i.Config.SigningKeys.Secondary.PrivateKey = expanded
			}
		}
	}

	if i.Config.PostgresImage == "" {
		i.Config.PostgresImage = DefaultPostgresImage
	}
	if i.Config.ControlPlaneImage == "" {
		i.Config.ControlPlaneImage = defaultImage("tyger-server")
	}
	if i.Config.DataPlaneImage == "" {
		i.Config.DataPlaneImage = defaultImage("tyger-data-plane-server")
	}
	if i.Config.BufferSidecarImage == "" {
		i.Config.BufferSidecarImage = defaultImage("buffer-sidecar")
	}
	if i.Config.GatewayImage == "" {
		i.Config.GatewayImage = defaultImage("tyger-cli")
	}

	if i.Config.UseGateway == nil {
		useGateway := defaultUseGateway()
		i.Config.UseGateway = &useGateway
	}

	return success
}

func defaultImage(repo string) string {
	return fmt.Sprintf("%s/%s:%s", install.ContainerRegistry, repo, install.ContainerImageTag)
}

func validationError(success *bool, format string, args ...any) {
	*success = false
	log.Error().Msgf(format, args...)
}
