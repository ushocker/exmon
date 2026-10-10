@echo off
rem Intune Win32 app uninstall command (runs as SYSTEM). Removes both services; the
rem program and %ProgramData%\exmon (logs, config) are left for you to delete.
"%ProgramFiles%\exmon\exmon.exe" uninstall
exit /b %errorlevel%
