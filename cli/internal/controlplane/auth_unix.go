// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

//go:build !windows

package controlplane

import "github.com/AzureAD/microsoft-authentication-library-for-go/apps/confidential"

func createCredentialFromSystemCertificateStore(thumbprint string) (confidential.Credential, error) {
	panic("referencing a certificate in a certificate store is not supported on this platform")
}
