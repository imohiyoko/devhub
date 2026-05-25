#!/bin/bash
# Linux / macOS 用起動スクリプト
# 使い方: ./start.sh [--no-browser]
set -e
SCRIPT_DIR="$(cd "$(dirname "$0")" && pwd)"
python3 "$SCRIPT_DIR/server.py" "$@"
