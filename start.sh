#!/bin/bash
# Linux / macOS 用起動スクリプト
# 使い方: ./start.sh [--no-browser]
set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"

if ! command -v python3 >/dev/null 2>&1; then
  echo "エラー: Python 3 が見つかりません。" >&2
  if [ "$(uname)" = "Darwin" ]; then
    echo "macOSの場合は以下を実行してインストールしてください:" >&2
    echo "  brew install python3" >&2
  else
    echo "Linuxの場合は以下を実行してインストールしてください:" >&2
    echo "  sudo apt install python3 (Ubuntu/Debian)" >&2
    echo "  sudo dnf install python3 (Fedora/RHEL)" >&2
  fi
  exit 1
fi

python3 "$SCRIPT_DIR/server.py" "$@"
