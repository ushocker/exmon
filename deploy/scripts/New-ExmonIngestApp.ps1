<#
.SYNOPSIS
  Creates (or adds a new secret to) the Entra app exmon uses to write events to
  Azure Monitor. Bicep can't create app registrations' secrets, so this is a script.

.DESCRIPTION
  The app gets no API permissions and no roles here. Its only access is the
  "Monitoring Metrics Publisher" role on the exmon data collection rule, which
  main.bicep grants once you pass the service principal's object id
  (ingestPrincipalId). So the secret can write rows to ExmonEvents_CL and nothing
  else: it can't read logs, list resources or sign in anywhere useful.

  Run it again with -AddSecret to rotate: it adds a second secret (both work),
  you push the new one to the fleet in the config, then remove the old one.

  Needs the Azure CLI (az) signed in as someone allowed to create app
  registrations (e.g. Application Developer).

.EXAMPLE
  ./New-ExmonIngestApp.ps1 -Name exmon-ingest-dev -SecretMonths 12
#>
[CmdletBinding()]
param(
    [string] $Name = 'exmon-ingest',
    [ValidateRange(1, 24)]
    [int] $SecretMonths = 12,
    [switch] $AddSecret
)
$ErrorActionPreference = 'Stop'

function Invoke-Az {
    # Runs az and fails loudly; returns parsed JSON.
    $out = & az @args --output json
    if ($LASTEXITCODE -ne 0) { throw "az $($args -join ' ') failed" }
    if ($out) { $out | ConvertFrom-Json }
}

$tenant = (Invoke-Az account show).tenantId
$app = Invoke-Az ad app list --display-name $Name | Select-Object -First 1

if (-not $app) {
    if ($AddSecret) { throw "No app named $Name exists yet; run without -AddSecret first." }
    Write-Host "Creating app registration $Name ..."
    # Single tenant, no redirect URIs, no API permissions.
    $app = Invoke-Az ad app create --display-name $Name --sign-in-audience AzureADMyOrg
} elseif (-not $AddSecret) {
    throw "An app named $Name already exists (appId $($app.appId)). Use -AddSecret to rotate its secret."
}

$sp = Invoke-Az ad sp list --filter "appId eq '$($app.appId)'" | Select-Object -First 1
if (-not $sp) {
    $sp = Invoke-Az ad sp create --id $app.appId
}

$end = (Get-Date).ToUniversalTime().AddMonths($SecretMonths).ToString('yyyy-MM-dd')
$cred = Invoke-Az ad app credential reset --id $app.appId --append --display-name "exmon $(Get-Date -Format yyyy-MM-dd)" --end-date $end

Write-Host ""
Write-Host "Done. Put these in the azureMonitor remote's settings (the secret is shown only once):"
Write-Host "  tenantId     : $tenant"
Write-Host "  clientId     : $($app.appId)"
Write-Host "  clientSecret : $($cred.password)"
Write-Host "  (expires $end)"
Write-Host ""
Write-Host "Set ingestPrincipalId = '$($sp.id)' in your .bicepparam file and redeploy main.bicep,"
Write-Host "so the app gets Monitoring Metrics Publisher on the data collection rule."
Write-Host "Role assignments can take up to 30 minutes to apply; until then sends get HTTP 403 and are retried."
