#!/usr/bin/env bash
# URWebDash uninstaller - removes the binary, systemd services, and (optionally) data.
# Safe to run as a regular user; sudo only needed if system services were installed.
set -euo pipefail

BIN_NAME="urwebdash"

log() { printf '\033[1;32m==>\033[0m %s\n' "$*"; }
warn() { printf '\033[1;33mwarning:\033[0m %s\n' "$*"; }

# --- systemd services (if present) ----------------------------------------
if [ "$(id -u)" -eq 0 ] && command -v systemctl >/dev/null 2>&1; then
  systemctl stop urwebdash-run.service urwebdash-serve.service 2>/dev/null || true
  systemctl disable urwebdash-run.service urwebdash-serve.service 2>/dev/null || true
  rm -f /etc/systemd/system/urwebdash-run.service /etc/systemd/system/urwebdash-serve.service
  systemctl daemon-reload
  log "Removed systemd services"
elif command -v systemctl >/dev/null 2>&1 && systemctl list-unit-files 2>/dev/null | grep -q urwebdash; then
  warn "System services exist but you are not root. Remove with:"
  echo "  sudo systemctl stop urwebdash-run urwebdash-serve"
  echo "  sudo systemctl disable urwebdash-run urwebdash-serve"
  echo "  sudo rm /etc/systemd/system/urwebdash-{run,serve}.service"
fi

# --- binary ----------------------------------------------------------------
if [ -f "$HOME/.local/bin/$BIN_NAME" ]; then
  rm -f "$HOME/.local/bin/$BIN_NAME"
  log "Removed $HOME/.local/bin/$BIN_NAME"
fi

# --- data (opt-in) ---------------------------------------------------------
DATA_DIR="$HOME/.urnetwork"
if [ -d "$DATA_DIR" ] || [ -f "$DATA_DIR" ]; then
  echo
  printf "Delete all URWebDash data (%s)? This cannot be undone. [y/N] " "$DATA_DIR"
  if [ -t 0 ]; then
    read -r ANSWER
    ANSWER="${ANSWER:-n}"
  else
    ANSWER="n"
  fi
  case "$ANSWER" in
    y|Y|yes|YES)
      # only delete files WE own; never touch a jwt that might belong to a provider install
      for f in wallet_stats.db wallet_stats.db-shm wallet_stats.db-wal \
               payout_notified.json discord_webhook spike_threshold; do
        [ -f "$DATA_DIR/$f" ] && rm -f "$DATA_DIR/$f"
      done
      rmdir "$DATA_DIR" 2>/dev/null || true   # remove dir only if empty (provider jwt left intact)
      log "Deleted URWebDash data"
      ;;
    *)
      log "Kept $DATA_DIR"
      ;;
  esac
fi

echo
log "Uninstall complete."
