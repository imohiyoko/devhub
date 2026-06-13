param(
    [string]$InstallDir = $env:DEVHUB_INSTALL_DIR,
    [string]$BinDir = $env:DEVHUB_BIN_DIR,
    [string]$RepoUrl = $env:DEVHUB_REPO_URL
)

$ErrorActionPreference = "Stop"

if (-not $RepoUrl) {
    $RepoUrl = "https://github.com/imohiyoko/devhub.git"
}

function Test-Python {
    param(
        [string]$Command,
        [string[]]$Arguments = @()
    )

    try {
        & $Command @Arguments -c "import sys; raise SystemExit(0 if sys.version_info >= (3, 8) else 1)" *> $null
        return $LASTEXITCODE -eq 0
    } catch {
        return $false
    }
}

function Find-PythonCommand {
    if ((Get-Command py -ErrorAction SilentlyContinue) -and (Test-Python "py" @("-3"))) {
        return "py -3"
    }

    foreach ($command in @("python", "python3")) {
        $resolved = Get-Command $command -ErrorAction SilentlyContinue
        if ($resolved -and (Test-Python $command)) {
            return ('"{0}"' -f $resolved.Source)
        }
    }

    Write-Host "エラー: Python 3.8 以上が見つかりません。" -ForegroundColor Red
    Write-Host "Windowsの場合は以下を実行してインストールしてください:" -ForegroundColor Red
    Write-Host "  winget install Python.Python.3" -ForegroundColor Red
    throw "Python 3.8 or newer was not found."
}

function Set-DefaultEditorIfNeeded {
    param([string]$RootDir)

    if (Get-Command code -ErrorAction SilentlyContinue) {
        return
    }

    $candidates = @()
    if ($env:LOCALAPPDATA) {
        $candidates += (Join-Path $env:LOCALAPPDATA "Programs\Microsoft VS Code\bin\code.cmd")
    }
    if ($env:ProgramFiles) {
        $candidates += (Join-Path $env:ProgramFiles "Microsoft VS Code\bin\code.cmd")
    }
    $programFilesX86 = [Environment]::GetEnvironmentVariable("ProgramFiles(x86)")
    if ($programFilesX86) {
        $candidates += (Join-Path $programFilesX86 "Microsoft VS Code\bin\code.cmd")
    }

    $editorPath = $null
    foreach ($candidate in $candidates) {
        if (Test-Path $candidate) {
            $editorPath = $candidate
            break
        }
    }

    if (-not $editorPath) {
        Write-Warning "VS Code 'code' command was not found. Configure settings/server.json editor if workspace open does not work."
        return
    }

    $settingsDir = Join-Path $RootDir "settings"
    $serverJson = Join-Path $settingsDir "server.json"
    New-Item -ItemType Directory -Force -Path $settingsDir | Out-Null

    if (Test-Path $serverJson) {
        $settings = Get-Content -Raw -Path $serverJson | ConvertFrom-Json
        if ($settings.PSObject.Properties.Name -contains "editor") {
            return
        }
    } else {
        $settings = [pscustomobject]@{}
    }

    $settings | Add-Member -NotePropertyName "editor" -NotePropertyValue $editorPath -Force
    $settings | ConvertTo-Json -Depth 5 | Set-Content -Path $serverJson -Encoding UTF8
}

$scriptRoot = $PSScriptRoot
if (-not $scriptRoot -and $MyInvocation.MyCommand.Path) {
    $scriptRoot = Split-Path -Parent $MyInvocation.MyCommand.Path
}

$managedInstall = $true
if (-not $InstallDir -and $scriptRoot -and (Test-Path (Join-Path $scriptRoot "server.py"))) {
    $InstallDir = $scriptRoot
    $managedInstall = $false
} elseif (-not $InstallDir) {
    $InstallDir = Join-Path $env:LOCALAPPDATA "devhub"
}

if (-not $BinDir) {
    $BinDir = Join-Path $env:USERPROFILE "bin"
}

if ($managedInstall) {
    if (-not (Get-Command git -ErrorAction SilentlyContinue)) {
        Write-Host "エラー: git が見つかりません。" -ForegroundColor Red
        Write-Host "Windowsの場合は以下を実行してインストールしてください:" -ForegroundColor Red
        Write-Host "  winget install Git.Git" -ForegroundColor Red
        throw "git was not found."
    }

    if (Test-Path (Join-Path $InstallDir ".git")) {
        git -C $InstallDir remote set-url origin $RepoUrl *> $null
        git -C $InstallDir pull --ff-only
        if ($LASTEXITCODE -ne 0) {
            throw "git pull failed."
        }
    } elseif (Test-Path $InstallDir) {
        throw "$InstallDir already exists, but it is not a git repository. Set DEVHUB_INSTALL_DIR to use a different path."
    } else {
        $parent = Split-Path -Parent $InstallDir
        if ($parent) {
            New-Item -ItemType Directory -Force -Path $parent | Out-Null
        }
        git clone $RepoUrl $InstallDir
        if ($LASTEXITCODE -ne 0) {
            throw "git clone failed."
        }
    }
}

$serverPath = Join-Path $InstallDir "server.py"
if (-not (Test-Path $serverPath)) {
    throw "$serverPath was not found."
}

Set-DefaultEditorIfNeeded $InstallDir

$pythonCommand = Find-PythonCommand

New-Item -ItemType Directory -Force -Path $BinDir | Out-Null

$commandPath = Join-Path $BinDir "devhub.cmd"
$commandBody = @"
@echo off
if not "%1"=="--no-browser" (
    explorer "http://localhost:8765"
)
call $pythonCommand "$serverPath" %*
"@
Set-Content -Path $commandPath -Value $commandBody -Encoding ASCII

$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$pathItems = @()
if ($userPath) {
    $pathItems = $userPath -split ";"
}

$pathContainsBin = $false
foreach ($item in $pathItems) {
    if ($item.TrimEnd("\") -ieq $BinDir.TrimEnd("\")) {
        $pathContainsBin = $true
        break
    }
}

if (-not $pathContainsBin) {
    $newUserPath = if ($userPath) { "$BinDir;$userPath" } else { $BinDir }
    [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
    $env:Path = "$BinDir;$env:Path"
    Write-Host "PATH updated for future terminals: $BinDir"
}

Write-Host "Installed devhub"
Write-Host "  Command: $commandPath"
Write-Host "  Server : $serverPath"
Write-Host ""
Write-Host "Start:"
Write-Host "  devhub"
Write-Host ""
Write-Host "[Notice] 環境変数を反映させるため、現在のターミナルを一度閉じて再起動するか、新しいウィンドウを開いてから devhub コマンドを実行してください。"
