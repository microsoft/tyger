package config

import (
	"os"

	"github.com/johnstairs/pathenvconfig"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog/log"
)

type DatabaseConfigSpec struct {
	ConnectionString string `required:"true"`
	Password         string
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
}

func GetConfig() ConfigSpec {
	err := godotenv.Load()
	if err != nil && !os.IsNotExist(err) {
		log.Fatal().Err(err).Msg("Error loading .env file")
	}

	config := ConfigSpec{}
	err = pathenvconfig.Process("TYGER", &config)
	if err != nil {
		log.Fatal().Err(err).Send()
	}

	return config
}
