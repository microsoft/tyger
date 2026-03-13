// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package client

import (
	"net/url"
	"runtime"
	"sort"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestParseSshUrl_Basic(t *testing.T) {
	u, err := url.Parse("ssh://myhost/path/to/socket")
	require.NoError(t, err)

	sp, err := ParseSshUrl(u)
	require.NoError(t, err)

	assert.Equal(t, "myhost", sp.Host)
	assert.Equal(t, "", sp.Port)
	assert.Equal(t, "", sp.User)
	assert.Equal(t, "/path/to/socket", sp.SocketPath)
	assert.Equal(t, "", sp.ConfigPath)
	assert.Equal(t, "", sp.CliPath)
	assert.Nil(t, sp.Options)
}

func TestParseSshUrl_WithPort(t *testing.T) {
	u, err := url.Parse("ssh://myhost:2222/path/to/socket")
	require.NoError(t, err)

	sp, err := ParseSshUrl(u)
	require.NoError(t, err)

	assert.Equal(t, "myhost", sp.Host)
	assert.Equal(t, "2222", sp.Port)
}

func TestParseSshUrl_WithUser(t *testing.T) {
	u, err := url.Parse("ssh://alice@myhost/path/to/socket")
	require.NoError(t, err)

	sp, err := ParseSshUrl(u)
	require.NoError(t, err)

	assert.Equal(t, "alice", sp.User)
	assert.Equal(t, "myhost", sp.Host)
}

func TestParseSshUrl_WithUserAndPort(t *testing.T) {
	u, err := url.Parse("ssh://alice@myhost:2222/path/to/socket")
	require.NoError(t, err)

	sp, err := ParseSshUrl(u)
	require.NoError(t, err)

	assert.Equal(t, "alice", sp.User)
	assert.Equal(t, "myhost", sp.Host)
	assert.Equal(t, "2222", sp.Port)
}

func TestParseSshUrl_PasswordRejected(t *testing.T) {
	u, err := url.Parse("ssh://alice:secret@myhost/path")
	require.NoError(t, err)

	_, err = ParseSshUrl(u)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "password")
}

func TestParseSshUrl_WrongScheme(t *testing.T) {
	u, err := url.Parse("http://myhost/path")
	require.NoError(t, err)

	_, err = ParseSshUrl(u)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "expected scheme ssh")
}

func TestParseSshUrl_NoHost(t *testing.T) {
	u := &url.URL{Scheme: "ssh", Path: "/path"}

	_, err := ParseSshUrl(u)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "no host")
}

func TestParseSshUrl_WithConfigPath(t *testing.T) {
	u, err := url.Parse("ssh://myhost/path?configPath=/home/user/.ssh/config")
	require.NoError(t, err)

	sp, err := ParseSshUrl(u)
	require.NoError(t, err)

	assert.Equal(t, "/home/user/.ssh/config", sp.ConfigPath)
}

func TestParseSshUrl_WithCliPath(t *testing.T) {
	u, err := url.Parse("ssh://myhost/path?cliPath=/usr/local/bin/tyger")
	require.NoError(t, err)

	sp, err := ParseSshUrl(u)
	require.NoError(t, err)

	assert.Equal(t, "/usr/local/bin/tyger", sp.CliPath)
}

func TestParseSshUrl_WithOptions(t *testing.T) {
	u, err := url.Parse("ssh://myhost/path?option[StrictHostKeyChecking]=no&option[UserKnownHostsFile]=/dev/null")
	require.NoError(t, err)

	sp, err := ParseSshUrl(u)
	require.NoError(t, err)

	require.NotNil(t, sp.Options)
	assert.Equal(t, "no", sp.Options["StrictHostKeyChecking"])
	assert.Equal(t, "/dev/null", sp.Options["UserKnownHostsFile"])
}

func TestParseSshUrl_WithAllQueryParams(t *testing.T) {
	u, err := url.Parse("ssh://myhost/path?configPath=/cfg&cliPath=/cli&option[Foo]=bar")
	require.NoError(t, err)

	sp, err := ParseSshUrl(u)
	require.NoError(t, err)

	assert.Equal(t, "/cfg", sp.ConfigPath)
	assert.Equal(t, "/cli", sp.CliPath)
	assert.Equal(t, "bar", sp.Options["Foo"])
}

