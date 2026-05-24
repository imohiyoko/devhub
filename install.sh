#!/bin/bash
# devhub をグローバルコマンドとしてインストールする
set -e

DEVHUB_DIR="$(cd "$(dirname "$0")" && pwd)"
BIN_DIR="$HOME/.local/bin"

mkdir -p "$BIN_DIR"

cat > "$BIN_DIR/devhub" << EOF
#!/bin/bash
python3 "$DEVHUB_DIR/server.py" "\$@"
EOF

chmod +x "$BIN_DIR/devhub"

echo "✓ インストール完了"
echo "  コマンド : $BIN_DIR/devhub"
echo "  実体     : $DEVHUB_DIR/server.py"
echo ""

# PATH チェック
if ! echo "$PATH" | tr ':' '\n' | grep -qx "$BIN_DIR"; then
  echo "⚠ ~/.local/bin が PATH に含まれていません。"
  echo "  以下を ~/.zshrc (または ~/.bashrc) に追加してください:"
  echo ""
  echo '    export PATH="$HOME/.local/bin:$PATH"'
  echo ""
  echo "  追加後: source ~/.zshrc"
fi
