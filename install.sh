#!/usr/bin/env bash
# devhub をグローバルコマンドとしてインストールする
set -euo pipefail

REPO_URL="${DEVHUB_REPO_URL:-https://github.com/imohiyoko/devhub.git}"
BIN_DIR="${DEVHUB_BIN_DIR:-$HOME/.local/bin}"
SCRIPT_PATH="${BASH_SOURCE[0]:-$0}"
SCRIPT_DIR=""
if [ -f "$SCRIPT_PATH" ]; then
  SCRIPT_DIR="$(cd "$(dirname "$SCRIPT_PATH")" && pwd)"
fi

if [ -z "${DEVHUB_INSTALL_DIR:-}" ] && [ -n "$SCRIPT_DIR" ] && [ -f "$SCRIPT_DIR/server.py" ]; then
  DEVHUB_DIR="$SCRIPT_DIR"
  MANAGED_INSTALL=0
else
  DEVHUB_DIR="${DEVHUB_INSTALL_DIR:-$HOME/.devhub}"
  MANAGED_INSTALL=1
fi

require_command() {
  if ! command -v "$1" >/dev/null 2>&1; then
    echo "エラー: $1 が見つかりません。" >&2
    exit 1
  fi
}

find_python() {
  local cmd
  for cmd in python3 python; do
    if command -v "$cmd" >/dev/null 2>&1 && "$cmd" - <<'PY' >/dev/null 2>&1
import sys
raise SystemExit(0 if sys.version_info >= (3, 8) else 1)
PY
    then
      command -v "$cmd"
      return 0
    fi
  done

  echo "エラー: Python 3.8 以上が見つかりません。" >&2
  exit 1
}

if [ "$MANAGED_INSTALL" -eq 1 ]; then
  require_command git

  if [ -d "$DEVHUB_DIR/.git" ]; then
    git -C "$DEVHUB_DIR" remote set-url origin "$REPO_URL" >/dev/null 2>&1 || true
    git -C "$DEVHUB_DIR" pull --ff-only
  elif [ -e "$DEVHUB_DIR" ]; then
    echo "エラー: $DEVHUB_DIR は既に存在しますが、git リポジトリではありません。" >&2
    echo "       別の場所に入れる場合は DEVHUB_INSTALL_DIR を指定してください。" >&2
    exit 1
  else
    mkdir -p "$(dirname "$DEVHUB_DIR")"
    git clone "$REPO_URL" "$DEVHUB_DIR"
  fi
fi

if [ ! -f "$DEVHUB_DIR/server.py" ]; then
  echo "エラー: $DEVHUB_DIR/server.py が見つかりません。" >&2
  exit 1
fi

chmod +x "$DEVHUB_DIR/start.sh" "$DEVHUB_DIR/devhub.app/Contents/MacOS/devhub" >/dev/null 2>&1 || true

PYTHON_BIN="$(find_python)"

mkdir -p "$BIN_DIR"

cat > "$BIN_DIR/devhub" << EOF
#!/usr/bin/env bash
exec "$PYTHON_BIN" "$DEVHUB_DIR/server.py" "\$@"
EOF

chmod +x "$BIN_DIR/devhub"

path_line_for_profile() {
  if [ "$BIN_DIR" = "$HOME/.local/bin" ]; then
    printf '%s\n' 'export PATH="$HOME/.local/bin:$PATH"'
  else
    printf 'export PATH="%s:$PATH"\n' "$BIN_DIR"
  fi
}

ensure_path_in_profile() {
  case ":$PATH:" in
    *":$BIN_DIR:"*) return 0 ;;
  esac

  local profile path_line
  case "${SHELL:-}" in
    */zsh)  profile="${ZDOTDIR:-$HOME}/.zshrc" ;;
    */bash) profile="$HOME/.bashrc" ;;
    *)      profile="$HOME/.profile" ;;
  esac

  path_line="$(path_line_for_profile)"
  mkdir -p "$(dirname "$profile")"

  if [ -f "$profile" ]; then
    if grep -F "$BIN_DIR" "$profile" >/dev/null 2>&1; then
      return 0
    fi
    if [ "$BIN_DIR" = "$HOME/.local/bin" ] && grep -F '$HOME/.local/bin' "$profile" >/dev/null 2>&1; then
      return 0
    fi
  fi

  if [ ! -f "$profile" ] || ! grep -F "$BIN_DIR" "$profile" >/dev/null 2>&1; then
    {
      printf '\n'
      printf '# devhub\n'
      printf '%s\n' "$path_line"
    } >> "$profile"
    echo "PATH 追記: $profile"
  fi
}

ensure_path_in_profile

echo "✓ インストール完了"
echo "  コマンド : $BIN_DIR/devhub"
echo "  実体     : $DEVHUB_DIR/server.py"
echo ""
echo "起動:"
echo "  devhub"
