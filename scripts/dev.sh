#!/usr/bin/env bash
# devhub をソースから起動する開発用ヘルパ。
#
# 配布バイナリを使わず（= 会社規定でバイナリ不可な環境向け）、このスクリプトが
# 属する worktree のコードを `go run` でそのまま起動する。アセットは
# module ルートからの go:embed なので、worktree ごとに「いま編集中のソース」が
# そのまま反映される（固定ビルドの devhub コマンドのように repo に固定されない）。
#
# 使い方:
#   scripts/dev.sh [run|build|stop|restart|status]
#   scripts/dev.sh run -- <devhub への追加引数...>
#
# 例:
#   scripts/dev.sh run                     # 既定ポート 8765 で起動
#   DEVHUB_PORT=9000 scripts/dev.sh run    # 別ポートで検証インスタンスを起動
#   DEVHUB_PORT=9000 scripts/dev.sh stop   # そのポートの devhub を停止
#   scripts/dev.sh run -- --no-browser     # devhub 本体へ引数を透過
#
# 環境変数:
#   DEVHUB_PORT  リッスンするポート（既定 8765）
#   DEVHUB_HOME  データ/設定の格納先（既定 ~/.devhub。検証を本番と完全分離したいとき指定）
set -euo pipefail

# このスクリプトが属する worktree の repo ルートへ移動する。どこから呼んでも
# 「そのスクリプトのソース」を起動できるようにするため。
REPO_ROOT=$(cd "$(dirname "$0")/.." && pwd)
cd "$REPO_ROOT"

PORT="${DEVHUB_PORT:-8765}"
export DEVHUB_PORT="$PORT"

require() { command -v "$1" >/dev/null 2>&1 || { echo "エラー: $1 が見つかりません。" >&2; exit 1; }; }

usage() {
  cat >&2 <<'EOF'
使い方: scripts/dev.sh [run|build|stop|restart|status]

  run       ソースから起動（既定）。`run -- <引数>` で devhub 本体へ透過
  build     ./devhub にバイナリをビルド
  install   ソースからビルドして PATH 上の `devhub` を更新（リリース不要）
  stop      DEVHUB_PORT を LISTEN している devhub を停止
  restart   stop してから run
  status    DEVHUB_PORT の LISTEN 状況を表示

環境変数: DEVHUB_PORT（既定 8765） / DEVHUB_HOME（既定 ~/.devhub）
          install 先: DEVHUB_INSTALL_DIR（既定 ~/.devhub） / DEVHUB_BIN_DIR（既定 ~/.local/bin）
EOF
}

# DEVHUB_PORT を LISTEN している PID を返す（なければ空）。
listeners() {
  require lsof
  lsof -nP -iTCP:"$PORT" -sTCP:LISTEN -t 2>/dev/null || true
}

cmd_run() {
  require go
  echo "devhub をソースから起動します (port=$PORT, home=${DEVHUB_HOME:-$HOME/.devhub})" >&2
  exec go run ./cmd/devhub "$@"
}

cmd_build() {
  require go
  go build -o devhub ./cmd/devhub
  echo "ビルド完了: $REPO_ROOT/devhub" >&2
}

# 配布リリース(install.sh)を待たず、いま手元のソースから `devhub` コマンドを更新する。
# 配置は install.sh と同じ（$DEVHUB_INSTALL_DIR/bin/devhub に実体、$DEVHUB_BIN_DIR に symlink）。
cmd_install() {
  require go
  local dir bindir dest sha tmp
  dir="${DEVHUB_INSTALL_DIR:-$HOME/.devhub}"
  bindir="${DEVHUB_BIN_DIR:-$HOME/.local/bin}"
  dest="$dir/bin/devhub"
  sha=$(git rev-parse --short HEAD 2>/dev/null || echo dev)
  mkdir -p "$dir/bin" "$bindir"
  tmp=$(mktemp "$dir/bin/.devhub.XXXXXX")
  go build -ldflags "-X main.version=dev-$sha" -o "$tmp" ./cmd/devhub
  [ "$(uname)" = "Darwin" ] && xattr -c "$tmp" 2>/dev/null || true
  mv -f "$tmp" "$dest"          # 原子的差し替え: 起動中インスタンスは旧 inode を掴んだまま
  ln -sf "$dest" "$bindir/devhub"
  echo "インストール完了: $dest (dev-$sha)" >&2
  echo "反映には起動中インスタンスの再起動が必要です（scripts/dev.sh stop → devhub）。" >&2
}

cmd_stop() {
  local pids
  pids=$(listeners)
  if [ -z "$pids" ]; then
    echo "port $PORT で LISTEN している devhub は見つかりません。" >&2
    return 0
  fi
  echo "port $PORT のプロセスを停止します (pid: $pids)" >&2
  # ports ツールは安全のため devhub 自身を kill できないため、ここで停止する。
  # shellcheck disable=SC2086
  kill $pids
}

cmd_status() {
  local pids
  pids=$(listeners)
  if [ -z "$pids" ]; then
    echo "port $PORT: LISTEN なし"
  else
    echo "port $PORT: LISTEN 中 (pid: $pids)"
  fi
}

cmd_restart() {
  cmd_stop
  sleep 1 # ポート解放を待つ
  cmd_run "$@"
}

action="${1:-run}"
if [ "$#" -gt 0 ]; then shift; fi
if [ "${1:-}" = "--" ]; then shift; fi

case "$action" in
  run)            cmd_run "$@" ;;
  build)          cmd_build ;;
  install)        cmd_install ;;
  stop)           cmd_stop ;;
  restart)        cmd_restart "$@" ;;
  status)         cmd_status ;;
  -h|--help|help) usage ;;
  *)
    echo "不明なサブコマンド: $action" >&2
    usage
    exit 1
    ;;
esac
