#!/usr/bin/env bash
# TorrServer built-in HTTP Basic Auth (server/web/auth/auth.go).
#
# Interactive (asks for login and password):
#   sudo torrserver-auth
#
# Non-interactive:
#   sudo torrserver-auth <user> <password>
#
# Writes /var/lib/torrserver/accs.db and restarts TorrServer (auth is read at
# startup). Requires root.
set -euo pipefail

FILE=/var/lib/torrserver/accs.db

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

install -d -o torrserver -g torrserver /var/lib/torrserver
TMP="$(mktemp)"
if [ -s "$FILE" ]; then
  jq --arg u "$USER_NAME" --arg p "$PASS" '.[$u] = $p' "$FILE" > "$TMP"
else
  jq -n --arg u "$USER_NAME" --arg p "$PASS" '{($u): $p}' > "$TMP"
fi
install -m 600 -o torrserver -g torrserver "$TMP" "$FILE"
rm -f "$TMP"

systemctl restart torrserver
echo "Готово: логин '${USER_NAME}' обновлён, TorrServer перезапущен."
