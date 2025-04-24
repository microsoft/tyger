package cloudinstall

import (
	"context"
	"fmt"
	"reflect"
	"slices"

	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/keyvault/armkeyvault"
	"github.com/Azure/azure-sdk-for-go/sdk/resourcemanager/msi/armmsi"
	"github.com/microsoft/tyger/cli/internal/install"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/rest"
	"k8s.io/utils/ptr"
)

func (inst *Installer) RemoveKeyVaultAccess(ctx context.Context, principalId string) error {
	vaultsClient, err := armkeyvault.NewVaultsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create key vault client: %w", err)
	}

	kv, err := vaultsClient.Get(ctx, inst.Config.Cloud.TlsCertificate.KeyVault.ResourceGroup, inst.Config.Cloud.TlsCertificate.KeyVault.Name, nil)
	if err != nil {
		return fmt.Errorf("failed to get key vault: %w", err)
	}

	if ptr.Deref(kv.Properties.EnableRbacAuthorization, false) {
		if err := removeRbacRoleAssignments(ctx, principalId, *kv.ID, inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
			return fmt.Errorf("failed to remove key vault user role: %w", err)
		}

		return nil
	}

	if kv.Properties.AccessPolicies != nil {
		for _, ap := range kv.Properties.AccessPolicies {
			if ap.ObjectID != nil && *ap.ObjectID == principalId {
				_, err := vaultsClient.UpdateAccessPolicy(
					ctx, inst.Config.Cloud.TlsCertificate.KeyVault.ResourceGroup, inst.Config.Cloud.TlsCertificate.KeyVault.Name, armkeyvault.AccessPolicyUpdateKindRemove,
					armkeyvault.VaultAccessPolicyParameters{
						Properties: &armkeyvault.VaultAccessPolicyProperties{
							AccessPolicies: []*armkeyvault.AccessPolicyEntry{ap},
						}},
					nil)

				if err != nil {
					return fmt.Errorf("failed to remove key vault access policy: %w", err)
				}

				return nil
			}
		}
	}

	return nil
}

func (inst *Installer) GrantAccessToKeyVault(ctx context.Context, principalId string) error {
	vaultsClient, err := armkeyvault.NewVaultsClient(inst.Config.Cloud.SubscriptionID, inst.Credential, nil)
	if err != nil {
		return fmt.Errorf("failed to create key vault client: %w", err)
	}

	kv, err := vaultsClient.Get(ctx, inst.Config.Cloud.TlsCertificate.KeyVault.ResourceGroup, inst.Config.Cloud.TlsCertificate.KeyVault.Name, nil)
	if err != nil {
		return fmt.Errorf("failed to get key vault: %w", err)
	}

	if ptr.Deref(kv.Properties.EnableRbacAuthorization, false) {
		if err := assignRbacRole(ctx, []string{principalId}, false, *kv.ID, "Key Vault Certificate User", inst.Config.Cloud.SubscriptionID, inst.Credential); err != nil {
			return fmt.Errorf("failed to assign key vault certificate user role: %w", err)
		}
	}

	if kv.Properties.AccessPolicies != nil {
		for _, ap := range kv.Properties.AccessPolicies {
			if ap.ObjectID != nil && *ap.ObjectID == principalId {
				if ap.Permissions != nil && ap.Permissions.Secrets != nil && ap.Permissions.Certificates != nil {
					hasAccess :=
						slices.ContainsFunc(
							ap.Permissions.Secrets,
							func(item *armkeyvault.SecretPermissions) bool {
								return *item == armkeyvault.SecretPermissionsGet
							}) &&
							slices.ContainsFunc(
								ap.Permissions.Certificates,
								func(item *armkeyvault.CertificatePermissions) bool {
									return *item == armkeyvault.CertificatePermissionsGet
								})

					if hasAccess {
						return nil
					}
				}

				ap.Permissions = &armkeyvault.Permissions{
					Secrets:      []*armkeyvault.SecretPermissions{ptr.To(armkeyvault.SecretPermissionsGet)},
					Certificates: []*armkeyvault.CertificatePermissions{ptr.To(armkeyvault.CertificatePermissionsGet)},
				}

				_, err = vaultsClient.UpdateAccessPolicy(
					ctx,
					inst.Config.Cloud.TlsCertificate.KeyVault.ResourceGroup,
					inst.Config.Cloud.TlsCertificate.KeyVault.Name,
					armkeyvault.AccessPolicyUpdateKindReplace,
					armkeyvault.VaultAccessPolicyParameters{Properties: &armkeyvault.VaultAccessPolicyProperties{AccessPolicies: []*armkeyvault.AccessPolicyEntry{ap}}}, nil)

				if err != nil {
					return fmt.Errorf("failed to update key vault access policy: %w", err)
				}

				return nil
			}
		}
	}

	_, err = vaultsClient.UpdateAccessPolicy(
		ctx,
		inst.Config.Cloud.TlsCertificate.KeyVault.ResourceGroup,
		inst.Config.Cloud.TlsCertificate.KeyVault.Name,
		armkeyvault.AccessPolicyUpdateKindAdd,
		armkeyvault.VaultAccessPolicyParameters{
			Properties: &armkeyvault.VaultAccessPolicyProperties{
				AccessPolicies: []*armkeyvault.AccessPolicyEntry{
					{
						TenantID: ptr.To(inst.Config.Cloud.TenantID),
						ObjectID: ptr.To(principalId),
						Permissions: &armkeyvault.Permissions{
							Secrets:      []*armkeyvault.SecretPermissions{ptr.To(armkeyvault.SecretPermissionsGet)},
							Certificates: []*armkeyvault.CertificatePermissions{ptr.To(armkeyvault.CertificatePermissionsGet)},
						},
					},
				},
			},
		}, nil)

	if err != nil {
		return fmt.Errorf("failed to add key vault access policy: %w", err)
	}

	return nil
}

