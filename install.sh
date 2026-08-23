#!/usr/bin/env bash
# URWebDash installer: downloads the binary, then hands off to
# `urwebdash setup` for configuration (token, webhook, services).
set -euo pipefail

REPO="full-bars/URWebDash"
BIN_NAME="urwebdash"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64)  ASSET="urwebdash-linux-amd64.tar.gz" ;;
  Linux/aarch64|Linux/arm64) ASSET="urwebdash-linux-arm64.tar.gz" ;;
  Darwin/arm64)  ASSET="urwebdash-darwin-arm64.tar.gz" ;;
  Darwin/x86_64) ASSET="urwebdash-darwin-amd64.tar.gz" ;;
  *) die "unsupported platform: $(uname -s)/$(uname -m). See https://github.com/$REPO#building-from-source" ;;
esac

command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 || die "curl or wget is required"
mkdir -p "$INSTALL_DIR"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

URL="https://github.com/$REPO/releases/latest/download/$ASSET"
log "Downloading $URL"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$URL" -o "$TMP/$ASSET" || die "download failed"
else
  wget -qO "$TMP/$ASSET" "$URL" || die "download failed"
fi

tar -xzf "$TMP/$ASSET" -C "$TMP"
install -m 0755 "$TMP/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"
log "Installed to $INSTALL_DIR/$BIN_NAME"

# Configuration runs as your user; only the optional systemd step needs sudo.
exec "$INSTALL_DIR/$BIN_NAME" setup
