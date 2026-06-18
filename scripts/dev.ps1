<#
.SYNOPSIS
  devhub をソースから起動する開発用ヘルパ（Windows / PowerShell 版）。

.DESCRIPTION
  配布バイナリを使わず（= 会社規定でバイナリ不可な環境向け）、このスクリプトが
  属する worktree のコードを `go run` でそのまま起動する。アセットは module
  ルートからの go:embed なので、worktree ごとに「いま編集中のソース」が反映される。

.PARAMETER Action
  run（既定） | build | stop | restart | status

.EXAMPLE
  scripts\dev.ps1 run
  $env:DEVHUB_PORT=9000; scripts\dev.ps1 run    # 別ポートで検証インスタンス
  $env:DEVHUB_PORT=9000; scripts\dev.ps1 stop   # そのポートの devhub を停止
  scripts\dev.ps1 run -- --no-browser           # devhub 本体へ引数を透過

  環境変数: DEVHUB_PORT（既定 8765） / DEVHUB_HOME（既定 %LOCALAPPDATA%\devhub）
#>
[CmdletBinding()]
param(
  [Parameter(Position = 0)]
  [ValidateSet('run', 'build', 'stop', 'restart', 'status')]
  [string]$Action = 'run',

  # run へ透過する追加引数（例: -- --no-browser）
  [Parameter(ValueFromRemainingArguments = $true)]
  [string[]]$Rest
)

$ErrorActionPreference = 'Stop'

# このスクリプトが属する worktree の repo ルートへ移動する。どこから呼んでも
# 「そのスクリプトのソース」を起動できるようにするため。
$RepoRoot = (Resolve-Path (Join-Path $PSScriptRoot '..')).Path
Set-Location $RepoRoot

$Port = if ($env:DEVHUB_PORT) { [int]$env:DEVHUB_PORT } else { 8765 }
$env:DEVHUB_PORT = "$Port"

function Require-Cmd($name) {
  if (-not (Get-Command $name -ErrorAction SilentlyContinue)) {
    Write-Error "エラー: $name が見つかりません。"
  }
}

# DEVHUB_PORT を LISTEN している PID 群を返す。
function Get-Listeners {
  Get-NetTCPConnection -LocalPort $Port -State Listen -ErrorAction SilentlyContinue |
    Select-Object -ExpandProperty OwningProcess -Unique
}

# 先頭の "--" 区切りを取り除く（run -- --no-browser のため）。
function PassThruArgs {
  if ($Rest -and $Rest.Count -gt 0 -and $Rest[0] -eq '--') {
    return $Rest[1..($Rest.Count - 1)]
  }
  return $Rest
}

switch ($Action) {
  'run' {
    Require-Cmd go
    $dataHome = if ($env:DEVHUB_HOME) { $env:DEVHUB_HOME } else { '%LOCALAPPDATA%\devhub' }
    Write-Host "devhub をソースから起動します (port=$Port, home=$dataHome)"
    & go run ./cmd/devhub @(PassThruArgs)
  }
  'build' {
    Require-Cmd go
    & go build -o devhub.exe ./cmd/devhub
    Write-Host "ビルド完了: $RepoRoot\devhub.exe"
  }
  'stop' {
    $pids = Get-Listeners
    if (-not $pids) {
      Write-Host "port $Port で LISTEN している devhub は見つかりません。"
      break
    }
    # ports ツールは安全のため devhub 自身を kill できないため、ここで停止する。
    Write-Host "port $Port のプロセスを停止します (pid: $($pids -join ', '))"
    foreach ($procId in $pids) { Stop-Process -Id $procId -Force }
  }
  'status' {
    $pids = Get-Listeners
    if (-not $pids) { Write-Host "port $Port: LISTEN なし" }
    else { Write-Host "port $Port: LISTEN 中 (pid: $($pids -join ', '))" }
  }
  'restart' {
    & $PSCommandPath stop
    Start-Sleep -Seconds 1 # ポート解放を待つ
    & go run ./cmd/devhub @(PassThruArgs)
  }
}
