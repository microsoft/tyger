package dockerinstall

import (
	"os/user"
	"strconv"

	"github.com/rs/zerolog/log"
)

func QuickValidateDockerEnvironmentConfig(config *DockerEnvironmentConfig) bool {
	success := true

	if _, err := strconv.Atoi(config.UserId); err != nil {
		if config.UserId == "" {
			currentUser, err := user.Current()
			if err != nil {
				validationError(&success, "Unable to determine the current user for the `userId` field")
			} else {
				config.UserId = currentUser.Uid
			}
		} else {
			u, err := user.Lookup(config.UserId)
			if err != nil {
				validationError(&success, "The `userId` field must be a valid user ID or name")
			} else {
				config.UserId = u.Uid
			}
		}
	}

	if _, err := strconv.Atoi(config.AllowedGroupId); err != nil {
		if config.AllowedGroupId == "" {
			currentUser, err := user.Current()
			if err != nil {
				validationError(&success, "Unable to determine the current user for the `userId` field")
			} else {
				config.AllowedGroupId = currentUser.Gid
			}
		} else {
			g, err := user.LookupGroup(config.AllowedGroupId)
			if err != nil {
				validationError(&success, "The `groupId` field must be a valid group ID or name")
			} else {
				config.AllowedGroupId = g.Gid
			}
		}
	}

	if config.SigningKeys.Primary == nil {
		validationError(&success, "The `signingKeys.primary` field is required")
	} else {
		if config.SigningKeys.Primary.PublicKey == "" {
			validationError(&success, "The `signingKeys.primary.publicKey` field is required to be the path to a public key file PEM file")
		}
		if config.SigningKeys.Primary.PrivateKey == "" {
			validationError(&success, "The `signingKeys.primary.privateKey` field is required to be the path to a private key PEM file")
		}
	}

	if config.SigningKeys.Secondary != nil {
		if config.SigningKeys.Secondary.PublicKey == "" {
			validationError(&success, "The `signingKeys.secondary.publicKey` field is required to be the path to a public key PEM file")
		}
		if config.SigningKeys.Secondary.PrivateKey == "" {
			validationError(&success, "The `signingKeys.secondary.privateKey` field is required to be the path to a private key PEM file")
		}
	}

	return success
}

func validationError(success *bool, format string, args ...any) {
	*success = false
	log.Error().Msgf(format, args...)
}