func TestParseSshUrl_UnexpectedQueryParam(t *testing.T) {
	u, err := url.Parse("ssh://myhost/path?badparam=value")
	require.NoError(t, err)

	_, err = ParseSshUrl(u)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "unexpected query parameter")
}

func TestParseSshUrl_Fragment(t *testing.T) {
	u, err := url.Parse("ssh://myhost/path#frag")
	require.NoError(t, err)

	_, err = ParseSshUrl(u)
	assert.Error(t, err)
	assert.Contains(t, err.Error(), "extra fragment")
}

func TestParseSshUrl_NoPath(t *testing.T) {
	u, err := url.Parse("ssh://myhost")
	require.NoError(t, err)

	sp, err := ParseSshUrl(u)
	require.NoError(t, err)

	assert.Equal(t, "myhost", sp.Host)
	assert.Equal(t, "", sp.SocketPath)
}

func TestSshParams_URL_Basic(t *testing.T) {
	sp := SshParams{Host: "myhost", SocketPath: "/path/to/socket"}
	u := sp.URL()

	assert.Equal(t, "ssh", u.Scheme)
	assert.Equal(t, "myhost", u.Hostname())
	assert.Equal(t, "/path/to/socket", u.Path)
	assert.Equal(t, "", u.Port())
	assert.Nil(t, u.User)
}

func TestSshParams_URL_WithUser(t *testing.T) {
	sp := SshParams{Host: "myhost", User: "alice"}
	u := sp.URL()
	assert.Equal(t, "alice", u.User.Username())
}

func TestSshParams_URL_WithPort(t *testing.T) {
	sp := SshParams{Host: "myhost", Port: "2222"}
	u := sp.URL()
	assert.Equal(t, "2222", u.Port())
}

func TestSshParams_URL_WithCliPath(t *testing.T) {
	sp := SshParams{Host: "myhost", CliPath: "/usr/bin/tyger"}
	u := sp.URL()
	assert.Contains(t, u.RawQuery, "cliPath")
}

func TestSshParams_URL_WithOptions(t *testing.T) {
	sp := SshParams{
		Host:    "myhost",
		Options: map[string]string{"Foo": "bar"},
	}
	u := sp.URL()
	assert.Contains(t, u.RawQuery, "option%5BFoo%5D=bar")
}

func TestSshParams_String_RoundTrip(t *testing.T) {
	original := "ssh://alice@myhost:2222/path/to/socket?cliPath=%2Fusr%2Fbin%2Ftyger&option%5BFoo%5D=bar"
	u, err := url.Parse(original)
	require.NoError(t, err)

	sp, err := ParseSshUrl(u)
	require.NoError(t, err)

	result := sp.String()
	// Parse again to compare semantically
	u2, err := url.Parse(result)
	require.NoError(t, err)

	assert.Equal(t, u.Scheme, u2.Scheme)
	assert.Equal(t, u.Hostname(), u2.Hostname())
	assert.Equal(t, u.Port(), u2.Port())
	assert.Equal(t, u.Path, u2.Path)
	assert.Equal(t, u.User.Username(), u2.User.Username())
	assert.Equal(t, u.Query().Get("cliPath"), u2.Query().Get("cliPath"))
	assert.Equal(t, u.Query().Get("option[Foo]"), u2.Query().Get("option[Foo]"))
}

func TestFormatCmdLine_Basic(t *testing.T) {
	sp := SshParams{Host: "myhost"}
	args := sp.FormatCmdLine()

	assert.Equal(t, "myhost", args[0])
	assert.Contains(t, args, "--")
	assert.Contains(t, args, "tyger")
	assert.Contains(t, args, "stdio-proxy")

	// Check default StrictHostKeyChecking=yes
	assertContainsOption(t, args, "StrictHostKeyChecking=yes")
}

