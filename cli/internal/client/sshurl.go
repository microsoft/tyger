// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package client

import (
	"fmt"
	"net/url"

	"github.com/google/uuid"
	"github.com/pkg/errors"
)

// Parses and formats ssh:// URLs

type SshParams struct {
	Host       string
	Port       string
	User       string
	SocketPath string
	CliPath    string
}

func ParseSshUrl(u *url.URL) (*SshParams, error) {
	if u.Scheme != "ssh" {
		return nil, fmt.Errorf("expected scheme ssh, got %q", u.Scheme)
	}

	sp := SshParams{}

	if u.User != nil {
		sp.User = u.User.Username()
		if _, ok := u.User.Password(); ok {
			return nil, errors.New("plain-text password is not supported")
		}
	}
	sp.Host = u.Hostname()
	if sp.Host == "" {
		return nil, errors.Errorf("no host specified")
	}
	sp.Port = u.Port()
	sp.SocketPath = u.Path

	if queryParams := u.Query(); len(queryParams) > 0 {
		if cliPath, ok := queryParams["cliPath"]; ok {
			sp.CliPath = cliPath[0]
			queryParams.Del("cliPath")
		}

		if len(queryParams) > 0 {
			return nil, errors.Errorf("unexpected query parameters: %v. Only 'cliPath' is supported", queryParams)
		}
	}

	if u.Fragment != "" {
		return nil, errors.Errorf("extra fragment after the host: %q", u.Fragment)
	}

	return &sp, nil
}

func (sp *SshParams) String() string {
	return sp.URL().String()
}

func (sp *SshParams) URL() *url.URL {
	u := url.URL{
		Scheme: "ssh",
		Host:   sp.Host,
		Path:   sp.SocketPath,
	}
	if sp.User != "" {
		u.User = url.User(sp.User)
	}

	if sp.Port != "" {
		u.Host += ":" + sp.Port
	}
	if sp.CliPath != "" {
		q := u.Query()
		q.Set("cliPath", sp.CliPath)
		u.RawQuery = q.Encode()
	}

	return &u
}

func (sp *SshParams) FormatCmdLine(add ...string) []string {
	return sp.formatCmdLine(false, add...)
}

func (sp *SshParams) formatCmdLine(dataPlane bool, add ...string) []string {
	args := []string{sp.Host}

	if sp.User != "" {
		args = append(args, "-l", sp.User)
	}
	if sp.Port != "" {
		args = append(args, "-p", sp.Port)
	}

	if dataPlane {
		// create a dedicated control socket for this process
		args = append(args, "-o", "ControlMaster=auto")
		args = append(args, "-o", fmt.Sprintf("ControlPath=/tmp/%s", uuid.New().String()))
		args = append(args, "-o", "ControlPersist=2m")
	}

	args = append(args, "--")

	if sp.CliPath != "" {
		args = append(args, sp.CliPath)
	} else {
		args = append(args, "tyger")
	}

	args = append(args, "stdio-proxy")

	args = append(args, add...)
	return args
}

func (sp *SshParams) FormatLoginArgs(add ...string) []string {
	args := []string{"login"}

	if sp.SocketPath != "" {
		args = append(args, "--socket-path", sp.SocketPath)
	}

	args = append(args, add...)
	return sp.FormatCmdLine(args...)
}

func (sp *SshParams) FormatDataPlaneCmdLine(add ...string) []string {
	return sp.formatCmdLine(true, add...)
}
