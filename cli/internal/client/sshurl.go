// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package client

import (
	"fmt"
	"net/url"
	"os"
	"runtime"
	"strings"

	"github.com/google/uuid"
	"github.com/pkg/errors"
	"golang.org/x/term"
)

// Parses and formats ssh:// URLs

type SshParams struct {
	Host       string
	Port       string
	User       string
	SocketPath string
	CliPath    string
	Options    map[string]string
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

		for k, v := range queryParams {
			if strings.HasPrefix(k, "option[") && strings.HasSuffix(k, "]") {
				name := k[7 : len(k)-1]
				if sp.Options == nil {
					sp.Options = make(map[string]string)
				}
				sp.Options[name] = v[0]
			} else {
				return nil, errors.Errorf("unexpected query parameter: %q. Only 'cliPath' and 'option[<SSH_OPTION>]' are suported", k)
			}
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
	q := u.Query()
	if sp.CliPath != "" {
		q.Set("cliPath", sp.CliPath)
	}
	for k, v := range sp.Options {
		q.Set(fmt.Sprintf("option[%s]", k), v)
	}

	if len(q) > 0 {
		u.RawQuery = q.Encode()
	}

	return &u
}

func (sp *SshParams) FormatCmdLine(add ...string) []string {
	sshOptions := map[string]string{
		"StrictHostKeyChecking": "yes",
	}
	return sp.formatCmdLine(sshOptions, nil, add...)
}

func (sp *SshParams) formatCmdLine(sshOptions map[string]string, otherSshArgs []string, cmdArgs ...string) []string {
	args := []string{sp.Host}

	var combinedSshOptions map[string]string
	if sp.Options == nil {
		combinedSshOptions = sshOptions
	} else if sshOptions == nil {
		combinedSshOptions = sp.Options
	} else {
		combinedSshOptions = make(map[string]string)
		for k, v := range sshOptions {
			combinedSshOptions[k] = v
		}
		for k, v := range sp.Options {
			combinedSshOptions[k] = v
		}
	}

	for k, v := range combinedSshOptions {
		args = append(args, "-o", fmt.Sprintf("%s=%s", k, v))
	}

	if sp.User != "" {
		args = append(args, "-l", sp.User)
	}
	if sp.Port != "" {
		args = append(args, "-p", sp.Port)
	}

	args = append(args, otherSshArgs...)

	args = append(args, "--")

	if sp.CliPath != "" {
		args = append(args, sp.CliPath)
	} else {
		args = append(args, "tyger")
	}

	args = append(args, "stdio-proxy")

	args = append(args, cmdArgs...)
	return args
}

func (sp *SshParams) FormatLoginArgs(add ...string) []string {
	var sshOptions map[string]string
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		// avoid interactive prompt
		sshOptions = map[string]string{
			"StrictHostKeyChecking": "yes",
		}
	}

	// Disable stdin and disable pseudo-terminal allocation.
	// On Windows, we can get a hang when the remote process exits quickly because
	// the SSH process is waiting for Enter to be pressed.

	otherSshArgs := []string{"-nT"}
	args := []string{"login"}

	if sp.SocketPath != "" {
		args = append(args, "--server-url", fmt.Sprintf("http+unix://%s", sp.SocketPath))
	}

	args = append(args, add...)
	return sp.formatCmdLine(sshOptions, otherSshArgs, args...)
}

func (sp *SshParams) FormatDataPlaneCmdLine(add ...string) []string {
	sshOptions := map[string]string{
		"StrictHostKeyChecking": "yes",
	}

	if runtime.GOOS != "windows" {
		// create a dedicated control socket for this process
		sshOptions["ControlMaster"] = "auto"
		sshOptions["ControlPath"] = fmt.Sprintf("/tmp/%s", uuid.New().String())
		sshOptions["ControlPersist"] = "2m"
	}

	return sp.formatCmdLine(sshOptions, nil, add...)
}
