@echo off
rem Windows 用起動スクリプト
rem 使い方: start.bat [--no-browser]
set "SCRIPT_DIR=%~dp0"

where py >nul 2>nul
if %ERRORLEVEL%==0 (
  call py -3 "%SCRIPT_DIR%server.py" %*
  exit /b
)

where python >nul 2>nul
if %ERRORLEVEL%==0 (
  call python "%SCRIPT_DIR%server.py" %*
  exit /b
)

echo エラー: Python が見つかりません。
echo Windowsの場合は以下を実行してインストールしてください:
echo   winget install Python.Python.3
exit /b 1
