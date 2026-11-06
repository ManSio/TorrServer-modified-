#!/usr/bin/env bash
# TorrServer HTTP Basic Auth for the nginx vhost.
#
# Interactive (asks for login and password):
#   sudo torrserver-auth
#
# Non-interactive:
#   sudo torrserver-auth <user> <password>
#
# Writes /etc/nginx/.htpasswd-torrserver and reloads nginx. Requires root.
set -euo pipefail

FILE=/etc/nginx/.htpasswd-torrserver

usage() {
  cat <<'EOF'
Usage:
  sudo torrserver-auth                 # ask for login and password
  sudo torrserver-auth <user> <pass>   # set directly

The password is hidden while typing and must be at least 8 characters.
EOF
}

if [ "${1:-}" = "-h" ] || [ "${1:-}" = "--help" ]; then
  usage
  exit 0
fi

if [ "$(id -u)" -ne 0 ]; then
  echo "Run as root: sudo $0" >&2
  exit 1
fi

USER_NAME="${1:-}"
PASS="${2:-}"

if [ -z "$USER_NAME" ]; then
  if [ ! -t 0 ]; then
    echo "No login given and stdin is not a terminal. Use: $0 <user> <password>" >&2
    exit 1
  fi
  read -r -p "Логин [admin]: " USER_NAME
  USER_NAME="${USER_NAME:-admin}"
fi

if ! [[ "$USER_NAME" =~ ^[A-Za-z0-9._-]+$ ]]; then
  echo "Логин может содержать только A-Z a-z 0-9 . _ -" >&2
  exit 1
fi

if [ -z "$PASS" ]; then
  if [ ! -t 0 ]; then
    echo "No password given and stdin is not a terminal. Use: $0 <user> <password>" >&2
    exit 1
  fi
  while :; do
    read -r -s -p "Пароль: " PASS; echo
    read -r -s -p "Повтори пароль: " PASS2; echo
    if [ "$PASS" != "$PASS2" ]; then
      echo "Пароли не совпадают, попробуй ещё раз."
      continue
    fi
    if [ "${#PASS}" -lt 8 ]; then
      echo "Пароль должен быть не короче 8 символов."
      continue
    fi
    break
  done
fi

if [ "${#PASS}" -lt 8 ]; then
  echo "Пароль должен быть не короче 8 символов." >&2
  exit 1
fi

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

echo "Готово: логин '${USER_NAME}' обновлён, nginx перезагружен."
