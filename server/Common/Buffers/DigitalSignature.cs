// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;

namespace Tyger.Common.Buffers;

public delegate byte[] SignDataFunc(byte[] data);
public delegate bool ValidateSignatureFunc(byte[] data, byte[] signature);

public static class DigitalSignature
{
    public static SignDataFunc CreateSingingFunc(string certificatePath)
    {
        var cert = X509Certificate2.CreateFromPemFile(certificatePath);

        if (cert.GetECDsaPrivateKey() is { } ecdsaKey)
        {
            return (data) => ecdsaKey.SignData(data, HashAlgorithmName.SHA256, DSASignatureFormat.Rfc3279DerSequence);
        }
        else if (cert.GetRSAPrivateKey() is { } rsaKey)
        {
            return (data) => rsaKey.SignData(data, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
        }
        else
        {
            throw new InvalidOperationException("No valid private key found for certificate with thumbprint " + cert.Thumbprint);
        }
    }

    public static ValidateSignatureFunc CreateValidationFunc(string primaryCertificatePath, string? secondaryCertificatePath)
    {
        static Func<byte[], byte[], bool> GetHashValidator(string certificatePath)
        {
            ReadOnlySpan<char> certContents = File.ReadAllText(certificatePath);
            var cert = X509Certificate2.CreateFromPem(certContents);

            if (cert.GetECDsaPublicKey() is { } ecdsaKey)
            {
                return (data, signature) => ecdsaKey.VerifyHash(data, signature, DSASignatureFormat.Rfc3279DerSequence);
            }
            else if (cert.GetRSAPublicKey() is { } rsaKey)
            {
                return (data, signature) => rsaKey.VerifyHash(data, signature, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
            }
            else
            {
                throw new InvalidOperationException("No valid public key found for certificate with thumbprint " + cert.Thumbprint);
            }
        }

        var primaryValidator = GetHashValidator(primaryCertificatePath);
        var secondaryValidator = string.IsNullOrEmpty(secondaryCertificatePath) ? null : GetHashValidator(secondaryCertificatePath);

        return (data, signature) =>
        {
            var hash = SHA256.HashData(data);
            return primaryValidator(hash, signature) || (secondaryValidator != null && secondaryValidator(hash, signature));
        };
    }
}
