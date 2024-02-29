using System.Security.Cryptography;
using System.Security.Cryptography.X509Certificates;

namespace Tyger.Buffers;

public delegate byte[] SignDataFunc(byte[] data);
public delegate bool ValidateSignatureFunc(byte[] data, byte[] signature);

public static class DigitalSignature
{
    public static SignDataFunc CreateSingingFunc(string certificatePath)
    {
        var cert = X509Certificate2.CreateFromPemFile(certificatePath);

        if (cert.GetECDsaPrivateKey() is { } ecdsaKey)
        {
            return (data) => ecdsaKey.SignData(data, HashAlgorithmName.SHA256);
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
        static ValidateSignatureFunc GetValidator(string certificatePath)
        {
            ReadOnlySpan<char> certContents = File.ReadAllText(certificatePath);
            var cert = X509Certificate2.CreateFromPem(certContents);

            if (cert.GetECDsaPublicKey() is { } ecdsaKey)
            {
                return (data, signature) => ecdsaKey.VerifyData(data, signature, HashAlgorithmName.SHA256);
            }
            else if (cert.GetRSAPublicKey() is { } rsaKey)
            {
                return (data, signature) => rsaKey.VerifyData(data, signature, HashAlgorithmName.SHA256, RSASignaturePadding.Pkcs1);
            }
            else
            {
                throw new InvalidOperationException("No valid public key found for certificate with thumbprint " + cert.Thumbprint);
            }
        }

        var primaryValidator = GetValidator(primaryCertificatePath);
        if (!string.IsNullOrEmpty(secondaryCertificatePath))
        {
            var secondaryValidator = GetValidator(secondaryCertificatePath);
            return (data, signature) => primaryValidator(data, signature) || secondaryValidator(data, signature);
        }
        else
        {
            return primaryValidator;
        }
    }
}
