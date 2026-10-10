<#
.SYNOPSIS
  Uploads a signed exmon.exe to the releases container and publishes its signed
  release manifest to a ring.

.DESCRIPTION
  1. Checks the exe's code signature (Get-AuthenticodeSignature).
  2. Uploads it to <account>/releases/<version>/exmon.exe with your Entra sign-in
     (no account keys). The URL is then public, read-only.
  3. Runs `exmonctl release publish`, which signs the manifest with the release
     key in Key Vault and publishes it, retained, to every device in the ring.

  Build the exe with its version baked in first, and sign it, e.g.:
    go build -ldflags "-X exmon/internal/version.Version=1.5.0" -o dist\exmon.exe .
    (then sign dist\exmon.exe with your code-signing certificate / Trusted Signing)

.EXAMPLE
  ./Publish-Release.ps1 -Version 1.5.0 -File dist\exmon.exe -StorageAccount exmonprodrelabc123 -Ring canary
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $Version,
    [Parameter(Mandatory)] [string] $File,
    [Parameter(Mandatory)] [string] $StorageAccount,
    [Parameter(Mandatory)] [string] $Ring,
    [int] $Percent = 100,
    [int] $SpreadMinutes = 60,
    [string] $ProfilePath = 'exmonctl.json',
    [string] $Exmonctl = 'exmonctl.exe'
)
$ErrorActionPreference = 'Stop'

$sig = Get-AuthenticodeSignature -FilePath $File
if ($sig.Status -ne 'Valid') { throw "$File is not validly signed ($($sig.Status)); devices would refuse it." }
Write-Host "signed by: $($sig.SignerCertificate.Subject)"

$blob = "$Version/exmon.exe"
& az storage blob upload --account-name $StorageAccount --container-name releases --name $blob `
    --file $File --auth-mode login --overwrite false --only-show-errors
if ($LASTEXITCODE -ne 0) { throw 'upload failed (does this version already exist? versions are immutable)' }
$url = "https://$StorageAccount.blob.core.windows.net/releases/$blob"
Write-Host "uploaded: $url"

& $Exmonctl release publish --profile $ProfilePath --version $Version --file $File --url $url `
    --ring $Ring --percent $Percent --spread $SpreadMinutes
if ($LASTEXITCODE -ne 0) { throw 'exmonctl release publish failed' }