func TestFormatCmdLine_WithUser(t *testing.T) {
	sp := SshParams{Host: "myhost", User: "alice"}
	args := sp.FormatCmdLine()

	assertContainsFlag(t, args, "-l", "alice")
}

func TestFormatCmdLine_WithPort(t *testing.T) {
	sp := SshParams{Host: "myhost", Port: "2222"}
	args := sp.FormatCmdLine()

	assertContainsFlag(t, args, "-p", "2222")
}

func TestFormatCmdLine_WithConfigPath(t *testing.T) {
	sp := SshParams{Host: "myhost", ConfigPath: "/home/user/.ssh/config"}
	args := sp.FormatCmdLine()

	assertContainsFlag(t, args, "-F", "/home/user/.ssh/config")
}

func TestFormatCmdLine_WithCliPath(t *testing.T) {
	sp := SshParams{Host: "myhost", CliPath: "/usr/local/bin/tyger"}
	args := sp.FormatCmdLine()

	// After "--", the cli path should be used instead of "tyger"
	dashIdx := indexOf(args, "--")
	require.GreaterOrEqual(t, dashIdx, 0)
	assert.Equal(t, "/usr/local/bin/tyger", args[dashIdx+1])
}

func TestFormatCmdLine_WithAdditionalArgs(t *testing.T) {
	sp := SshParams{Host: "myhost"}
	args := sp.FormatCmdLine("--extra", "value")

	assert.Contains(t, args, "--extra")
	assert.Contains(t, args, "value")
}

func TestFormatCmdLine_WithOptionsOverrideDefaults(t *testing.T) {
	sp := SshParams{
		Host:    "myhost",
		Options: map[string]string{"StrictHostKeyChecking": "no"},
	}
	args := sp.FormatCmdLine()

	// The user-provided option should override the default
	assertContainsOption(t, args, "StrictHostKeyChecking=no")
	assertNotContainsOption(t, args, "StrictHostKeyChecking=yes")
}

func TestFormatCmdLine_WithAdditionalOptions(t *testing.T) {
	sp := SshParams{
		Host:    "myhost",
		Options: map[string]string{"UserKnownHostsFile": "/dev/null"},
	}
	args := sp.FormatCmdLine()

	assertContainsOption(t, args, "UserKnownHostsFile=/dev/null")
	// Default should still be present
	assertContainsOption(t, args, "StrictHostKeyChecking=yes")
}

func TestFormatLoginArgs_Basic(t *testing.T) {
	sp := SshParams{Host: "myhost"}
	args := sp.FormatLoginArgs()

	assert.Equal(t, "myhost", args[0])
	assert.Contains(t, args, "-nT")
	assert.Contains(t, args, "login")
	assert.Contains(t, args, "stdio-proxy")
}

func TestFormatLoginArgs_WithSocketPath(t *testing.T) {
	sp := SshParams{Host: "myhost", SocketPath: "/var/run/tyger.sock"}
	args := sp.FormatLoginArgs()

	loginIdx := indexOf(args, "login")
	require.GreaterOrEqual(t, loginIdx, 0)

	serverUrlIdx := indexOf(args, "--server-url")
	require.GreaterOrEqual(t, serverUrlIdx, 0)
	assert.Equal(t, "http+unix:///var/run/tyger.sock", args[serverUrlIdx+1])
}

func TestFormatLoginArgs_WithAdditionalArgs(t *testing.T) {
	sp := SshParams{Host: "myhost"}
	args := sp.FormatLoginArgs("--service-principal")

	assert.Contains(t, args, "--service-principal")
}

func TestFormatTunnelArgs_Basic(t *testing.T) {
	sp := SshParams{Host: "myhost", SocketPath: "/var/run/tyger.sock"}
	args := sp.FormatTunnelArgs("localhost:8080")

	assert.Equal(t, "myhost", args[0])
	assert.Contains(t, args, "-nNT")

	// Check the -L forwarding
	lIdx := indexOf(args, "-L")
	require.GreaterOrEqual(t, lIdx, 0)
	assert.Equal(t, "localhost:8080:/var/run/tyger.sock", args[lIdx+1])

	// Should NOT have -- or tyger (callTyger=false)
	assert.Equal(t, -1, indexOf(args, "--"))
	assert.Equal(t, -1, indexOf(args, "tyger"))
}

