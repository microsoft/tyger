// Copyright (c) Microsoft Corporation.
// Licensed under the MIT License.

using System.Security.Cryptography;

namespace Tyger.Common.Buffers;

public delegate byte[] SignDataFunc(byte[] data);
public delegate bool ValidateSignatureFunc(byte[] data, byte[] signature);

public static class DigitalSignature
{
    public static AsymmetricAlgorithm CreateAsymmetricAlgorithmFromPem(string pemFilePath)
    {
        string pemText = File.ReadAllText(pemFilePath);

        try
        {
            var ecdsa = ECDsa.Create();
            ecdsa.ImportFromPem(pemText);
            return ecdsa;
        }
        catch (Exception)
        {
            try
            {
                var rsa = RSA.Create();
                rsa.ImportFromPem(pemText);
                return rsa;
            }
            catch (Exception e)
            {
                throw new InvalidOperationException("The PEM file does not contain a valid ECDSA or RSA key.", e);
            }
        }
    }

    public static SignDataFunc CreateSingingFunc(AsymmetricAlgorithm asymmetricAlgorithm)
    {
        return asymmetricAlgorithm switch
        {
            ECDsa ecdsa => (data) => ecdsa.SignData(data, HashAlgorithmName.SHA256, DSASignatureFormat.Rfc3279DerSequence),
            RSA rsa => (data) => rsa.SignData(data, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1),
            _ => throw new InvalidOperationException("The provided AsymmetricAlgorithm is not supported.")
        };
    }

    public static ValidateSignatureFunc CreateValidationFunc(AsymmetricAlgorithm primaryKey, AsymmetricAlgorithm? secondaryKey)
    {
        static Func<byte[], byte[], bool> GetHashValidator(AsymmetricAlgorithm asymmetricAlgorithm)
        {
            return asymmetricAlgorithm switch
            {
                ECDsa ecdsa => (hash, sig) => ecdsa.VerifyHash(hash, sig, DSASignatureFormat.Rfc3279DerSequence),
                RSA rsa => (hash, sig) => rsa.VerifyHash(hash, sig, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1),
                _ => throw new InvalidOperationException("The provided AsymmetricAlgorithm is not supported.")
            };
        }

        var primaryValidator = GetHashValidator(primaryKey);
        var secondaryValidator = secondaryKey == null ? null : GetHashValidator(secondaryKey);

        return (data, signature) =>
        {
            var hash = SHA256.HashData(data);
            return primaryValidator(hash, signature) || (secondaryValidator != null && secondaryValidator(hash, signature));
        };
    }
}
