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
  echo "[entrypoint] exchanging URNETWORK_AUTH_CODE for a session token..."
  RESP="$(wget -qO- --no-hsts --header='Content-Type: application/json' \
    --header='Accept: */*' \
    --post-data="{\"auth_code\":\"$URNETWORK_AUTH_CODE\"}" \
    https://api.bringyour.com/auth/code-login)" || {
    echo "[entrypoint] auth code exchange failed" >&2; exit 1; }
  BY_JWT="$(printf '%s' "$RESP" | sed -n 's/.*"by_jwt":"\([^"]*\)".*/\1/p')"
  [ -n "$BY_JWT" ] || { echo "[entrypoint] auth code rejected: $RESP" >&2; exit 1; }
  printf '%s' "$BY_JWT" > "$JWT"
  chmod 600 "$JWT" 2>/dev/null || true
  echo "[entrypoint] session token saved to $JWT"
else
  echo "[entrypoint] no JWT found."
  echo "  Mount your host jwt:      -v ~/.urnetwork/jwt:/host-jwt:ro"
  echo "  Or set URNETWORK_AUTH_CODE (get one at https://ur.io)."
  exit 1
fi

exec "$@"
