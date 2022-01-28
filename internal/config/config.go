package config

import (
	"errors"
	"os"

	"github.com/johnstairs/pathenvconfig"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
)

type DatabaseConfigSpec struct {
	ConnectionString string `required:"true"`
	Password         string
}

type SecurityConfigSpec struct {
	Enabled   bool `default:"true"`
	Authority string
	Audience  string
}

type ConfigSpec struct {
	StorageAccountConnectionString string `required:"true"`
	StorageEmulatorExternalHost    string `required:"true"`
	KubernetesNamespace            string `required:"true"`
	BaseUri                        string
	Port                           int `default:"3000"`
	KubeconfigPath                 string
	Database                       DatabaseConfigSpec
	MrdStorageUri                  string `required:"true"`
	Security                       SecurityConfigSpec
}

func GetConfig() ConfigSpec {
	if err := godotenv.Load(); err != nil && !os.IsNotExist(err) {
		log.Fatal().Err(err).Msg("Error loading .env file")
	}

	config := ConfigSpec{}

	if err := pathenvconfig.Process("TYGER", &config); err != nil {
		log.Fatal().Err(err).Send()
	}

	if err := config.Validate(); err != nil {
		log.Fatal().Err(err).Msg("Configuration validation errors")
	}

	return config
}

func (config ConfigSpec) Validate() error {
	if config.Security.Enabled {
		if config.Security.Authority == "" || config.Security.Audience == "" {
			return errors.New("when TYGER_SECURITY_ENABLED is true, then both TYGER_SECURITY_AUTHORITY and TYGER_SECURITY_AUDIENCE must be set")
		}
	}

	return nil
}
