// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package dockerinstall

import (
	_ "embed"
	"fmt"
	"io"
	"strconv"
	"strings"
	"text/template"
)

//go:embed config.tpl
var configTemplate string

const (
	EnvironmentKindDocker = "docker"
)

type DockerEnvironmentConfig struct {
	Kind string `json:"kind"`

	EnvironmentName string `json:"environmentName"`

	DataPlanePort int `json:"dataPlanePort"`

	UserId         string `json:"userId"`
	AllowedGroupId string `json:"allowedGroupId"`

	InstallationPath string `json:"installationPath"`

	PostgresImage      string `json:"postgresImage"`
	ControlPlaneImage  string `json:"controlPlaneImage"`
	DataPlaneImage     string `json:"dataPlaneImage"`
	BufferSidecarImage string `json:"bufferSidecarImage"`
	GatewayImage       string `json:"gatewayImage"`

	UseGateway *bool `json:"useGateway"`

	SigningKeys DataPlaneSigningKeys `json:"signingKeys"`

	InitialDatabaseVersion *int `json:"initialDatabaseVersion"`
}

type DataPlaneSigningKeys struct {
	Primary   *KeyPair `json:"primary"`
	Secondary *KeyPair `json:"secondary"`
}
type KeyPair struct {
	PublicKey  string `json:"public"`
	PrivateKey string `json:"private"`
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
	funcs := map[string]any{
		"contains": strings.Contains,
	}

	t := template.Must(template.New("config").Funcs(funcs).Parse(configTemplate))

	return t.Execute(writer, templateValues)
}
