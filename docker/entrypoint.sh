#!/bin/sh
# Container entrypoint: make sure a JWT exists at $JWT_PATH before handing
# off to the real command. Order of preference:
#   1. JWT already in the data volume (survives restarts)
#   2. Host jwt bind-mounted at /host-jwt (copied in once)
#   3. URNETWORK_AUTH_CODE env var, exchanged for a session token once
set -eu

# If running as root (default), take ownership of a possibly root-created bind
# mount and drop privileges. PUID/PGID let users match their host account.
if [ "$(id -u)" = "0" ]; then
  chown -R "${PUID:-1000}:${PGID:-1000}" /data 2>/dev/null || true
  exec su-exec "${PUID:-1000}:${PGID:-1000}" "$0" "$@"
fi

JWT="${JWT_PATH:-/data/jwt}"

if [ -s "$JWT" ]; then
  echo "[entrypoint] using existing JWT at $JWT"
elif [ -s /host-jwt ]; then
  cp /host-jwt "$JWT"
  chmod 600 "$JWT" 2>/dev/null || true
  echo "[entrypoint] copied host JWT from /host-jwt"
elif [ -n "${URNETWORK_AUTH_CODE:-}" ]; then
  # Sanity-check the code before use; also keeps malformed values out of the request.
  case "$URNETWORK_AUTH_CODE" in
    ''|*[!A-Za-z0-9._=+-]*)
      echo "[entrypoint] URNETWORK_AUTH_CODE contains unexpected characters" >&2
      exit 1 ;;
  esac

  echo "[entrypoint] exchanging URNETWORK_AUTH_CODE for a session token..."
  # Body goes via a file so the auth code never appears in process args (ps).
  BODY="$(mktemp)"
  trap 'rm -f "$BODY"' EXIT
  printf '{"auth_code":"%s"}' "$URNETWORK_AUTH_CODE" > "$BODY"

  RESP_FILE="$(mktemp)"
  wget -qO "$RESP_FILE" --no-hsts --timeout=20 --tries=2 \
    --header='Content-Type: application/json' --header='Accept: */*' \
    --post-file="$BODY" \
    https://api.bringyour.com/auth/code-login </dev/null || {
    rm -f "$BODY" "$RESP_FILE"
    echo "[entrypoint] auth code exchange failed" >&2; exit 1; }

  BY_JWT="$(stats_tracker extract-by-jwt < "$RESP_FILE")"
  rm -f "$BODY" "$RESP_FILE"

  # A JWT has three dot-separated segments; anything else is an error body.
  case "$BY_JWT" in
    *.*.*)
      printf '%s' "$BY_JWT" > "$JWT"
      chmod 600 "$JWT" 2>/dev/null || true
      unset URNETWORK_AUTH_CODE
      echo "[entrypoint] session token saved to $JWT" ;;
    *)
      # never print BY_JWT/RESP: may contain account details
      echo "[entrypoint] auth code rejected by the API" >&2
      exit 1 ;;
  esac
else
  echo "[entrypoint] no JWT found."
  echo "  Mount your host jwt:      -v ~/.urnetwork/jwt:/host-jwt:ro"
  echo "  Or set URNETWORK_AUTH_CODE (get one at https://ur.io)."
  exit 1
fi

exec "$@"
