<#
.SYNOPSIS
  Lets the sync runbook's managed identity read Entra devices and group members.

.DESCRIPTION
  Grants two Microsoft Graph application roles to the Automation account's
  system-assigned identity:
    Device.Read.All       read devices (deviceId, accountEnabled)
    GroupMember.Read.All  read the members of the exmon-ring-* groups
  Read-only. Bicep can't grant Graph app roles (except with the preview Graph
  extension), so this is a script. Run it once after the first prod deployment.

  Needs the Azure CLI signed in as a Privileged Role Administrator or Global
  Administrator (granting Graph application permissions requires admin consent).

.EXAMPLE
  ./Grant-RunbookGraphPermissions.ps1 -IdentityObjectId <runbookIdentityObjectId output>
#>
[CmdletBinding()]
param(
    [Parameter(Mandatory)] [string] $IdentityObjectId
)
$ErrorActionPreference = 'Stop'

function Invoke-Az {
    $out = & az @args --output json
    if ($LASTEXITCODE -ne 0) { throw "az $($args -join ' ') failed" }
    if ($out) { $out | ConvertFrom-Json }
}

$graphAppId = '00000003-0000-0000-c000-000000000000' # Microsoft Graph
$graph = Invoke-Az ad sp show --id $graphAppId
foreach ($roleName in 'Device.Read.All', 'GroupMember.Read.All') {
    $role = $graph.appRoles | Where-Object { $_.value -eq $roleName -and $_.allowedMemberTypes -contains 'Application' }
    if (-not $role) { throw "Graph has no application role $roleName" }
    $existing = Invoke-Az rest --method GET --url "https://graph.microsoft.com/v1.0/servicePrincipals/$IdentityObjectId/appRoleAssignments"
    if ($existing.value | Where-Object { $_.appRoleId -eq $role.id }) {
        Write-Host "$roleName already granted"
        continue
    }
    $body = @{ principalId = $IdentityObjectId; resourceId = $graph.id; appRoleId = $role.id } | ConvertTo-Json -Compress
    $tmp = New-TemporaryFile
    Set-Content -Path $tmp -Value $body -Encoding utf8
    Invoke-Az rest --method POST --url "https://graph.microsoft.com/v1.0/servicePrincipals/$IdentityObjectId/appRoleAssignments" `
        --headers 'Content-Type=application/json' --body "@$tmp" | Out-Null
    Remove-Item $tmp
    Write-Host "granted $roleName"
}
