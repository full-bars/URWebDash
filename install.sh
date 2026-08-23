#!/usr/bin/env bash
# URWebDash installer - downloads the latest prebuilt binary from GitHub Releases,
# sets up the JWT if missing, and optionally installs the systemd services.
set -euo pipefail

REPO="full-bars/URWebDash"
RAW_URL="https://raw.githubusercontent.com/$REPO/master/install.sh"
BIN_NAME="stats_tracker"

# Under sudo, install for the invoking user, not root - the systemd units
# will run as that user and need paths in their home.
if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
  TARGET_HOME="$(getent passwd "$SUDO_USER" | cut -d: -f6)" || true
  [ -n "$TARGET_HOME" ] || die "could not resolve home directory for $SUDO_USER"
else
  TARGET_HOME="$HOME"
fi

INSTALL_DIR="${INSTALL_DIR:-$TARGET_HOME/.local/bin}"
JWT_PATH="${JWT_PATH:-$TARGET_HOME/.urnetwork/jwt}"
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
  echo "You can get an auth code at: https://ur.io"
  echo "(Sign in, generate the code, then enter it here - it gets exchanged for a"
  echo " session token that URWebDash stores locally.)"
  printf "Enter auth code (blank to skip): "
  read -r AUTH_CODE
  [ -z "${AUTH_CODE:-}" ] && { warn "Skipped JWT setup."; return 1; }

  case "$AUTH_CODE" in
    *[!A-Za-z0-9._=+-]*)
      die "auth code contains unexpected characters; copy it exactly as shown at https://ur.io" ;;
  esac

  log "Exchanging auth code for a session token..."
  # Body via stdin so the auth code never appears in process args (ps).
  RESP_FILE="$(mktemp)"
  trap 'rm -f "$RESP_FILE"' RETURN
  printf '{"auth_code":"%s"}' "$AUTH_CODE" | \
    curl -fsSL --max-time 30 -X POST "$AUTH_API/auth/code-login" \
      -H 'Content-Type: application/json' -H 'Accept: */*' \
      --data @- -o "$RESP_FILE" 2>/dev/null || {
    rm -f "$RESP_FILE"; die "request to $AUTH_API/auth/code-login failed"; }

  # Parse with the binary's real JSON parser (no sed guesswork).
  BY_JWT="$("$INSTALL_DIR/$BIN_NAME" extract-by-jwt < "$RESP_FILE")"
  rm -f "$RESP_FILE"

  if [ -z "$BY_JWT" ]; then
    # do not print the response body: it may echo account details
    die "auth code rejected by the API. Double-check the code and try again."
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

# --- Discord webhook setup (optional) -------------------------------------

setup_webhook() {
  # Same stdin rule as setup_jwt: never read from a pipe (curl | bash would
  # eat the script text as "input").
  if [ ! -t 0 ]; then
    warn "Non-interactive session: skipped webhook setup. Re-run in a terminal to configure alerts."
    return 1
  fi

  local WH="$(dirname "$JWT_PATH")/discord_webhook"
  if [ -s "$WH" ]; then
    log "Found existing webhook at $WH"
    return 0
  fi

  echo
  echo "Optional: set up a Discord webhook for traffic-spike and payout alerts."
  echo "In Discord: Server Settings -> Integrations -> Webhooks -> New Webhook, then copy the URL."
  printf "Paste webhook URL (blank to skip): "
  read -r WEBHOOK_URL
  [ -z "${WEBHOOK_URL:-}" ] && { warn "Skipped webhook setup."; return 1; }
  case "$WEBHOOK_URL" in
    https://discord.com/api/webhooks/*|https://discordapp.com/api/webhooks/*) ;;
    *) warn "That does not look like a Discord webhook URL - skipping."; return 1 ;;
  esac

  mkdir -p "$(dirname "$WH")"
  printf '%s' "$WEBHOOK_URL" > "$WH"
  chmod 600 "$WH"
  unset WEBHOOK_URL
  log "Webhook saved to $WH"

  # Spike threshold (only asked once a webhook exists)
  printf "Traffic-spike alert threshold (e.g. 500M, 0.5G, 2GB; blank for default 1GB): "
  read -r SPIKE_IN
  if [ -n "${SPIKE_IN:-}" ]; then
    printf '%s' "$SPIKE_IN" > "$(dirname "$WH")/spike_threshold"
    log "Spike threshold saved to $(dirname "$WH")/spike_threshold"
  fi
}

if setup_webhook; then
  WEBHOOK_OK=1
else
  WEBHOOK_OK=0
fi

# --- Optional systemd install --------------------------------------------

install_services() {
  command -v systemctl >/dev/null 2>&1 || return 1
  [ "$(id -u)" -eq 0 ] || return 1

  # Same account the binary/JWT paths were derived for at the top of script.
  if [ -n "${SUDO_USER:-}" ] && [ "$SUDO_USER" != "root" ]; then
    SERVICE_USER="$SUDO_USER"
    USER_HOME="$TARGET_HOME"
  else
    SERVICE_USER="root"
    USER_HOME="/root"
  fi

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
  if [ "$JWT_OK" = 1 ]; then
    systemctl enable --now urwebdash-run.service urwebdash-serve.service
    log "Services enabled: urwebdash-run (polling) + urwebdash-serve (dashboard on 127.0.0.1:3001)"
  else
    # Without a JWT both units would crash-loop. Install but stay off.
    systemctl enable urwebdash-run.service urwebdash-serve.service 2>/dev/null || true
    warn "No JWT yet, so services are installed but not started."
    warn "Add a token at $JWT_PATH then: sudo systemctl start urwebdash-run urwebdash-serve"
  fi
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
  echo "To run as a service without root, see deploy/*.service (user units + linger)."
  echo "For system services, download the installer and run it with sudo:"
  echo "  curl -fsSL $RAW_URL -o /tmp/urwebdash-install.sh && sudo bash /tmp/urwebdash-install.sh"
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
if [ "$WEBHOOK_OK" = 1 ]; then
  echo "  Test alerts: $INSTALL_DIR/$BIN_NAME testwebhook"
fi
