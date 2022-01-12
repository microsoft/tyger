package uniqueid

import (
	"encoding/base32"
	"strings"

	"github.com/google/uuid"
	"github.com/rs/zerolog/log"
)

func NewId() string {
	uuidBytes, err := uuid.New().MarshalBinary()
	if err != nil {
		log.Panic().Err(err).Send()
	}

	return strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(uuidBytes), "="))
}