func TestFormatTunnelArgs_SshOptions(t *testing.T) {
	sp := SshParams{Host: "myhost", SocketPath: "/sock"}
	args := sp.FormatTunnelArgs("localhost:8080")

	// Check overriding options are present
	assertContainsOption(t, args, "ControlMaster=no")
	assertContainsOption(t, args, "ControlPath=none")
	assertContainsOption(t, args, "ExitOnForwardFailure=yes")
	assertContainsOption(t, args, "ServerAliveInterval=15")
	assertContainsOption(t, args, "ServerAliveCountMax=3")
	assertContainsOption(t, args, "StrictHostKeyChecking=yes")
}

func TestFormatTunnelArgs_StrictHostKeyCheckingOverriddenByOptions(t *testing.T) {
	sp := SshParams{
		Host:       "myhost",
		SocketPath: "/sock",
		Options:    map[string]string{"StrictHostKeyChecking": "no"},
	}
	args := sp.FormatTunnelArgs("localhost:8080")

	// StrictHostKeyChecking=yes is a default, but sp.Options sets it to "no".
	// Since it is NOT in overridingSshOptions, the user's explicit "no" should win.
	assertContainsOption(t, args, "StrictHostKeyChecking=no")
	assertNotContainsOption(t, args, "StrictHostKeyChecking=yes")
}

func TestFormatTunnelArgs_WithUserAndPort(t *testing.T) {
	sp := SshParams{Host: "myhost", User: "bob", Port: "3333", SocketPath: "/sock"}
	args := sp.FormatTunnelArgs("localhost:8080")

	assertContainsFlag(t, args, "-l", "bob")
	assertContainsFlag(t, args, "-p", "3333")
}

func TestFormatDataPlaneCmdLine_Basic(t *testing.T) {
	sp := SshParams{Host: "myhost"}
	args := sp.FormatDataPlaneCmdLine(false, "read", "mybuffer")

	assert.Equal(t, "myhost", args[0])
	assertContainsOption(t, args, "StrictHostKeyChecking=yes")

	assert.Contains(t, args, "--")
	assert.Contains(t, args, "tyger")
	assert.Contains(t, args, "stdio-proxy")
	assert.Contains(t, args, "read")
	assert.Contains(t, args, "mybuffer")
}

func TestFormatDataPlaneCmdLine_ControlSocketOptions(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("Control socket options are not set on Windows")
	}

	sp := SshParams{Host: "myhost"}

	// Long-lived
	args := sp.FormatDataPlaneCmdLine(true)
	assertContainsOption(t, args, "ControlMaster=auto")
	assertContainsOption(t, args, "ControlPath=/tmp/ssh-%C")
	assertContainsOption(t, args, "ControlPersist=30m")

	// Short-lived
	args = sp.FormatDataPlaneCmdLine(false)
	assertContainsOption(t, args, "ControlMaster=auto")
	assertContainsOption(t, args, "ControlPersist=2m")

	// ControlPath should start with /tmp/ssh- for short-lived
	found := false
	for i, a := range args {
		if a == "-o" && i+1 < len(args) && strings.HasPrefix(args[i+1], "ControlPath=/tmp/ssh-") {
			found = true
			break
		}
	}
	assert.True(t, found, "expected ControlPath starting with /tmp/ssh-")
}

func TestFormatDataPlaneCmdLine_WithCliPath(t *testing.T) {
	sp := SshParams{Host: "myhost", CliPath: "/custom/tyger"}
	args := sp.FormatDataPlaneCmdLine(false)

	dashIdx := indexOf(args, "--")
	require.GreaterOrEqual(t, dashIdx, 0)
	assert.Equal(t, "/custom/tyger", args[dashIdx+1])
}

