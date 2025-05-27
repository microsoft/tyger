// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"bytes"
	"context"
	"os"
	"path"
	"testing"

	"github.com/google/uuid"
	"github.com/microsoft/tyger/cli/internal/install/cloudinstall"
	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const preOrganizationsConfig = `{"kind":"azureCloud","environmentName":"demo","cloud":{"tenantId":"2dda111b-8b49-4193-b6ad-7ad797aa552f","subscriptionId":"17f10121-f320-4d47-928f-3d42adb68e01","resourceGroup":"demo","defaultLocation":"westus2","compute":{"clusters":[{"name":"demo","apiHost":true,"kubernetesVersion":1.27,"systemNodePool":{"name":"cpunp","vmSize":"Standard_DS12_v2","minCount":1,"maxCount":3},"userNodePools":[{"name":"cpunp","vmSize":"Standard_DS12_v2","minCount":1,"maxCount":10},{"name":"gpunp","vmSize":"Standard_NC6s_v3","minCount":0,"maxCount":10}]}],"managementPrincipals":[{"kind":"User","userPrincipalName":"me@example.com","objectId":"18c9e451-88aa-47d2-ae4f-1d34d55dc50c"}]},"database":{"serverName":"demo-tyger","postgresMajorVersion":16},"storage":{"buffers":[{"name":"demowestus2buf"}],"logs":{"name":"demotygerlogs"}}},"api":{"domainName":"demo-tyger.westus2.cloudapp.azure.com","auth":{"tenantId":"705ef40b-9fa6-45a3-ba0c-b7ced9af6dce","apiAppUri":"api://tyger-server","cliAppUri":"api://tyger-cli"},"buffers":{"activeLifetime":"0.00:00","softDeletedLifetime":"1.00:00"}}}`
const preAccessControlConfig = `{"kind":"azureCloud","environmentName":"demo","cloud":{"tenantId":"2dda111b-8b49-4193-b6ad-7ad797aa552f","subscriptionId":"17f10121-f320-4d47-928f-3d42adb68e01","resourceGroup":"demo","defaultLocation":"westus2","compute":{"clusters":[{"name":"demo","apiHost":true,"kubernetesVersion":"1.30","sku":null,"systemNodePool":{"name":"system","vmSize":"Standard_DS2_v2","minCount":1,"maxCount":3},"userNodePools":[{"name":"cpunp","vmSize":"Standard_DS2_v2","minCount":1,"maxCount":10},{"name":"gpunp","vmSize":"Standard_NC6s_v3","minCount":0,"maxCount":10}]}],"managementPrincipals":[{"kind":"User","userPrincipalName":"me@example.com","objectId":"18c9e451-88aa-47d2-ae4f-1d34d55dc50c"}]},"database":{"serverName":"demo-tyger","postgresMajorVersion":16}},"organizations":[{"name":"default","cloud":{"storage":{"buffers":[{"name":"demowestus2buf"}],"logs":{"name":"demotygerlogs"}}},"api":{"domainName":"demo-tyger.westus2.cloudapp.azure.com","tlsCertificateProvider":"LetsEncrypt","auth":{"tenantId":"72f988bf-86f1-41af-91ab-2d7cd011db47","apiAppUri":"api://tyger-server","cliAppUri":"api://tyger-cli"}}}]}`

func TestConvertPreOrganizationsConfig(t *testing.T) {
	tempdir := t.TempDir()

	oldPath := path.Join(tempdir, "old-config.json")
	newPath := path.Join(tempdir, "new-config.yaml")
	require.NoError(t, os.WriteFile(oldPath, []byte(preOrganizationsConfig), 0644))

	authConfig := &cloudinstall.AccessControlConfig{
		TenantID:  "705ef40b-9fa6-45a3-ba0c-b7ced9af6dce",
		ApiAppUri: "api://tyger-server",
		ApiAppId:  uuid.New().String(),
		CliAppUri: "api://tyger-cli",
		CliAppId:  uuid.New().String(),
	}

	authConfigPath := path.Join(tempdir, "authconfig.yml")

	authConfigFile, err := os.OpenFile(authConfigPath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0644)
	require.NoError(t, err)
	defer authConfigFile.Close()

	cloudinstall.PrettyPrintAccessControlConfig(authConfig, authConfigFile)
	authConfigFile.Close()

	errorBuf := bytes.Buffer{}
	ctx := zerolog.New(&errorBuf).WithContext(context.Background())
	require.NoError(t, convert(ctx, oldPath, newPath), errorBuf.String())
}

func TestParsePreOrganizationConfigSuggestsConversion(t *testing.T) {
	_, err := parseConfig([]byte(preOrganizationsConfig))
	require.Error(t, err)

	require.Contains(t, err.Error(), "tyger config convert")
}

func TestParsePreAccessControlConfigSuggestsConversion(t *testing.T) {
	_, err := parseConfig([]byte(preAccessControlConfig))
	require.Error(t, err)

	require.Contains(t, err.Error(), "tyger config convert")
}
