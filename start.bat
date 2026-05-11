@echo off
rem Windows 用起動スクリプト
rem 使い方: start.bat [--no-browser]
set SCRIPT_DIR=%~dp0
python "%SCRIPT_DIR%server.py" %*
