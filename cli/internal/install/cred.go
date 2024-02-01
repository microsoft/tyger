package install

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
)

var (
	ErrNotLoggedIn = errors.New("You must run `az login` before running this command")
)

// az account get-access-token fails if --tenant is provided and the user is logged in with a managed identity
// This creates a TokenProvider that works around this case
func NewMiAwareAzureCLICredential(options *azidentity.AzureCLICredentialOptions) (azcore.TokenCredential, error) {
	cmd := exec.Command("az", "account", "show")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, ErrNotLoggedIn
	}

	outputMap := make(map[string]any)
	if err := json.Unmarshal(out.Bytes(), &outputMap); err != nil {
		return nil, fmt.Errorf("failed to unmarshal az account show output: %w", err)
	}

	userMap := outputMap["user"].(map[string]any)
	if userMap["type"] == "servicePrincipal" {
		if name := userMap["name"].(string); name == "systemAssignedIdentity" || name == "userAssignedIdentity" {
			currentTenantId := outputMap["tenantId"].(string)

			if options != nil {
				if options.TenantID != "" && options.TenantID != currentTenantId || len(options.AdditionallyAllowedTenants) > 0 {
					return nil, fmt.Errorf("the managed identity account only supports tenant %s", currentTenantId)
				}

				options.TenantID = ""
			}

			innerCred, err := azidentity.NewAzureCLICredential(options)
			if err != nil {
				return nil, err
			}

			return &miAwareAzureCLICredential{
				cred:     innerCred,
				tenantID: currentTenantId,
			}, nil
		}
	}

	// return a regular AzureCLICredential
	return azidentity.NewAzureCLICredential(options)
}

type miAwareAzureCLICredential struct {
	cred     *azidentity.AzureCLICredential
	tenantID string
}

func (cred *miAwareAzureCLICredential) GetToken(ctx context.Context, opts policy.TokenRequestOptions) (azcore.AccessToken, error) {
	if opts.TenantID != "" {
		if opts.TenantID != cred.tenantID {
			return azcore.AccessToken{}, fmt.Errorf("the managed identity account only supports tenant %s", cred.tenantID)
		}

		newOps := opts
		newOps.TenantID = ""

		return cred.cred.GetToken(ctx, newOps)
	}
	return cred.cred.GetToken(ctx, opts)
}
