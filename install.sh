#!/usr/bin/env bash
# URWebDash installer - downloads the latest prebuilt binary from GitHub Releases,
# sets up the JWT if missing, and optionally installs the systemd services.
set -euo pipefail

REPO="full-bars/URWebDash"
BIN_NAME="stats_tracker"
INSTALL_DIR="${INSTALL_DIR:-$HOME/.local/bin}"
JWT_PATH="${JWT_PATH:-$HOME/.urnetwork/jwt}"
AUTH_API="https://api.bringyour.com"

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*"; }
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

# --- JWT setup -----------------------------------------------------------
# Only step that can be interactive. Skipped entirely when a JWT already
# exists (e.g. the box also runs a URnetwork provider).

setup_jwt() {
  if [ -s "$JWT_PATH" ]; then
    log "Found existing JWT at $JWT_PATH"
    return 0
  fi

  # Non-interactive contexts (curl | bash leaves stdin attached to the pipe,
  # CI has no TTY): skip with instructions rather than hang.
  if [ ! -t 0 ]; then
    warn "No JWT found at $JWT_PATH and stdin is not interactive."
    echo "   Set it up later by either:"
    echo "     a) copying one from your provider machine:"
    echo "          mkdir -p ~/.urnetwork && scp host:.urnetwork/jwt $JWT_PATH"
    echo "     b) re-running this installer in a terminal to enter an auth code."
    return 1
  fi

  echo
  echo "No URnetwork JWT found at $JWT_PATH."
  echo "You can create an auth code at: https://ur.network/auth"
  echo "(It is a short code shown after you sign in - entering it here exchanges"
  echo " it for a session token that URWebDash stores locally.)"
  printf "Enter auth code (blank to skip): "
  read -r AUTH_CODE
  [ -z "${AUTH_CODE:-}" ] && { warn "Skipped JWT setup."; return 1; }

  log "Exchanging auth code for a session token..."
  local RESP BY_JWT ERRMSG
  RESP="$(curl -fsSL -X POST "$AUTH_API/auth/code-login" \
    -H 'Content-Type: application/json' \
    -H 'Accept: */*' \
    -d "{\"auth_code\":\"$AUTH_CODE\"}" 2>/dev/null)" || die "request to $AUTH_API/auth/code-login failed"

  BY_JWT="$(printf '%s' "$RESP" | sed -n 's/.*"by_jwt":"\([^"]*\)".*/\1/p')"
  ERRMSG="$(printf '%s' "$RESP" | sed -n 's/.*"message":"\([^"]*\)".*/\1/p')"

  if [ -z "$BY_JWT" ]; then
    die "auth code rejected${ERRMSG:+: $ERRMSG}"
  fi

  mkdir -p "$(dirname "$JWT_PATH")"
  umask 077
  printf '%s' "$BY_JWT" > "$JWT_PATH"
  chmod 600 "$JWT_PATH"
  unset AUTH_CODE BY_JWT RESP
  log "Session token saved to $JWT_PATH"
}

if setup_jwt; then
  JWT_OK=1
else
  JWT_OK=0
fi

# --- Optional systemd install --------------------------------------------

install_services() {
  command -v systemctl >/dev/null 2>&1 || return 1
  [ "$(id -u)" -eq 0 ] || return 1

  local SERVICE_USER="${SUDO_USER:-root}"
  local USER_HOME
  USER_HOME="$(getent passwd "$SERVICE_USER" | cut -d: -f6)"
  [ -n "$USER_HOME" ] || USER_HOME=/root

  log "Installing systemd services (user: $SERVICE_USER)"

  cat > /etc/systemd/system/urwebdash-run.service <<EOF
[Unit]
Description=URWebDash polling daemon
Documentation=https://github.com/$REPO
After=network-online.target
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
ExecStart=$USER_HOME/.local/bin/stats_tracker run
Restart=on-failure
RestartSec=30

NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

  cat > /etc/systemd/system/urwebdash-serve.service <<EOF
[Unit]
Description=URWebDash web dashboard
Documentation=https://github.com/$REPO
After=network-online.target urwebdash-run.service
Wants=network-online.target

[Service]
Type=simple
User=$SERVICE_USER
Environment=HOST=127.0.0.1
ExecStart=$USER_HOME/.local/bin/stats_tracker serve 3001
Restart=on-failure
RestartSec=30

NoNewPrivileges=true
PrivateTmp=true

[Install]
WantedBy=multi-user.target
EOF

  systemctl daemon-reload
  systemctl enable --now urwebdash-run.service urwebdash-serve.service
  log "Services enabled: urwebdash-run (polling) + urwebdash-serve (dashboard on 127.0.0.1:3001)"
}

echo
if [ "$(id -u)" -eq 0 ] && command -v systemctl >/dev/null 2>&1; then
  printf "Install and start the systemd services now? [Y/n] "
  if [ -t 0 ]; then
    read -r ANSWER
    ANSWER="${ANSWER:-y}"
  else
    ANSWER=y
  fi
  case "$ANSWER" in
    n|N|no|NO)
      warn "Skipping service install."
      SERVICES_INSTALLED=0
      ;;
    *)
      install_services && SERVICES_INSTALLED=1
      ;;
  esac
else
  SERVICES_INSTALLED=0
  echo "To run as a service, re-run this script as root (or see deploy/*.service)."
fi

# --- Summary -------------------------------------------------------------

echo
log "Done."
if [ "${SERVICES_INSTALLED:-0}" = 1 ]; then
  echo "  Dashboard: http://127.0.0.1:3001 (loopback only - put a reverse proxy or tunnel in front for remote access)"
  echo "  Logs:      journalctl -u urwebdash-serve -f"
elif [ "$JWT_OK" = 1 ]; then
  echo "  Try it:    $INSTALL_DIR/$BIN_NAME run &"
  echo "             $INSTALL_DIR/$BIN_NAME serve"
  echo "  Then open: http://127.0.0.1:3001"
fi
