// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build integrationtest

package integrationtest

// func TestMigrations(t *testing.T) {
// 	t.Parallel()
// 	skipIfUsingUnixSocket(t) // TODO: use better skip condition

// 	environmentConfig := runCommandSucceeds(t, "../../scripts/get-config.sh")
// 	tempDir := t.TempDir()
// 	configPath := fmt.Sprintf("%s/environment-config.yaml", tempDir)
// 	require.NoError(t, os.WriteFile(configPath, []byte(environmentConfig), 0644))

// 	config := cloudinstall.CloudEnvironmentConfig{}

// 	koanfConfig := koanf.New(".")
// 	require.NoError(t, koanfConfig.Load(file.Provider(configPath), koanfyaml.Parser()))
// 	require.NoError(t, koanfConfig.UnmarshalWithConf("", &config, koanf.UnmarshalConf{
// 		Tag: "json",
// 		DecoderConfig: &mapstructure.DecoderConfig{
// 			WeaklyTypedInput: true,
// 			ErrorUnused:      true,
// 			Squash:           true,
// 			Result:           &config,
// 		},
// 	}))

// 	ctx := context.Background()

// 	ctx = install.SetEnvironmentConfigOnContext(ctx, &config)
// 	cred, err := cloudinstall.NewMiAwareAzureCLICredential(
// 		&azidentity.AzureCLICredentialOptions{
// 			TenantID: config.Cloud.TenantID,
// 		})
// 	ctx = cloudinstall.SetAzureCredentialOnContext(ctx, cred)

// 	restConfig, err := cloudinstall.GetUserRESTConfig(ctx)
// 	require.NoError(t, err)

// 	// this is a try run to get the Helm values
// 	_, helmValuesYaml, err := cloudinstall.InstallTygerHelmChart(ctx, restConfig, true)
// 	require.NoError(t, err)

// 	helmValues := make(map[string]any)
// 	require.NoError(t, yaml.Unmarshal([]byte(helmValuesYaml), &helmValues))

// 	username := runCommandSucceeds(t, "az", "account", "show", "--query", "user.name", "-o", "tsv")
// 	password := runCommandSucceeds(t, "az", "account", "get-access-token", "--resource-type", "oss-rdbms", "--query", "accessToken", "-o", "tsv")
// 	if username == "systemAssignedIdentity" || username == "userAssignedIdentity" {
// 		// Need to get the managed identity app id from the token
// 		claims := jwt.MapClaims{}
// 		_, _, err = jwt.NewParser().ParseUnverified(password, claims)
// 		require.NoError(t, err)
// 		username = claims["appid"].(string)
// 	}

// 	host := helmValues["database"].(map[string]any)["host"].(string)
// 	port := helmValues["database"].(map[string]any)["port"].(float64)
// 	databaseName := helmValues["database"].(map[string]any)["databaseName"].(string)

// 	temporaryDatabaseName := fmt.Sprintf("tygertest%s", cloudinstall.RandomAlphanumString(8))

// 	createPsqlCommandBuilder := func() *CmdBuilder {
// 		return NewCmdBuilder("psql",
// 			"--host", host,
// 			"--port", fmt.Sprintf("%d", int(port)),
// 			"--username", username,
// 			"--dbname", databaseName).
// 			Env("PGPASSWORD", password)
// 	}

// 	createPsqlCommandBuilder().
// 		Arg("--command").Arg(fmt.Sprintf("CREATE DATABASE %s", temporaryDatabaseName)).
// 		RunSucceeds(t)

// 	tygerMigrationApplyArgs := []string{
// 		"api", "migrations", "apply", "--latest", "--offline", "--wait",
// 		"-f", configPath,
// 		"--set", fmt.Sprintf("api.helm.tyger.values.database.databaseName=%s", temporaryDatabaseName),
// 	}

// 	runTygerSucceeds(t, tygerMigrationApplyArgs...)

// 	logs := runTygerSucceeds(t, "api", "migrations", "logs", "1",
// 		"-f", configPath,
// 		"--set", fmt.Sprintf("api.helm.tyger.values.database.databaseName=%s", temporaryDatabaseName))

// 	assert.Contains(t, logs, "Migration 1 complete")

// 	logs = runTygerSucceeds(t, "api", "migrations", "logs", "2",
// 		"-f", configPath,
// 		"--set", fmt.Sprintf("api.helm.tyger.values.database.databaseName=%s", temporaryDatabaseName))

// 	assert.Contains(t, logs, "Migration 2 complete")

// 	defer func() {
// 		createPsqlCommandBuilder().
// 			Arg("--command").Arg(fmt.Sprintf("DROP DATABASE %s", temporaryDatabaseName)).
// 			RunSucceeds(t)
// 	}()
// }
