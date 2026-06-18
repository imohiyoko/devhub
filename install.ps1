param(
    [string]$Version    = $env:DEVHUB_VERSION,
    [string]$InstallDir = $env:DEVHUB_INSTALL_DIR,
    [string]$BinDir     = $env:DEVHUB_BIN_DIR,
    [string]$Repo       = $env:DEVHUB_REPO
)

# devhub installer (Windows): downloads a pinned, checksum-verified release
# binary. No git clone, no runtime — just a single static executable.
$ErrorActionPreference = "Stop"

if (-not $Repo)       { $Repo = "imohiyoko/devhub" }
if (-not $InstallDir) { $InstallDir = Join-Path $env:LOCALAPPDATA "devhub" }
if (-not $BinDir)     { $BinDir = Join-Path $env:USERPROFILE "bin" }

# --- detect arch (must match GoReleaser's asset names) ---
switch ($env:PROCESSOR_ARCHITECTURE) {
    "AMD64" { $arch = "amd64" }
    "ARM64" { $arch = "arm64" }
    default { throw "未対応の CPU アーキテクチャ: $($env:PROCESSOR_ARCHITECTURE)" }
}

# --- resolve version ---
if (-not $Version) {
    $rel = Invoke-RestMethod -Uri "https://api.github.com/repos/$Repo/releases/latest" -Headers @{ "User-Agent" = "devhub-installer" }
    $Version = $rel.tag_name
}
if (-not $Version) { throw "最新バージョンを取得できませんでした。DEVHUB_VERSION を指定してください。" }
$nv = $Version.TrimStart("v")

$asset = "devhub_${nv}_windows_${arch}.zip"
$base  = "https://github.com/$Repo/releases/download/$Version"

$tmp = Join-Path $env:TEMP ("devhub-" + [System.IO.Path]::GetRandomFileName())
New-Item -ItemType Directory -Force -Path $tmp | Out-Null
try {
    Write-Host "Downloading $asset ($Version) ..."
    Invoke-WebRequest -Uri "$base/$asset" -OutFile (Join-Path $tmp $asset)
    Invoke-WebRequest -Uri "$base/checksums.txt" -OutFile (Join-Path $tmp "checksums.txt")

    # --- verify SHA256 before extracting ---
    Write-Host "Verifying checksum ..."
    $expectedLine = Select-String -Path (Join-Path $tmp "checksums.txt") -Pattern ([regex]::Escape($asset)) | Select-Object -First 1
    if (-not $expectedLine) { throw "$asset のチェックサムが見つかりません。" }
    $expected = ($expectedLine.Line -split "\s+")[0].ToLower()
    $actual = (Get-FileHash -Algorithm SHA256 -Path (Join-Path $tmp $asset)).Hash.ToLower()
    if ($expected -ne $actual) { throw "チェックサム検証に失敗しました ($asset)。" }

    # --- install the single binary ---
    Expand-Archive -Path (Join-Path $tmp $asset) -DestinationPath $tmp -Force
    New-Item -ItemType Directory -Force -Path (Join-Path $InstallDir "bin") | Out-Null
    $exe = Join-Path $InstallDir "bin\devhub.exe"
    Copy-Item -Path (Join-Path $tmp "devhub.exe") -Destination $exe -Force
} finally {
    Remove-Item -Recurse -Force $tmp -ErrorAction SilentlyContinue
}

# --- shim on PATH that calls the installed binary ---
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
$shim = Join-Path $BinDir "devhub.cmd"
Set-Content -Path $shim -Encoding ASCII -Value @"
@echo off
"$exe" %*
"@

# --- ensure BinDir is on the user PATH (carried over from the previous installer) ---
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
$pathItems = @()
if ($userPath) { $pathItems = $userPath -split ";" }
$onPath = $false
foreach ($item in $pathItems) {
    if ($item.TrimEnd("\") -ieq $BinDir.TrimEnd("\")) { $onPath = $true; break }
}
if (-not $onPath) {
    $newUserPath = if ($userPath) { "$BinDir;$userPath" } else { $BinDir }
    [Environment]::SetEnvironmentVariable("Path", $newUserPath, "User")
    $env:Path = "$BinDir;$env:Path"
    Write-Host "PATH updated for future terminals: $BinDir"
}

Write-Host "Installed devhub $Version"
Write-Host "  Command : $shim"
Write-Host "  Binary  : $exe"
Write-Host "  Settings: $(Join-Path $InstallDir 'settings')"
Write-Host ""
Write-Host "Start: devhub"
Write-Host "[Notice] 新しいターミナルを開いてから devhub を実行してください。"
