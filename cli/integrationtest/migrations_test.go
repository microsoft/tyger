//go:build integrationtest

package integrationtest

import (
	"fmt"
	"os"
	"testing"

	"github.com/microsoft/tyger/cli/internal/install"
	"github.com/stretchr/testify/require"
	"sigs.k8s.io/yaml"
)

func TestMigrations(t *testing.T) {
	stdout, stderr, err := runCommand("helm", "get", "values", "tyger", "-n", "tyger", "-o", "yaml")
	if err != nil {
		t.Log("Unable to get Helm values. Ensure that `make up` and `make set-context` have been run.")
		t.Logf("stdout: %s", stdout)
		t.Logf("stderr: %s", stderr)
		t.FailNow()
	}
	helmValues := make(map[string]any)
	require.NoError(t, yaml.Unmarshal([]byte(stdout), &helmValues))

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

	environmentConfig := runCommandSucceeds(t, "../../scripts/get-config.sh")
	tempDir := t.TempDir()
	configPath := fmt.Sprintf("%s/environment-config.yaml", tempDir)
	require.NoError(t, os.WriteFile(configPath, []byte(environmentConfig), 0644))

	tygerArgs := []string{
		"api",
		"migrations",
		"apply",
		"--latest",
		"--wait",
		"-f", configPath,
		"--set", fmt.Sprintf("api.helm.tyger.values.database.databaseName=%s", temporaryDatabaseName),
	}

	runTygerSucceeds(t, tygerArgs...)

	defer func() {
		createPsqlCommandBuilder().
			Arg("--command").Arg(fmt.Sprintf("DROP DATABASE %s", temporaryDatabaseName)).
			RunSucceeds(t)
	}()
}
