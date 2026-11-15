@echo off
cd /d "%~dp0"
if not exist config.json powershell.exe -NoProfile -ExecutionPolicy Bypass -File "%~dp0init-config.ps1"
ws-server.exe
pause
