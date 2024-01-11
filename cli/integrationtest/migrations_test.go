//go:build integrationtest

package integrationtest

import (
	"context"
	"fmt"
	"os"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
	koanfyaml "github.com/knadh/koanf/parsers/yaml"
	"github.com/knadh/koanf/providers/file"
	"github.com/knadh/koanf/v2"
	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/mitchellh/mapstructure"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestMigrations(t *testing.T) {
	t.Parallel()

	environmentConfig := runCommandSucceeds(t, "../../scripts/get-config.sh")
	tempDir := t.TempDir()
	configPath := fmt.Sprintf("%s/environment-config.yaml", tempDir)
	require.NoError(t, os.WriteFile(configPath, []byte(environmentConfig), 0644))

	config := install.EnvironmentConfig{}

	koanfConfig := koanf.New(".")
	require.NoError(t, koanfConfig.Load(file.Provider(configPath), koanfyaml.Parser()))
	require.NoError(t, koanfConfig.UnmarshalWithConf("", &config, koanf.UnmarshalConf{
		Tag: "json",
		DecoderConfig: &mapstructure.DecoderConfig{
			WeaklyTypedInput: true,
			ErrorUnused:      true,
			Result:           &config,
		},
	}))

	ctx := context.Background()

	ctx = install.SetConfigOnContext(ctx, &config)
	cred, err := azidentity.NewAzureCLICredential(
		&azidentity.AzureCLICredentialOptions{
			TenantID: config.Cloud.TenantID,
		})
	ctx = install.SetAzureCredentialOnContext(ctx, cred)

	restConfig, err := install.GetUserRESTConfig(ctx)
	require.NoError(t, err)

	// this is a try run to get the Helm values
	_, helmValuesYaml, err := install.InstallTygerHelmChart(ctx, restConfig, true)
	require.NoError(t, err)

	helmValues := make(map[string]any)
	require.NoError(t, yaml.Unmarshal([]byte(helmValuesYaml), &helmValues))

	username := runCommandSucceeds(t, "az", "account", "show", "--query", "user.name", "-o", "tsv")
	password := runCommandSucceeds(t, "az", "account", "get-access-token", "--resource-type", "oss-rdbms", "--query", "accessToken", "-o", "tsv")
	host := helmValues["database"].(map[string]any)["host"].(string)
	port := helmValues["database"].(map[string]any)["port"].(float64)
	databaseName := helmValues["database"].(map[string]any)["databaseName"].(string)

	temporaryDatabaseName := fmt.Sprintf("tygertest%s", install.RandomAlphanumString(8))

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

	tygerMigrationApplyArgs := []string{
		"api", "migrations", "apply", "--latest", "--wait",
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

	defer func() {
		createPsqlCommandBuilder().
			Arg("--command").Arg(fmt.Sprintf("DROP DATABASE %s", temporaryDatabaseName)).
			RunSucceeds(t)
	}()
}