func (inst *Installer) addSecretProviderClass(ctx context.Context, namespace string, identityPromise *install.Promise[*armmsi.Identity], restConfigPromise *install.Promise[*rest.Config]) error {
	identity, err := identityPromise.Await()
	if err != nil {
		return install.ErrDependencyFailed
	}

	restConfig, err := restConfigPromise.Await()
	if err != nil {
		return install.ErrDependencyFailed
	}

	spc := &unstructured.Unstructured{
		Object: map[string]any{
			"apiVersion": "secrets-store.csi.x-k8s.io/v1",
			"kind":       "SecretProviderClass",
			"metadata": map[string]any{
				"name": inst.Config.Cloud.TlsCertificate.CertificateName,
			},
			"spec": map[string]any{
				"provider": "azure",
				"parameters": map[string]any{
					"tenantID":       inst.Config.Cloud.TenantID,
					"usePodIdentity": "false",
					"clientID":       *identity.Properties.ClientID,
					"keyvaultName":   inst.Config.Cloud.TlsCertificate.KeyVault.Name,
					"cloudName":      "",
					"objects": fmt.Sprintf(`array:
  - |
    objectName: %s
    objectType: secret
    objectAlias: tls`, inst.Config.Cloud.TlsCertificate.CertificateName),
				},
			},
		},
	}

	dynamicClient, err := dynamic.NewForConfig(restConfig)
	if err != nil {
		return fmt.Errorf("failed to create dynamic client: %w", err)
	}

	spcResource := schema.GroupVersionResource{
		Group:    "secrets-store.csi.x-k8s.io",
		Version:  "v1",
		Resource: "secretproviderclasses",
	}

	_, err = dynamicClient.Resource(spcResource).Namespace(namespace).Create(ctx, spc, metav1.CreateOptions{})
	if err != nil {
		if apierrors.IsAlreadyExists(err) {
			// Retrieve the existing SecretProviderClass
			existingSPC, getErr := dynamicClient.Resource(spcResource).Namespace(namespace).Get(ctx, spc.GetName(), metav1.GetOptions{})
			if getErr != nil {
				return fmt.Errorf("failed to get existing SecretProviderClass: %w", getErr)
			}

			existingSpec, ok := existingSPC.Object["spec"].(map[string]any)
			if !ok {
				return fmt.Errorf("failed to parse spec of existing SecretProviderClass")
			}

			if reflect.DeepEqual(existingSpec, spc.Object["spec"]) {
				return nil
			}

			spc.SetResourceVersion(existingSPC.GetResourceVersion())
			if _, updateErr := dynamicClient.Resource(spcResource).Namespace(namespace).Update(ctx, spc, metav1.UpdateOptions{}); updateErr != nil {
				return fmt.Errorf("failed to update SecretProviderClass: %w", updateErr)
			}

			return nil
		}
		return fmt.Errorf("failed to create SecretProviderClass: %w", err)
	}

	return nil
}
