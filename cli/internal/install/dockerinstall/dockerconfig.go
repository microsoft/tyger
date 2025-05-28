// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dockerinstall

import (
	_ "embed"
	"fmt"
	"io"
	"strconv"
	"text/template"

	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/microsoft/tyger/cli/internal/templatefunctions"
)

//go:embed docker-config-pretty.tpl
var prettyPrintConfigTemplate string

const (
	ConfigKindDocker = "docker"
)

type DockerEnvironmentConfig struct {
	install.ConfigFileCommon `yaml:",inline"`

	EnvironmentName string `yaml:"environmentName"`

	DataPlanePort int `yaml:"dataPlanePort"`

	UserId         string `yaml:"userId"`
	AllowedGroupId string `yaml:"allowedGroupId"`

	InstallationPath string `yaml:"installationPath"`

	PostgresImage      string `yaml:"postgresImage"`
	MarinerImage       string `yaml:"marinerImage"`
	ControlPlaneImage  string `yaml:"controlPlaneImage"`
	DataPlaneImage     string `yaml:"dataPlaneImage"`
	BufferSidecarImage string `yaml:"bufferSidecarImage"`
	GatewayImage       string `yaml:"gatewayImage"`

	UseGateway *bool `yaml:"useGateway"`

	SigningKeys DataPlaneSigningKeys `yaml:"signingKeys"`

	InitialDatabaseVersion *int `yaml:"initialDatabaseVersion"`

	Network *NetworkConfig `yaml:"network"`

	Buffers *BuffersConfig `yaml:"buffers"`
}

type DataPlaneSigningKeys struct {
	Primary   *KeyPair `yaml:"primary"`
	Secondary *KeyPair `yaml:"secondary"`
}

type KeyPair struct {
	PublicKey  string `yaml:"public"`
	PrivateKey string `yaml:"private"`
}

type NetworkConfig struct {
	Subnet string `yaml:"subnet"`
}

type BuffersConfig struct {
	ActiveLifetime      string `yaml:"activeLifetime"`
	SoftDeletedLifetime string `yaml:"softDeletedLifetime"`
}

func (c *DockerEnvironmentConfig) GetGroupIdInt() int {
	id, err := strconv.Atoi(c.AllowedGroupId)
	if err != nil {
		// this should have been caught by validation
		panic(fmt.Sprintf("Invalid group ID: %s", c.AllowedGroupId))
	}

	return id
}

func (c *DockerEnvironmentConfig) GetUserIdInt() int {
	id, err := strconv.Atoi(c.UserId)
	if err != nil {
		// this should have been caught by validation
		panic(fmt.Sprintf("Invalid user ID: %s", c.UserId))
	}

	return id
}

type ConfigTemplateValues struct {
	PublicSigningKeyPath  string
	PrivateSigningKeyPath string
	DataPlanePort         int
}

func RenderConfig(templateValues ConfigTemplateValues, writer io.Writer) error {
	config := DockerEnvironmentConfig{
		ConfigFileCommon: install.ConfigFileCommon{
			Kind: ConfigKindDocker,
		},
		DataPlanePort: templateValues.DataPlanePort,
		SigningKeys: DataPlaneSigningKeys{
			Primary: &KeyPair{
				PublicKey:  templateValues.PublicSigningKeyPath,
				PrivateKey: templateValues.PrivateSigningKeyPath,
			},
		},
	}

	return PrettyPrintConfig(&config, writer)
}

func PrettyPrintConfig(config *DockerEnvironmentConfig, writer io.Writer) error {
	t := template.Must(template.New("config").Funcs(templatefunctions.GetFuncMap()).Parse(prettyPrintConfigTemplate))
	return t.Execute(writer, config)
}
