package dockerinstall

import (
	"os/user"
	"strconv"

	"github.com/rs/zerolog/log"
)

func (i *Installer) QuickValidateConfig() bool {
	success := true

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
		}
		if i.Config.SigningKeys.Primary.PrivateKey == "" {
			validationError(&success, "The `signingKeys.primary.privateKey` field is required to be the path to a private key PEM file")
		}
	}

	if i.Config.SigningKeys.Secondary != nil {
		if i.Config.SigningKeys.Secondary.PublicKey == "" {
			validationError(&success, "The `signingKeys.secondary.publicKey` field is required to be the path to a public key PEM file")
		}
		if i.Config.SigningKeys.Secondary.PrivateKey == "" {
			validationError(&success, "The `signingKeys.secondary.privateKey` field is required to be the path to a private key PEM file")
		}
	}

	return success
}

func validationError(success *bool, format string, args ...any) {
	*success = false
	log.Error().Msgf(format, args...)
}
