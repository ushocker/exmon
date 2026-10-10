<#
.SYNOPSIS
  Keeps an Event Grid namespace's exmon clients in step with Entra ring groups.

.DESCRIPTION
  Runs in Azure Automation (PowerShell 7.2) with the account's managed identity.

  For every device in an Entra group named <RingGroupPrefix><ring> (e.g.
  exmon-ring-canary), there should be an Event Grid client:
    name / authenticationName = the device's Entra device id (deviceId, not the
                                object id): the CN of its Intune SCEP certificate
    attributes = { role: device, ring: <ring>, managedBy: sync }
  A device in several ring groups gets the earliest ring in RingOrder.

  Clients created by this runbook (managedBy = sync) whose device has left every
  ring group, or is disabled in Entra, are set to Disabled, and deleted after
  DeleteAfterDays. Clients without managedBy = sync (test clients, anything made by
  hand) are never touched.

  Needs: Graph app roles Device.Read.All and GroupMember.Read.All for the managed
  identity, and the "exmon client manager" role on the namespace (both from
  deploy/).
#>
param(
    [Parameter(Mandatory)] [string] $NamespaceId,
    [string] $RingGroupPrefix = 'exmon-ring-',
    [string] $RingOrder = 'canary,early,broad',
    [int] $DeleteAfterDays = 30,
    [switch] $WhatIf
)
$ErrorActionPreference = 'Stop'
$api = '2025-02-15'

Connect-AzAccount -Identity | Out-Null

function Get-Token([string] $resource) {
    $t = Get-AzAccessToken -ResourceUrl $resource
    # Newer Az.Accounts versions return the token as a SecureString.
    if ($t.Token -is [securestring]) {
        return [System.Net.NetworkCredential]::new('', $t.Token).Password
    }
    return $t.Token
}

function Invoke-Graph([string] $uri) {
    $headers = @{ Authorization = "Bearer $(Get-Token 'https://graph.microsoft.com')" }
    $items = @()
    while ($uri) {
        $page = Invoke-RestMethod -Uri $uri -Headers $headers
        $items += $page.value
        $uri = $page.'@odata.nextLink'
    }
    return $items
}

# 1. Desired clients from the ring groups.
$order = $RingOrder.Split(',') | ForEach-Object { $_.Trim() }
$groups = Invoke-Graph "https://graph.microsoft.com/v1.0/groups?`$filter=startswith(displayName,'$RingGroupPrefix')&`$select=id,displayName"
$desired = @{}   # deviceId -> ring
foreach ($g in $groups) {
    $ring = $g.displayName.Substring($RingGroupPrefix.Length)
    if ($order -notcontains $ring) { Write-Warning "group $($g.displayName): ring '$ring' is not in RingOrder; ignored"; continue }
    $members = Invoke-Graph "https://graph.microsoft.com/v1.0/groups/$($g.id)/transitiveMembers/microsoft.graph.device?`$select=deviceId,accountEnabled"
    foreach ($d in $members) {
        if (-not $d.accountEnabled -or -not $d.deviceId) { continue }
        $id = $d.deviceId.ToLower()
        if (-not $desired.ContainsKey($id) -or $order.IndexOf($ring) -lt $order.IndexOf($desired[$id])) {
            $desired[$id] = $ring
        }
    }
}
Write-Output "desired: $($desired.Count) device(s) in $($groups.Count) ring group(s)"

# 2. Current clients.
$existing = @{}
$path = "$NamespaceId/clients?api-version=$api"
while ($path) {
    $resp = (Invoke-AzRestMethod -Path $path -Method GET).Content | ConvertFrom-Json
    foreach ($c in $resp.value) { $existing[$c.name.ToLower()] = $c }
    $path = if ($resp.nextLink) { ([uri]$resp.nextLink).PathAndQuery } else { $null }
}

function Set-Client([string] $id, [string] $ring, [string] $state, $disabledSince) {
    $attrs = @{ role = 'device'; ring = $ring; managedBy = 'sync' }
    if ($disabledSince) { $attrs.disabledSince = $disabledSince }
    $body = @{ properties = @{
        authenticationName = $id
        clientCertificateAuthentication = @{ validationScheme = 'SubjectMatchesAuthenticationName' }
        state = $state
        attributes = $attrs
    } } | ConvertTo-Json -Depth 5
    if ($WhatIf) { Write-Output "would set $id ring=$ring state=$state"; return }
    $r = Invoke-AzRestMethod -Path "$NamespaceId/clients/$id`?api-version=$api" -Method PUT -Payload $body
    if ($r.StatusCode -ge 300) { Write-Warning "$id : HTTP $($r.StatusCode) $($r.Content)" }
}

# 3. Create or update desired clients.
$created = 0; $updated = 0
foreach ($id in $desired.Keys) {
    $ring = $desired[$id]
    $c = $existing[$id]
    if (-not $c) { Set-Client $id $ring 'Enabled' $null; $created++; continue }
    if ($c.properties.attributes.managedBy -ne 'sync') { Write-Warning "client $id exists but isn't managed by sync; left alone"; continue }
    if ($c.properties.attributes.ring -ne $ring -or $c.properties.state -ne 'Enabled') {
        Set-Client $id $ring 'Enabled' $null; $updated++
    }
}

# 4. Disable (then delete) synced clients no longer wanted.
$disabled = 0; $deleted = 0
$now = (Get-Date).ToUniversalTime()
foreach ($id in $existing.Keys) {
    $c = $existing[$id]
    if ($c.properties.attributes.managedBy -ne 'sync' -or $desired.ContainsKey($id)) { continue }
    if ($c.properties.state -ne 'Disabled') {
        Set-Client $id $c.properties.attributes.ring 'Disabled' $now.ToString('o'); $disabled++
    } elseif ($c.properties.attributes.disabledSince -and ([datetime]$c.properties.attributes.disabledSince).AddDays($DeleteAfterDays) -lt $now) {
        if (-not $WhatIf) { Invoke-AzRestMethod -Path "$NamespaceId/clients/$id`?api-version=$api" -Method DELETE | Out-Null }
        $deleted++
    }
}
Write-Output "created $created, updated $updated, disabled $disabled, deleted $deleted"
