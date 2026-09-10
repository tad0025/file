@echo off
chcp 65001 >nul
title TeleStream Local Server
cd /d "%~dp0"
echo ==================================================
echo TeleStream Local Server (http://localhost:8080)
echo ==================================================
.\bin\server.exe
pause
