# Intune detection script for exmon. Reports "installed" when the service and the
# program exist, WITHOUT checking the version: exmon updates itself through the
# control plane, and a version check would make Intune "repair" every updated PC
# back to the packaged version.
$svc = Get-Service -Name exmon -ErrorAction SilentlyContinue
$helper = Get-Service -Name exmon-helper -ErrorAction SilentlyContinue
$exe = Join-Path $env:ProgramFiles 'exmon\exmon.exe'
if ($svc -and $helper -and (Test-Path $exe)) {
    Write-Output 'exmon installed'
    exit 0
}
exit 1
