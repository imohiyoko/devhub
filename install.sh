#!/usr/bin/env bash
# devhub installer: downloads a pinned, checksum-verified release binary.
# No git clone, no runtime — just a single static binary.
#
# Env overrides:
#   DEVHUB_VERSION      tag to install (default: latest release)
#   DEVHUB_INSTALL_DIR  where the binary lives  (default: ~/.devhub)
#   DEVHUB_BIN_DIR      where `devhub` is linked (default: ~/.local/bin)
set -euo pipefail

OWNER_REPO="${DEVHUB_REPO:-imohiyoko/devhub}"
DEVHUB_DIR="${DEVHUB_INSTALL_DIR:-$HOME/.devhub}"
BIN_DIR="${DEVHUB_BIN_DIR:-$HOME/.local/bin}"

require() { command -v "$1" >/dev/null 2>&1 || { echo "エラー: $1 が見つかりません。" >&2; exit 1; }; }
require curl
require tar

# --- detect OS / arch (must match GoReleaser's asset names) ---
os="$(uname -s)"; arch="$(uname -m)"
case "$os" in
  Darwin) os=darwin ;;
  Linux)  os=linux ;;
  *) echo "エラー: 未対応の OS: $os" >&2; exit 1 ;;
esac
case "$arch" in
  x86_64|amd64)  arch=amd64 ;;
  aarch64|arm64) arch=arm64 ;;
  *) echo "エラー: 未対応の CPU アーキテクチャ: $arch" >&2; exit 1 ;;
esac

# --- resolve version ---
ver="${DEVHUB_VERSION:-}"
if [ -z "$ver" ]; then
  ver="$(curl -fsSL "https://api.github.com/repos/$OWNER_REPO/releases/latest" \
        | sed -n 's/.*"tag_name": *"\([^"]*\)".*/\1/p' | head -1)"
fi
if [ -z "$ver" ]; then
  echo "エラー: 最新バージョンを取得できませんでした。DEVHUB_VERSION を指定してください。" >&2
  exit 1
fi
nv="${ver#v}" # strip leading v for the asset name

asset="devhub_${nv}_${os}_${arch}.tar.gz"
base="https://github.com/$OWNER_REPO/releases/download/$ver"

tmp="$(mktemp -d)"; trap 'rm -rf "$tmp"' EXIT
echo "Downloading $asset ($ver) ..."
curl -fsSL "$base/$asset" -o "$tmp/$asset"
curl -fsSL "$base/checksums.txt" -o "$tmp/checksums.txt"

# --- optional: verify checksums.txt signature (cosign keyless) ---
# Off by default: the SHA256 check below already pins the binary to this
# checksums.txt. Set DEVHUB_VERIFY_SIGNATURE=1 to additionally prove the
# checksums.txt was produced by this repo's release workflow — this is what
# defends against a *compromised release* that swaps the binary AND its
# checksums.txt together (SHA256 alone cannot detect that). Requires cosign.
#
# cosign v3+ verifies the sigstore bundle (checksums.txt.sigstore.json); v2
# lacks bundle-by-default and keeps verifying the legacy pair
# (checksums.txt.sig / .pem). Releases attach both formats during the
# migration window (issue #109).
if [ "${DEVHUB_VERIFY_SIGNATURE:-0}" = "1" ]; then
  require cosign
  echo "Verifying checksums.txt signature (cosign) ..."
  cosign_major="$(cosign version 2>&1 | sed -n 's/^GitVersion:[[:space:]]*v\{0,1\}\([0-9][0-9]*\).*/\1/p' | head -1 || true)"
  if [ "${cosign_major:-0}" -ge 3 ]; then
    curl -fsSL "$base/checksums.txt.sigstore.json" -o "$tmp/checksums.txt.sigstore.json" \
      || { echo "エラー: sigstore bundle (checksums.txt.sigstore.json) を取得できませんでした。bundle 添付前の古いリリースの可能性があります（cosign v2 なら .sig/.pem で検証できます）。" >&2; exit 1; }
    cosign verify-blob \
      --bundle "$tmp/checksums.txt.sigstore.json" \
      --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
      --certificate-identity-regexp "^https://github.com/${OWNER_REPO}/\.github/workflows/release\.yml@refs/" \
      "$tmp/checksums.txt" \
      || { echo "エラー: checksums.txt の署名検証に失敗しました。" >&2; exit 1; }
  else
    curl -fsSL "$base/checksums.txt.sig" -o "$tmp/checksums.txt.sig"
    curl -fsSL "$base/checksums.txt.pem" -o "$tmp/checksums.txt.pem"
    cosign verify-blob \
      --certificate "$tmp/checksums.txt.pem" \
      --signature "$tmp/checksums.txt.sig" \
      --certificate-oidc-issuer "https://token.actions.githubusercontent.com" \
      --certificate-identity-regexp "^https://github.com/${OWNER_REPO}/\.github/workflows/release\.yml@refs/" \
      "$tmp/checksums.txt" \
      || { echo "エラー: checksums.txt の署名検証に失敗しました。" >&2; exit 1; }
  fi
  echo "✓ 署名検証 OK (cosign keyless)"
