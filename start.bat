@echo off
rem Windows 用起動スクリプト
rem 使い方: start.bat [--no-browser]
set "SCRIPT_DIR=%~dp0"

where py >nul 2>nul
if %ERRORLEVEL%==0 (
  py -3 "%SCRIPT_DIR%server.py" %*
) else (
  python "%SCRIPT_DIR%server.py" %*
)
