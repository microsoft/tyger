// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

package install

import (
	"bytes"
	"context"
	"os"
	"path"
	"testing"

	"github.com/rs/zerolog"
	"github.com/stretchr/testify/require"
)

const oldConfig = `{"kind":"azureCloud","environmentName":"demo","cloud":{"tenantId":"2dda111b-8b49-4193-b6ad-7ad797aa552f","subscriptionId":"17f10121-f320-4d47-928f-3d42adb68e01","resourceGroup":"demo","defaultLocation":"westus2","compute":{"clusters":[{"name":"demo","apiHost":true,"kubernetesVersion":1.27,"systemNodePool":{"name":"cpunp","vmSize":"Standard_DS12_v2","minCount":1,"maxCount":3},"userNodePools":[{"name":"cpunp","vmSize":"Standard_DS12_v2","minCount":1,"maxCount":10},{"name":"gpunp","vmSize":"Standard_NC6s_v3","minCount":0,"maxCount":10}]}],"managementPrincipals":[{"kind":"User","userPrincipalName":"me@example.com","objectId":"18c9e451-88aa-47d2-ae4f-1d34d55dc50c"}]},"database":{"serverName":"demo-tyger","postgresMajorVersion":16},"storage":{"buffers":[{"name":"demowestus2buf"}],"logs":{"name":"demotygerlogs"}}},"api":{"domainName":"demo-tyger.westus2.cloudapp.azure.com","auth":{"tenantId":"705ef40b-9fa6-45a3-ba0c-b7ced9af6dce","apiAppUri":"api://tyger-server","cliAppUri":"api://tyger-cli"},"buffers":{"activeLifetime":"0.00:00","softDeletedLifetime":"1.00:00"}}}`

func TestConvertConfig(t *testing.T) {
	tempdir := t.TempDir()

	oldPath := path.Join(tempdir, "old-config.json")
	newPath := path.Join(tempdir, "new-config.yaml")
	require.NoError(t, os.WriteFile(oldPath, []byte(oldConfig), 0644))

	errorBuf := bytes.Buffer{}
	ctx := zerolog.New(&errorBuf).WithContext(context.Background())
	require.NoError(t, convert(ctx, oldPath, newPath), errorBuf.String())
}

func TestParseOldConfigSuggestsConversion(t *testing.T) {
	_, err := parseConfigFromYamlBytes([]byte(oldConfig), nil, false)

	require.Contains(t, err.Error(), "tyger config convert")
}
