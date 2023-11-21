package main

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/microsoft/tyger/cli/internal/cmd"
	"github.com/microsoft/tyger/cli/internal/proxy"
	"github.com/rs/zerolog/log"
	"github.com/spf13/cobra"
	"sigs.k8s.io/yaml"
)

var (
	// set during build
	version = ""
)

func main() {
	optionsFilePath := ""
	options := proxy.ProxyOptions{
		Port: 6888,
	}

	rootCommand := cmd.NewCommonRootCommand(version)
	rootCommand.Use = "tyger-proxy"
	rootCommand.Long = `tyger-proxy is an HTTP proxy for Tyger. It allows accessing a subset of the
control-plane API without authentication and to tunnel data-plane requests to Azure Storage.
It is indended to be run on on the Siemens Host machine and to be accessed from the MARS,
which does not have direct internet connectivity and where securely storing credentials is challenging.`

	rootCommand.AddCommand(newProxyRunCommand(&optionsFilePath, &options))
	rootCommand.AddCommand(newProxyStartCommand(&optionsFilePath, &options))

	err := rootCommand.Execute()
	if err != nil {
		os.Exit(1)
	}
}

func addFileFlag(cmd *cobra.Command, optionsFilePath *string) {
	cmd.Flags().StringVarP(optionsFilePath, "file", "f", "", `The path to a file containing proxy options. It should be a YAML file with the following structure:

# The Tyger server URI
serverUri: https://example.com

# The serive principal ID
servicePrincipal: api://my-client

# The path to a file with the service principal certificate
certificatePath: /a/path/to/a/file.pem

# The thumbprint of a certificate in a Windows certificate store to use for service principal authentication (Windows only)
certificateThumbprint: 92829BFAEB67C738DECE0B255C221CF9E1A46285

# A list of CIDR ranges that are allowed to access the proxy.
# If empty, there are no restrictions.
allowedClientCIDRs:
  - 172.18.0.2/32

# The port to listen on. If not specified, 6888 is used. If 0, a random port is used.
port: 6888

# The HTTP proxy to use. Can be 'auto[matic]', 'none', or a URI. The default is 'auto'.
proxy: auto

# A path either to a directory or to a file to write logs. If it is a directory, a log file will be created in it.
logPath: /tmp/tyger-proxy
	`)

	cmd.MarkFlagRequired("file")
	cmd.MarkFlagFilename("file", "yaml", "yml")
}

func createLogFileInDirectory(dir string) (*os.File, error) {
	var err error
	dir, err = filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, err
	}

	f, err := os.CreateTemp(dir, fmt.Sprintf("tyger-proxy-%s-*.log", time.Now().Format("2006-01-02T15-04-05Z07-00")))
	if err != nil {
		return nil, err
	}

	return f, os.Chmod(f.Name(), 0644)
}

func readProxyOptions(optionsFilePath string, options *proxy.ProxyOptions) error {
	var file *os.File
	if optionsFilePath == "-" {
		file = os.Stdin
	} else {
		optionsFilePath, err := filepath.Abs(optionsFilePath)
		if err != nil {
			return fmt.Errorf("failed to resolve options file path: %v", err)
		}
		f, err := os.Open(optionsFilePath)
		if err != nil {
			return fmt.Errorf("failed to read options file: %v", err)
		}
		defer f.Close()
		file = f
	}

	bytes, err := io.ReadAll(file)
	if err != nil {
		return fmt.Errorf("failed to read options file: %v", err)
	}

	if err := yaml.UnmarshalStrict(bytes, options); err != nil {
		return fmt.Errorf("failed to parse options file: %v", err)
	}

	if options.ServerUri == "" {
		return errors.New("serverUri must be specified")
	}

	if options.ServicePrincipal == "" {
		return errors.New("servicePrincipal must be specified in the options file")
	}

	if runtime.GOOS == "windows" {
		if options.CertificatePath == "" && options.CertificateThumbprint == "" {
			return errors.New("either certificatePath or certificateThumbprint must be specified in the options file")
		}

		if options.CertificatePath != "" && options.CertificateThumbprint != "" {
			return errors.New("certificatePath and certificateThumbprint cannot both be specified")
		}
	} else if options.CertificatePath == "" {
		return errors.New("certificatePath must be specified in the options file")
	}

	if optionsFilePath != "-" {
		// make paths relative to the options file
		optionsFileDirectory := filepath.Dir(optionsFilePath)
		makeRelativeToOptionsFile := func(path string) string {
			if path == "" || filepath.IsAbs(path) {
				return path
			}
			return filepath.Clean(filepath.Join(optionsFileDirectory, path))
		}

		options.CertificatePath = makeRelativeToOptionsFile(options.CertificatePath)
		options.LogPath = makeRelativeToOptionsFile(options.LogPath)
	}

	return nil
}

func exitIfRunning(options *proxy.ProxyOptions, alreadyRunning bool) {
	proxyMetadata, err := proxy.CheckProxyAlreadyRunning(options)
	switch err {
	case nil:
		var message string
		if alreadyRunning {
			message = "A proxy is already running on the specified port"
		} else {
			message = "The proxy is running"
		}

		log.Info().Int("port", options.Port).Str("logFile", proxyMetadata.LogPath).Msg(message)
		os.Exit(0)
	case proxy.ErrProxyAlreadyRunningWrongTarget:
		log.Fatal().Str("logFile", proxyMetadata.LogPath).Msg("A proxy is already running on the specified port, but it is not targeting the same server")
	}
}

func isPathDirectoryIntent(p string) bool {
	if p == "" {
		return false
	}

	if strings.HasSuffix(p, string(os.PathSeparator)) {
		return true
	}

	if info, err := os.Stat(p); err == nil && info.IsDir() {
		return true
	}

	return path.Ext(p) == ""
}
