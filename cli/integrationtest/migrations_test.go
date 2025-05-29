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
	"github.com/goccy/go-yaml"
	"github.com/golang-jwt/jwt/v5"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"

	"github.com/microsoft/tyger/cli/internal/cmd/install"
	"github.com/microsoft/tyger/cli/internal/dataplane"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
)

func TestCloudMigrations(t *testing.T) {
	t.Parallel()
	skipIfOnlyFastTests(t)
	skipIfUsingUnixSocket(t)

	environmentConfig := runCommandSucceeds(t, "../../scripts/get-config.sh")
	tempDir := t.TempDir()
	configPath := fmt.Sprintf("%s/environment-config.yaml", tempDir)
	require.NoError(t, os.WriteFile(configPath, []byte(environmentConfig), 0644))

	configBytes, err := os.ReadFile(configPath)
	require.NoError(t, err)

	vc, err := install.ParseConfig(configBytes)
	require.NoError(t, err)

	config := vc.(*cloudinstall.CloudEnvironmentConfig)
	config.Organizations = []*cloudinstall.OrganizationConfig{config.Organizations[0]}

	ctx := context.Background()

	cred, err := cloudinstall.NewMiAwareAzureCLICredential(
		&azidentity.AzureCLICredentialOptions{
			TenantID: config.Cloud.TenantID,
		})

	installer := cloudinstall.Installer{
		Config:     config,
		Credential: cred,
	}

	assert.NoError(t, installer.Config.QuickValidateConfig(ctx))

	// this is a dry run to get the Helm values
	_, helmValuesYaml, err := installer.InstallTygerHelmChart(ctx, installer.Config.GetSingleOrg(), true)
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
	port := helmValues["database"].(map[string]any)["port"].(uint64)
	databaseName := helmValues["database"].(map[string]any)["databaseName"].(string)

	temporaryDatabaseName := fmt.Sprintf("tygertest%s", cloudinstall.RandomAlphanumString(8))

	createPsqlCommandBuilder := func(databaseName string) *CmdBuilder {
		return NewCmdBuilder("psql",
			"--host", host,
			"--port", fmt.Sprintf("%d", port),
			"--username", username,
			"--dbname", databaseName).
			Env("PGPASSWORD", password)
	}

	createPsqlCommandBuilder(databaseName).
		Arg("--command").Arg(fmt.Sprintf("CREATE DATABASE %s", temporaryDatabaseName)).
		RunSucceeds(t)

	createPsqlCommandBuilder(temporaryDatabaseName).
		Arg("--command").Arg(fmt.Sprintf("GRANT CREATE, USAGE ON SCHEMA public to \"%s\"; GRANT CREATE, USAGE ON SCHEMA public to \"%s\"", "lamna-tyger-migration-runner", "lamna-tyger-owners")).
		RunSucceeds(t)

	defer func() {
		createPsqlCommandBuilder(databaseName).
			Arg("--command").Arg(fmt.Sprintf("DROP DATABASE %s", temporaryDatabaseName)).
			RunSucceeds(t)
	}()

	config.Organizations[0].Cloud.DatabaseName = temporaryDatabaseName

	b, err := yaml.Marshal(config)
	require.NoError(t, err)
	os.WriteFile(configPath, b, 0644)

	tygerMigrationApplyArgs := []string{
		"api", "migrations", "apply", "--latest", "--offline", "--wait",
		"-f", configPath,
	}

	runTygerSucceeds(t, tygerMigrationApplyArgs...)

	logs := runTygerSucceeds(t, "api", "migrations", "logs", "1",
		"-f", configPath)

	assert.Contains(t, logs, "Migration 1 complete")

	logs = runTygerSucceeds(t, "api", "migrations", "logs", "2",
		"-f", configPath)

	assert.Contains(t, logs, "Migration 2 complete")
}

func getTempDockerInstallationPath(t *testing.T) string {
	lowercaseTestName := strings.ToLower(t.Name())

	installationPath := fmt.Sprintf("../../install/%s", lowercaseTestName)
	require.NoError(t, os.MkdirAll(installationPath, 0755))
	installationPath, err := filepath.Abs(installationPath)
	require.NoError(t, err)
	installationPath, err = filepath.EvalSymlinks(installationPath)
	require.NoError(t, err)

	return installationPath
}

// TODO: Re-enable this test when we have a current database version that supports online migrations

// func TestDockerOnlineMigrations(t *testing.T) {
// 	t.Parallel()
//  skipIfOnlyFastTests(t)
// 	skipUnlessUsingUnixSocket(t)
// 	skipIfNotUsingUnixSocketDirectly(t)

// 	installationPath := getTempDockerInstallationPath(t)
// 	defer os.RemoveAll(installationPath)

// 	environmentConfig := runCommandSucceeds(t, "../../scripts/get-config.sh", "--docker")

// 	lowercaseTestName := strings.ToLower(t.Name())
// 	if len(lowercaseTestName) > 23 {
// 		lowercaseTestName = lowercaseTestName[:23]
// 	}
// 	configMap := make(map[string]any)
// 	require.NoError(t, yaml.Unmarshal([]byte(environmentConfig), &configMap))
// 	configMap["environmentName"] = lowercaseTestName
// 	configMap["installationPath"] = installationPath
// 	configMap["initialDatabaseVersion"] = 3
// 	p, err := dataplane.GetFreePort()
// 	require.NoError(t, err)
// 	configMap["dataPlanePort"] = p
// 	configMap["network"] = map[string]any{"subnet": "172.255.0.0/24"}

