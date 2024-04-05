package dockerinstall

import (
	"fmt"
	"strconv"
)

const (
	EnvironmentKindDocker = "docker"
)

type DockerEnvironmentConfig struct {
	Kind string `json:"kind"`

	UserId         string `json:"userId"`
	AllowedGroupId string `json:"groupId"`

	PostgresImage      string `json:"postgresImage"`
	ControlPlaneImage  string `json:"controlPlaneImage"`
	DataPlaneImage     string `json:"dataPlaneImage"`
	BufferSidecarImage string `json:"bufferSidecarImage"`
	GatewayImage       string `json:"gatewayImage"`

	SigningKeys DataPlaneSigningKeys `json:"signingKeys"`
}

type DataPlaneSigningKeys struct {
	Primary   *KeyPair `json:"primary"`
	Secondary *KeyPair `json:"secondary"`
}
type KeyPair struct {
	PublicKey  string `json:"public"`
	PrivateKey string `json:"private"`
}

func (*DockerEnvironmentConfig) _environmentConfig() {
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