fi

# --- verify SHA256 before extracting ---
echo "Verifying checksum ..."
(
  cd "$tmp"
  line="$(grep " ${asset}\$" checksums.txt || true)"
  [ -n "$line" ] || { echo "エラー: $asset のチェックサムが見つかりません。" >&2; exit 1; }
  if command -v sha256sum >/dev/null 2>&1; then
    printf '%s\n' "$line" | sha256sum -c -
  else
    printf '%s\n' "$line" | shasum -a 256 -c -
  fi
) || { echo "エラー: チェックサム検証に失敗しました。" >&2; exit 1; }

# --- install the single binary ---
tar -xzf "$tmp/$asset" -C "$tmp"
mkdir -p "$DEVHUB_DIR/bin"
install -m 0755 "$tmp/devhub" "$DEVHUB_DIR/bin/devhub"
# Clear any quarantine attribute (set only on browser downloads; harmless if absent).
if [ "$os" = "darwin" ]; then xattr -c "$DEVHUB_DIR/bin/devhub" 2>/dev/null || true; fi

# コマンドスロットは 1 つ（scripts/dev.sh install の dev shim と同じパス）。
# 最後に install した方が勝つ設計だが、置き換えは黙って行わず必ず告知する。
mkdir -p "$BIN_DIR"
if [ -f "$BIN_DIR/devhub" ] && [ ! -L "$BIN_DIR/devhub" ] && grep -q 'devhub dev shim' "$BIN_DIR/devhub" 2>/dev/null; then
  old_root=$(sed -n 's/^cd "\([^"]*\)".*/\1/p' "$BIN_DIR/devhub" | head -n1)
  echo "[Notice] 既存の dev shim（ソース実行: ${old_root:-?}）をリリース版へのリンクに置き換えます。"
  echo "         ソース実行に戻すには: scripts/dev.sh install"
fi
ln -sf "$DEVHUB_DIR/bin/devhub" "$BIN_DIR/devhub"

# --- ensure BIN_DIR is on PATH (carried over from the previous installer) ---
path_line_for_profile() {
  if [ "$BIN_DIR" = "$HOME/.local/bin" ]; then
    printf '%s\n' 'export PATH="$HOME/.local/bin:$PATH"'
  else
    printf 'export PATH="%s:$PATH"\n' "$BIN_DIR"
  fi
}

ensure_path_in_profile() {
  case ":$PATH:" in *":$BIN_DIR:"*) return 0 ;; esac
  local profile path_line
  case "${SHELL:-}" in
    */zsh)  profile="${ZDOTDIR:-$HOME}/.zshrc" ;;
    */bash) profile="$HOME/.bashrc" ;;
    *)      profile="$HOME/.profile" ;;
  esac
  path_line="$(path_line_for_profile)"
  mkdir -p "$(dirname "$profile")"
  if [ -f "$profile" ] && grep -F "$BIN_DIR" "$profile" >/dev/null 2>&1; then return 0; fi
  if [ "$BIN_DIR" = "$HOME/.local/bin" ] && [ -f "$profile" ] && grep -F '$HOME/.local/bin' "$profile" >/dev/null 2>&1; then return 0; fi
  {
    printf '\n# devhub\n'
    printf '%s\n' "$path_line"
  } >> "$profile"
  echo "PATH 追記: $profile"
}

ensure_path_in_profile

echo "✓ インストール完了 ($ver)"
echo "  コマンド : $BIN_DIR/devhub"
echo "  実体     : $DEVHUB_DIR/bin/devhub"
echo "  設定     : ${DEVHUB_HOME:-$HOME/.devhub}/settings"
echo ""
echo "起動: devhub start"
echo "（git / gh / mysql / lsof は各ツールの実行時に必要。インストールには不要です）"
