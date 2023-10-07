<#
.SYNOPSIS
    Runs a set of `tyger login` tests using certificates in the current user's certificate store.
    Must be running on Windows.
#>

param(
    [Parameter(Mandatory = $true)]
    [string]$ServerUri

    [Parameter(Mandatory = $true)]
    [string]$servicePrincipal

    [Parameter(Mandatory = $true)]
    $KeyVaultName

    [Parameter(Mandatory = $true)]
    $CertificateName

    [Parameter(Mandatory = $true)]
    $CertificateVersion
)

$ErrorActionPreference = "Stop"
Set-StrictMode -Version Latest

function RunTests {
    param (
        [X509Certificate]$Cert
    )

    Write-Host "Running tests..."

    # Login with certificate thumbprint given as a command-line argument

    Invoke-NativeCommand tyger login $ServerUri --service-principal $servicePrincipal --cert-thumbprint $Cert.Thumbprint
    Invoke-NativeCommand tyger run list --limit 1 | Out-Null

    # Make the last token appear expired so that we can test refreshing it
    $fileContent = Get-Content -Path $cacheFile
    $propertyName = 'lastTokenExpiration'
    $propertyLine = $fileContent | Where-Object { $_ -match "^${propertyName}:" }
    $fileContent = $fileContent -replace [regex]::Escape($propertyLine), "${propertyName}: 10"
    $fileContent | Set-Content -Path $cacheFile

    Invoke-NativeCommand tyger run list --limit 1 | Out-Null

    # Login with certificate thumbprint given in an options file

    $optionsFile = New-TemporaryFile
    $options = @{
        serverUri             = $ServerUri
        servicePrincipal      = $servicePrincipal
        certificateThumbprint = $Cert.Thumbprint
        logPath               = [System.IO.Path]::GetTempPath()
    }
    $yamlContent = ''
    foreach ($key in $options.Keys) {
        $yamlContent += "$key`: $($options[$key])`r`n"
    }
    Set-Content -Path $optionsFile.FullName -Value $yamlContent

    Invoke-NativeCommand tyger logout
    Invoke-NativeCommand tyger login --file $optionsFile.FullName
    $codespecVersion = Invoke-NativeCommand tyger codespec create cert-test --image busybox --command '--' sh -c 'echo "hello world"'
    $runId = Invoke-NativeCommand tyger run create --codespec cert-test --version $codespecVersion

    # Start tyger-proxy using a certificate thumbprint

    # Start a new proxy with on a random port
    $yamlContent += "port`: 0`r`n"
    Set-Content -Path $optionsFile.FullName -Value $yamlContent

    Stop-Process -Name tyger-proxy -ErrorAction SilentlyContinue -Force
    $proxyOut = Invoke-NativeCommand tyger-proxy start -f $optionsFile --log-format json 2>&1 | ConvertFrom-Json
    $port = $proxyOut.port

    # Login to the tyger proxy
    Invoke-NativeCommand tyger logout
    Invoke-NativeCommand tyger login "http://localhost:$port"
    Invoke-NativeCommand tyger run show $runId | Out-null

    # Stop the proxy
    Get-NetTCPConnection -LocalPort $port -ErrorAction Ignore | ForEach-Object { Stop-Process $_.OwningProcess -Force -ErrorAction SilentlyContinue }

    # Validate that specifying a certificate file and a certificate thumbprint at the same time is an error
    $certFile = New-TemporaryFile
    Invoke-NativeCommandEnsureFailure -ExpectedErrorSubstring "if any flags in the group [cert-file cert-thumbprint] are set none of the others can" `
        tyger login $serverUri --service-principal $servicePrincipal --cert-thumbprint $cert.Thumbprint --cert-file $certFile.FullName

    $yamlContent += "certificatePath`: $($certFile.FullName)`r`n"
    Set-Content -Path $optionsFile.FullName -Value $yamlContent

    Invoke-NativeCommandEnsureFailure -ExpectedErrorSubstring "certificatePath and certificateThumbprint cannot both be specified" `
        tyger login -f $optionsFile.FullName

    Invoke-NativeCommandEnsureFailure -ExpectedErrorSubstring "certificatePath and certificateThumbprint cannot both be specified" `
        tyger-proxy start -f $optionsFile.FullName
}

function Invoke-NativeCommand() {
    if ($args.Count -eq 0) {
        throw "Must supply a command."
    }
    $command = $args[0]
    $commandArgs = @()
    if ($args.Count -gt 1) {
        $commandArgs = $args[1..($args.Count - 1)]
    }

    & $command $commandArgs
    $result = $LASTEXITCODE

    if ($result -ne 0) {
        # this will terminate the script
        Write-Error "Command $command $commandArgs exited with code $result."
    }
}

function Invoke-NativeCommandEnsureFailure() {
    [CmdletBinding()]
    param (
        [Parameter(Mandatory = $true)]
        [string]$ExpectedErrorSubstring,

        [Parameter(Mandatory = $true, ValueFromRemainingArguments = $true)]
        [string[]]$CommandAndArgs
    )

    if ($CommandAndArgs.Count -eq 0) {
        throw "Must supply a command."
    }

    $command = $CommandAndArgs[0]
    $commandArgs = @()
    if ($CommandAndArgs.Count -gt 1) {
        $commandArgs = $CommandAndArgs[1..($CommandAndArgs.Count - 1)]
    }

    $output = & $command $commandArgs 2>&1
    $result = $LASTEXITCODE

    if ($result -eq 0) {
        Write-Error "Command $command $commandArgs did not fail as expected."
    }

    if (-not $output.ToString().Contains("$ExpectedErrorSubstring")) {
        Write-Error "Command $command $commandArgs did not fail with expected error substring '$ExpectedErrorSubstring'. Actual output: $output"
    }

    $global:LASTEXITCODE = 0
}

Write-Host "Checking for certificate..."

# See if the certificate is already in the store
$certMetadata = Invoke-NativeCommand az keyvault certificate show --vault-name $KeyVaultName -n $CertificateName --version $CertificateVersion -o json | ConvertFrom-Json
$cert = Get-Item "cert:\CurrentUser\My\$($certMetadata.x509ThumbprintHex)" -ErrorAction SilentlyContinue
$installCertificate = -not $cert

# Have tyger use a temporary cache file to not interfere with existing state on the system
$originalCacheFileValue = $env:TYGER_CACHE_FILE
$cacheFile = New-TemporaryFile
$env:TYGER_CACHE_FILE = $cacheFile.FullName

try {
    if ($installCertificate) {
        Write-Host "Certificate not found in store. Downloading from Key Vault..."
        $tempFile = New-TemporaryFile
        $temporaryPath = $tempFile.FullName
        Remove-Item $temporaryPath

        try {
            Invoke-NativeCommand az keyvault secret download --file $temporaryPath --vault-name $KeyVaultName -n $CertificateName --version $CertificateVersion

            # The private key will not be exportable.
            $cert = Import-PfxCertificate -FilePath $temporaryPath -CertStoreLocation Cert:\CurrentUser\My
        }
        finally {
            Remove-Item $temporaryPath
        }
    }

    RunTests $cert
}
finally {
    # Restore state
    $env:TYGER_CACHE_FILE = $originalCacheFileValue
    if ($installCertificate) {
        Remove-Item $cert.PSPath -ErrorAction SilentlyContinue
    }

    Remove-Item $cacheFile.FullName -ErrorAction SilentlyContinue
}
