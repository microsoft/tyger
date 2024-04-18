// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build integrationtest

package integrationtest

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	"github.com/go-viper/mapstructure/v2"
	"github.com/golang-jwt/jwt/v5"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"

	koanfyaml "github.com/knadh/koanf/parsers/yaml"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
)

func TestCloudMigrations(t *testing.T) {
	t.Parallel()
	skipIfUsingUnixSocket(t)

	environmentConfig := runCommandSucceeds(t, "../../scripts/get-config.sh")
	tempDir := t.TempDir()
	configPath := fmt.Sprintf("%s/environment-config.yaml", tempDir)
	require.NoError(t, os.WriteFile(configPath, []byte(environmentConfig), 0644))

	config := cloudinstall.CloudEnvironmentConfig{}

	koanfConfig := koanf.New(".")
	require.NoError(t, koanfConfig.Load(file.Provider(configPath), koanfyaml.Parser()))
	require.NoError(t, koanfConfig.UnmarshalWithConf("", &config, koanf.UnmarshalConf{
		Tag: "json",
		DecoderConfig: &mapstructure.DecoderConfig{
			WeaklyTypedInput: true,
			ErrorUnused:      true,
			Squash:           true,
			Result:           &config,
		},
	}))

	ctx := context.Background()

	cred, err := cloudinstall.NewMiAwareAzureCLICredential(
		&azidentity.AzureCLICredentialOptions{
			TenantID: config.Cloud.TenantID,
		})

	installer := cloudinstall.Installer{
		Config:     &config,
		Credential: cred,
	}

	restConfig, err := installer.GetUserRESTConfig(ctx)
	require.NoError(t, err)

	// this is a try run to get the Helm values
	_, helmValuesYaml, err := installer.InstallTygerHelmChart(ctx, restConfig, true)
	require.NoError(t, err)

	helmValues := make(map[string]any)
	require.NoError(t, yaml.Unmarshal([]byte(helmValuesYaml), &helmValues))

	username := runCommandSucceeds(t, "az", "account", "show", "--query", "user.name", "-o", "tsv")
	password := runCommandSucceeds(t, "az", "account", "get-access-token", "--resource-type", "oss-rdbms", "--query", "accessToken", "-o", "tsv")
	if username == "systemAssignedIdentity" || username == "userAssignedIdentity" {
		// Need to get the managed identity app id from the token
		claims := jwt.MapClaims{}
		_, _, err = jwt.NewParser().ParseUnverified(password, claims)
		require.NoError(t, err)
		username = claims["appid"].(string)
	}

	host := helmValues["database"].(map[string]any)["host"].(string)
	port := helmValues["database"].(map[string]any)["port"].(float64)
	databaseName := helmValues["database"].(map[string]any)["databaseName"].(string)

	temporaryDatabaseName := fmt.Sprintf("tygertest%s", cloudinstall.RandomAlphanumString(8))

	createPsqlCommandBuilder := func() *CmdBuilder {
		return NewCmdBuilder("psql",
			"--host", host,
			"--port", fmt.Sprintf("%d", int(port)),
			"--username", username,
			"--dbname", databaseName).
			Env("PGPASSWORD", password)
	}

	createPsqlCommandBuilder().
		Arg("--command").Arg(fmt.Sprintf("CREATE DATABASE %s", temporaryDatabaseName)).
		RunSucceeds(t)

	defer func() {
		createPsqlCommandBuilder().
			Arg("--command").Arg(fmt.Sprintf("DROP DATABASE %s", temporaryDatabaseName)).
			RunSucceeds(t)
	}()

	tygerMigrationApplyArgs := []string{
		"api", "migrations", "apply", "--latest", "--offline", "--wait",
		"-f", configPath,
		"--set", fmt.Sprintf("api.helm.tyger.values.database.databaseName=%s", temporaryDatabaseName),
	}

	runTygerSucceeds(t, tygerMigrationApplyArgs...)

	logs := runTygerSucceeds(t, "api", "migrations", "logs", "1",
		"-f", configPath,
		"--set", fmt.Sprintf("api.helm.tyger.values.database.databaseName=%s", temporaryDatabaseName))

	assert.Contains(t, logs, "Migration 1 complete")

	logs = runTygerSucceeds(t, "api", "migrations", "logs", "2",
		"-f", configPath,
		"--set", fmt.Sprintf("api.helm.tyger.values.database.databaseName=%s", temporaryDatabaseName))

	assert.Contains(t, logs, "Migration 2 complete")
}

func TestDockerMigrations(t *testing.T) {
	t.Parallel()
	skipUnlessUsingUnixSocket(t)
	skipIfNotUsingUnixSocketDirectly(t)

	lowercaseTestName := strings.ToLower(t.Name())

	environmentConfig := runCommandSucceeds(t, "../../scripts/get-config.sh", "--docker")

	intallationPath := fmt.Sprintf("/tmp/tyger/%s", lowercaseTestName)
	defer func() {
		os.RemoveAll(intallationPath)
	}()

	configMap := make(map[string]any)
	require.NoError(t, yaml.Unmarshal([]byte(environmentConfig), &configMap))
	configMap["environmentName"] = lowercaseTestName
	configMap["installationPath"] = intallationPath
	configMap["initialDatabaseVersion"] = 1

	updatedEnvironmentConfigBytes, err := yaml.Marshal(configMap)
	require.NoError(t, err)

	tempDir := t.TempDir()
	configPath := fmt.Sprintf("%s/environment-config.yaml", tempDir)
	require.NoError(t, os.WriteFile(configPath, updatedEnvironmentConfigBytes, 0644))

	tygerPath, err := exec.LookPath("tyger")
	require.NoError(t, err)
	tygerPath, err = filepath.Abs(tygerPath)
	require.NoError(t, err)

	defer func() {
		runCommandSucceeds(t, "sudo", tygerPath, "api", "uninstall", "-f", configPath, "--delete-data")
	}()

	runCommandSucceeds(t, "sudo", tygerPath, "api", "install", "-f", configPath)

	runTygerSucceeds(t, "api", "migrations", "apply", "--latest", "--offline", "--wait", "-f", configPath)

	logs := runTygerSucceeds(t, "api", "migrations", "logs", "2", "-f", configPath)
	assert.Contains(t, logs, "Migration 2 complete")
}