// 	updatedEnvironmentConfigBytes, err := yaml.Marshal(configMap)
// 	require.NoError(t, err)

// 	tempDir := t.TempDir()
// 	configPath := fmt.Sprintf("%s/environment-config.yaml", tempDir)
// 	require.NoError(t, os.WriteFile(configPath, updatedEnvironmentConfigBytes, 0644))

// 	tygerPath, err := exec.LookPath("tyger")
// 	require.NoError(t, err)
// 	tygerPath, err = filepath.Abs(tygerPath)
// 	require.NoError(t, err)

// 	defer func() {
// 		runCommandSucceeds(t, "sudo", tygerPath, "api", "uninstall", "-f", configPath, "--delete-data", "--preserve-run-containers")
// 	}()

// 	runCommandSucceeds(t, "sudo", tygerPath, "api", "install", "-f", configPath)

// 	runTygerSucceeds(t, "api", "migrations", "apply", "--latest", "--offline", "--wait", "-f", configPath)

// 	logs := runTygerSucceeds(t, "api", "migrations", "logs", "4", "-f", configPath)
// 	assert.Contains(t, logs, "Migration 4 complete")
// }

func TestDockerOfflineMigrations(t *testing.T) {
	t.Parallel()
	skipIfOnlyFastTests(t)
	skipUnlessUsingUnixSocket(t)
	skipIfNotUsingUnixSocketDirectly(t)

	const previousVersionTag = "v0.6.9"
	tempDir := t.TempDir()

	lowercaseTestName := strings.ToLower(t.Name())
	if len(lowercaseTestName) > 23 {
		lowercaseTestName = lowercaseTestName[:23]
	}

	environmentConfig := runCommandSucceeds(t, "../../scripts/get-config.sh", "--docker")
	devConfig := getDevConfig(t)

	installationPath := getTempDockerInstallationPath(t)
	defer os.RemoveAll(installationPath)

	configMap := make(map[string]any)
	require.NoError(t, yaml.Unmarshal([]byte(environmentConfig), &configMap))
	configMap["environmentName"] = lowercaseTestName
	configMap["installationPath"] = installationPath
	configMap["initialDatabaseVersion"] = 1
	p, err := dataplane.GetFreePort()
	require.NoError(t, err)
	configMap["dataPlanePort"] = p
	configMap["network"] = map[string]any{"subnet": "172.255.1.0/24"}

	newEnvironmentConfigBytes, err := yaml.Marshal(configMap)
	require.NoError(t, err)
	newConfigPath := fmt.Sprintf("%s/new-environment-config.yaml", tempDir)
	require.NoError(t, os.WriteFile(newConfigPath, newEnvironmentConfigBytes, 0644))

	registry := devConfig["officialPullContainerRegistry"].(map[string]any)["fqdn"].(string)
	registryDir := devConfig["officialPullContainerRegistry"].(map[string]any)["directory"].(string)

	configMap["controlPlaneImage"] = fmt.Sprintf("%s%s/tyger-server:%s", registry, registryDir, previousVersionTag)
	configMap["dataPlaneImage"] = fmt.Sprintf("%s%s/tyger-data-plane-server:%s", registry, registryDir, previousVersionTag)
	configMap["bufferSidecarImage"] = fmt.Sprintf("%s%s/buffer-sidecar:%s", registry, registryDir, previousVersionTag)
	configMap["gatewayImage"] = fmt.Sprintf("%s%s/tyger-cli:%s", registry, registryDir, previousVersionTag)

	previousEnvironmentConfigBytes, err := yaml.Marshal(configMap)
	require.NoError(t, err)
	previousConfigPath := fmt.Sprintf("%s/environment-config.yaml", tempDir)
	require.NoError(t, os.WriteFile(previousConfigPath, previousEnvironmentConfigBytes, 0644))

	tygerPath, err := exec.LookPath("tyger")
	require.NoError(t, err)
	tygerPath, err = filepath.Abs(tygerPath)
	require.NoError(t, err)

	defer func() {
		runCommandSucceeds(t, "sudo", tygerPath, "api", "uninstall", "-f", previousConfigPath, "--delete-data", "--preserve-run-containers")
	}()

	runCommandSucceeds(t, "sudo", tygerPath, "api", "install", "-f", previousConfigPath)

	_, out, err := runCommand("sudo", tygerPath, "api", "install", "-f", newConfigPath)
	require.Error(t, err)
	require.Contains(t, out, "This version of Tyger requires the database to be migrated to at least version")

	runCommandSucceeds(t, "sudo", tygerPath, "api", "uninstall", "-f", previousConfigPath, "--preserve-run-containers")

	runCommandSucceeds(t, "sudo", tygerPath, "api", "migrations", "apply", "--latest", "--offline", "--wait", "-f", newConfigPath)

	runCommandSucceeds(t, "sudo", tygerPath, "api", "install", "-f", newConfigPath)
}
