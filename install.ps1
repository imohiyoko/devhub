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

    # --- optional: verify checksums.txt signature (cosign keyless) ---
    # Off by default: the SHA256 check below already pins the binary to this
    # checksums.txt. Set DEVHUB_VERIFY_SIGNATURE=1 to additionally prove the
    # checksums.txt was produced by this repo's release workflow (defends against
    # a compromised release that swaps the binary AND checksums.txt together).
    #
    # cosign v3+ verifies the sigstore bundle (checksums.txt.sigstore.json); v2
    # lacks bundle-by-default and keeps verifying the legacy pair
    # (checksums.txt.sig / .pem). Releases attach both formats during the
    # migration window (issue #109).
    if ($env:DEVHUB_VERIFY_SIGNATURE -eq "1") {
        if (-not (Get-Command cosign -ErrorAction SilentlyContinue)) {
            throw "cosign が見つかりません（DEVHUB_VERIFY_SIGNATURE=1 には cosign が必要です）。"
        }
        Write-Host "Verifying checksums.txt signature (cosign) ..."
        # stderr は敢えてリダイレクトしない（PowerShell 5.1 は EAP=Stop 下で
        # 2>&1 リダイレクトされた stderr 出力を NativeCommandError にしてしまう）。
        # バージョンを判定できないときは 0 のまま → 従来形式で検証する。
        $cosignMajor = 0
        try {
            $verText = (& cosign version | Out-String)
            if ($verText -match "GitVersion:\s*v?(\d+)") { $cosignMajor = [int]$Matches[1] }
        } catch {}
        if ($cosignMajor -ge 3) {
            $bundle = Join-Path $tmp "checksums.txt.sigstore.json"
            try {
                Invoke-WebRequest -Uri "$base/checksums.txt.sigstore.json" -OutFile $bundle
            } catch {
                throw "sigstore bundle (checksums.txt.sigstore.json) を取得できませんでした。bundle 添付前の古いリリースの可能性があります（cosign v2 なら .sig/.pem で検証できます）。"
            }
            & cosign verify-blob `
                --bundle $bundle `
                --certificate-oidc-issuer "https://token.actions.githubusercontent.com" `
                --certificate-identity-regexp "^https://github.com/$Repo/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)`$" `
                (Join-Path $tmp "checksums.txt")
            if ($LASTEXITCODE -ne 0) { throw "checksums.txt の署名検証に失敗しました。" }
        } else {
            Invoke-WebRequest -Uri "$base/checksums.txt.sig" -OutFile (Join-Path $tmp "checksums.txt.sig")
            Invoke-WebRequest -Uri "$base/checksums.txt.pem" -OutFile (Join-Path $tmp "checksums.txt.pem")
            & cosign verify-blob `
                --certificate (Join-Path $tmp "checksums.txt.pem") `
                --signature (Join-Path $tmp "checksums.txt.sig") `
                --certificate-oidc-issuer "https://token.actions.githubusercontent.com" `
                --certificate-identity-regexp "^https://github.com/$Repo/\.github/workflows/release\.yml@refs/(heads/main|tags/v[0-9]+\.[0-9]+\.[0-9]+(-[0-9A-Za-z.-]+)?)`$" `
                (Join-Path $tmp "checksums.txt")
            if ($LASTEXITCODE -ne 0) { throw "checksums.txt の署名検証に失敗しました。" }
        }
        Write-Host "✓ 署名検証 OK (cosign keyless)"
    }

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
# The command slot is deliberately single: this shim and scripts\dev.ps1
# install's dev shim share the same path, and the last installer to run owns
# it. Replacing the other kind must be announced, never silent (devhub doctor
# shows the current owner).
New-Item -ItemType Directory -Force -Path $BinDir | Out-Null
$shim = Join-Path $BinDir "devhub.cmd"
if (Test-Path $shim) {
    $old = Get-Content $shim -Raw -ErrorAction SilentlyContinue
    if ($old -match 'devhub dev shim') {
        $oldRoot = if ($old -match '(?m)^pushd (.+?)\r?$') { $Matches[1] } else { '?' }
        Write-Host "[Notice] 既存の dev shim（ソース実行: $oldRoot）をリリース版 shim に置き換えます。"
        Write-Host "         ソース実行に戻すには: scripts\dev.ps1 install"
    }
}
Set-Content -Path $shim -Encoding ASCII -Value @"
@echo off
rem devhub release shim - runs the pinned binary installed by install.ps1
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
Write-Host "Start: devhub start"
Write-Host "[Notice] 新しいターミナルを開いてから devhub を実行してください。"
