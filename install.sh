#!/usr/bin/env bash
# URWebDash installer — downloads the latest prebuilt binary from GitHub Releases.
set -euo pipefail

REPO="full-bars/URWebDash"
BIN_NAME="stats_tracker"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
die() { printf '\033[1;31merror:\033[0m %s\n' "$*" >&2; exit 1; }

case "$(uname -s)/$(uname -m)" in
  Linux/x86_64)  ASSET="stats_tracker-linux-amd64.tar.gz" ;;
  Linux/aarch64|Linux/arm64) ASSET="stats_tracker-linux-arm64.tar.gz" ;;
  Darwin/arm64)  ASSET="stats_tracker-darwin-arm64.tar.gz" ;;
  Darwin/x86_64) ASSET="stats_tracker-darwin-amd64.tar.gz" ;;
  *) die "unsupported platform: $(uname -s)/$(uname -m). Build from source instead: https://github.com/$REPO#building-from-source" ;;
esac

command -v curl >/dev/null 2>&1 || command -v wget >/dev/null 2>&1 || die "curl or wget is required"
mkdir -p "$INSTALL_DIR"
TMP="$(mktemp -d)"
trap 'rm -rf "$TMP"' EXIT

URL="https://github.com/$REPO/releases/latest/download/$ASSET"
log "Downloading $URL"
if command -v curl >/dev/null 2>&1; then
  curl -fsSL "$URL" -o "$TMP/$ASSET" || die "download failed (no release assets yet? build from source: https://github.com/$REPO#building-from-source)"
else
  wget -qO "$TMP/$ASSET" "$URL" || die "download failed"
fi

tar -xzf "$TMP/$ASSET" -C "$TMP"
install -m 0755 "$TMP/$BIN_NAME" "$INSTALL_DIR/$BIN_NAME"

log "Installed to $INSTALL_DIR/$BIN_NAME"
"$INSTALL_DIR/$BIN_NAME" --help >/dev/null 2>&1 || true

cat <<EOF

Next steps:
  1. Make sure your URnetwork JWT exists at ~/.urnetwork/jwt
     (copy from your provider machine: scp host:.urnetwork/jwt ~/.urnetwork/jwt)
  2. Start it:
       $INSTALL_DIR/$BIN_NAME run &
       $INSTALL_DIR/$BIN_NAME serve
  3. Open http://localhost:3001

For the systemd setup, see:
  https://github.com/$REPO#run-as-a-service-systemd
EOF
