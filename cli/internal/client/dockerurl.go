// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package client

import (
	"fmt"
	"net/url"

	"github.com/pkg/errors"
)

type DockerParams struct {
	ContainerName string
	SocketPath    string
}

const (
	defaultGatewayContainerName = "tyger-local-gateway"
)

func ParseDockerUrl(u *url.URL) (*DockerParams, error) {
	if u.Scheme != "docker" {
		return nil, fmt.Errorf("expected scheme docker, got %q", u.Scheme)
	}

	params := DockerParams{}

	params.ContainerName = u.Hostname()
	if params.ContainerName == "" {
		params.ContainerName = defaultGatewayContainerName
	}

	params.SocketPath = u.Path

	if queryParams := u.Query(); len(queryParams) > 0 {
		return nil, errors.Errorf("unexpected query parameters: %v.", queryParams)
	}

	if u.Fragment != "" {
		return nil, errors.Errorf("extra fragment after the host: %q", u.Fragment)
	}

	return &params, nil
}

func (sp *DockerParams) String() string {
	return sp.URL().String()
}

func (p *DockerParams) URL() *url.URL {
	u := url.URL{
		Scheme: "ssh",
		Host:   p.ContainerName,
		Path:   p.SocketPath,
	}

	return &u
}

func (p *DockerParams) FormatCmdLine(add ...string) []string {
	args := []string{"exec", "-i", p.ContainerName, "/app/tyger", "stdio-proxy"}
	args = append(args, add...)
	return args
}

func (sp *DockerParams) FormatLoginArgs(add ...string) []string {
	args := []string{"login"}

	if sp.SocketPath != "" {
		args = append(args, "--socket-path", sp.SocketPath)
	}

	args = append(args, add...)
	return sp.FormatCmdLine(args...)
}
