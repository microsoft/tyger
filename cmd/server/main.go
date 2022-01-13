package main

import (
	"encoding/base32"
	"fmt"
	"net/http"
	"os"
	"strings"

	"dev.azure.com/msresearch/compimag/_git/tyger/api"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/buffers"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/config"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/database"
	"dev.azure.com/msresearch/compimag/_git/tyger/internal/k8s"
	"github.com/alecthomas/kong"
	"github.com/google/uuid"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

type Args struct {
	PrettyPrint bool   `help:"Pretty-print logs." short:"p"`
	LogLevel    string `help:"Set the minimum log level to emit." short:"l" default:"Info" enum:"Debug,Info,Warn,Error,Fatal,Panic,Disabled"`
}

func main() {
	args := Args{}
	kong.Parse(&args, kong.UsageOnError())

	configureZerolog(args)

	config := config.GetConfig()

	repository, err := database.Connect(config)
	if err != nil {
		log.Fatal().Err(fmt.Errorf("failure during database initialization: %v", err)).Send()
	}

	bufferManager, err := buffers.NewBufferManager(config)
	if err != nil {
		log.Fatal().Err(fmt.Errorf("failure creating buffer manager: %v", err)).Send()
	}
	k8sManager, err := k8s.NewK8sManager(config, repository, bufferManager)
	if err != nil {
		log.Fatal().Err(fmt.Errorf("failure creating Kubernetes manager: %v", err)).Send()
	}

	router, err := api.BuildRouter(config, repository, bufferManager, k8sManager)
	if err != nil {
		log.Fatal().Err(fmt.Errorf("failed to build router: %v", err)).Send()
	}

	log.Info().Msgf("Listening on port %d...", config.Port)
	err = http.ListenAndServe(fmt.Sprintf(":%d", config.Port), router)
	log.Fatal().Err(err).Send()
}

func configureZerolog(args Args) {

	zerolog.TimeFieldFormat = "2006-01-02T15:04:05.999Z07:00"

	var level zerolog.Level
	switch args.LogLevel {
	case "Trace":
		level = zerolog.TraceLevel
	case "Debug":
		level = zerolog.DebugLevel
	case "Info":
		level = zerolog.InfoLevel
	case "Warn":
		level = zerolog.WarnLevel
	case "Error":
		level = zerolog.ErrorLevel
	case "Fatal":
		level = zerolog.FatalLevel
	case "Panic":
		level = zerolog.PanicLevel
	case "Disabled":
		level = zerolog.Disabled
	}
	zerolog.SetGlobalLevel(level)

	if args.PrettyPrint {
		log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr})
	}

	zerolog.DefaultContextLogger = &log.Logger
}

func NewId() string {
	uuidBytes, err := uuid.New().MarshalBinary()
	if err != nil {
		log.Panic().Err(err).Send()
	}

	return strings.ToLower(strings.TrimRight(base32.StdEncoding.EncodeToString(uuidBytes), "="))
}
