@echo off
chcp 65001 >nul
title TeleStream Video Uploader
cd /d "%~dp0"
.\bin\uploader.exe
