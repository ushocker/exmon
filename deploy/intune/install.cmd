@echo off
rem Intune Win32 app install command for exmon (runs as SYSTEM).
rem Package this folder with exmon.exe, exmon-helper.exe and exmon.json using IntuneWinAppUtil.
rem Edit the values below for your environment (see docs/deploy/intune.md).
rem
rem  - exmon.json goes to %ProgramData%\exmon\config (admin-only; it holds the Azure Monitor secret)
rem  - `exmon install` copies both exes to Program Files and installs/updates the exmon service
rem    (LocalService) and the exmon-helper service (LocalSystem; applies updates, grants key access)
setlocal
set CONTROL_HOST=exmon-prod-eg.eastus-1.ts.eventgrid.azure.net
set CERT_ISSUER=exmon Issuing CA
set UPDATE_HOST=exmonprodrelabc123.blob.core.windows.net
set PUBLISHER=Contoso Ltd

"%~dp0exmon.exe" install --control-host "%CONTROL_HOST%" --control-cert-issuer "%CERT_ISSUER%" --update-hosts "%UPDATE_HOST%" --update-publisher "%PUBLISHER%"
if errorlevel 1 exit /b %errorlevel%

rem install has locked down %ProgramData%\exmon\config; copy the config in afterwards.
if exist "%~dp0exmon.json" (
  copy /y "%~dp0exmon.json" "%ProgramData%\exmon\config\exmon.json" >nul || exit /b 1
  sc stop exmon >nul 2>&1
  timeout /t 5 /nobreak >nul
  sc start exmon >nul
)
exit /b 0
