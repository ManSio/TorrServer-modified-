#!/usr/bin/env bash
# Set or change the TorrServer HTTP Basic Auth credentials used by nginx.
# Usage: torrserver-auth.sh [user] [password]
# With no password a random 20-character one is generated and printed.
set -euo pipefail

USER_NAME="${1:-admin}"
if [ "${2:-}" != "" ]; then
  PASS="$2"
else
  PASS="$(openssl rand -base64 24 | tr -dc 'A-Za-z0-9' | head -c 20)"
fi

FILE=/etc/nginx/.htpasswd-torrserver
HASH="$(openssl passwd -apr1 "$PASS")"

umask 077
touch "$FILE"
if grep -q "^${USER_NAME}:" "$FILE"; then
  sed -i "s|^${USER_NAME}:.*|${USER_NAME}:${HASH}|" "$FILE"
else
  printf '%s:%s\n' "$USER_NAME" "$HASH" >> "$FILE"
fi

chown root:www-data "$FILE" 2>/dev/null || true
chmod 640 "$FILE"

nginx -t >/dev/null
systemctl reload nginx

printf 'user=%s\npassword=%s\n' "$USER_NAME" "$PASS"