func TestFormatCmdLine_NilOptions_UsesDefaults(t *testing.T) {
	sp := SshParams{Host: "myhost"}
	// Options is nil, so defaults should be used
	args := sp.FormatCmdLine()
	assertContainsOption(t, args, "StrictHostKeyChecking=yes")
}

func TestFormatCmdLine_StructureOrder(t *testing.T) {
	sp := SshParams{
		Host:       "myhost",
		Port:       "22",
		User:       "alice",
		CliPath:    "/bin/tyger",
		ConfigPath: "/cfg",
	}
	args := sp.FormatCmdLine("arg1", "arg2")

	// Host is always first
	assert.Equal(t, "myhost", args[0])

	// "--" separator exists
	dashIdx := indexOf(args, "--")
	require.GreaterOrEqual(t, dashIdx, 0)

	// Everything before "--" is SSH args
	// Everything after "--" is the remote command
	remoteCmd := args[dashIdx+1:]
	assert.Equal(t, "/bin/tyger", remoteCmd[0])
	assert.Equal(t, "stdio-proxy", remoteCmd[1])
	assert.Equal(t, "arg1", remoteCmd[2])
	assert.Equal(t, "arg2", remoteCmd[3])
}

func TestParseSshUrl_ConfigPathNotInURL(t *testing.T) {
	// configPath is consumed during parsing and should not appear in URL output
	sp := SshParams{
		Host:       "myhost",
		ConfigPath: "/home/user/.ssh/config",
	}
	u := sp.URL()
	assert.NotContains(t, u.RawQuery, "configPath")
}

func TestSshParams_URL_NoQueryWhenEmpty(t *testing.T) {
	sp := SshParams{Host: "myhost", SocketPath: "/path"}
	u := sp.URL()
	assert.Equal(t, "", u.RawQuery)
}

// Helpers

func assertContainsOption(t *testing.T, args []string, option string) {
	t.Helper()
	for i, a := range args {
		if a == "-o" && i+1 < len(args) && args[i+1] == option {
			return
		}
	}
	t.Errorf("expected args to contain -o %s, got %v", option, args)
}

func assertNotContainsOption(t *testing.T, args []string, option string) {
	t.Helper()
	for i, a := range args {
		if a == "-o" && i+1 < len(args) && args[i+1] == option {
			t.Errorf("expected args NOT to contain -o %s, got %v", option, args)
			return
		}
	}
}

func assertContainsFlag(t *testing.T, args []string, flag, value string) {
	t.Helper()
	for i, a := range args {
		if a == flag && i+1 < len(args) && args[i+1] == value {
			return
		}
	}
	t.Errorf("expected args to contain %s %s, got %v", flag, value, args)
}

func indexOf(args []string, target string) int {
	for i, a := range args {
		if a == target {
			return i
		}
	}
	return -1
}

// Verify that options from different sources merge correctly
func TestFormatCmdLine_OptionsMerge(t *testing.T) {
	sp := SshParams{
		Host: "myhost",
		Options: map[string]string{
			"UserKnownHostsFile":    "/dev/null",
			"StrictHostKeyChecking": "no", // override default
		},
	}
	args := sp.FormatCmdLine()

	assertContainsOption(t, args, "UserKnownHostsFile=/dev/null")
	assertContainsOption(t, args, "StrictHostKeyChecking=no")
	assertNotContainsOption(t, args, "StrictHostKeyChecking=yes")
}

// Verify all -o options appear as pairs
func TestFormatCmdLine_OptionPairsIntegrity(t *testing.T) {
	sp := SshParams{
		Host:    "myhost",
		Options: map[string]string{"A": "1", "B": "2"},
	}
	args := sp.FormatCmdLine()

	var optionValues []string
	for i, a := range args {
		if a == "-o" {
			require.Less(t, i+1, len(args), "-o flag must have a value")
			optionValues = append(optionValues, args[i+1])
		}
	}

	sort.Strings(optionValues)
	assert.Contains(t, optionValues, "A=1")
	assert.Contains(t, optionValues, "B=2")
	assert.Contains(t, optionValues, "StrictHostKeyChecking=yes")
}
