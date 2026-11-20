#!/usr/bin/env bash
# Offline regression test for the installer's release channel selection.
#
# Why: /releases/latest now points at Silo.2, whose binary accepts only
# "-c config.yaml", while both installers write the MatriX CLI flags. Installing
# it leaves the service in a restart loop (YouROK/TorrServer#977). The rule is
# that "latest" means the newest release of our own tag prefix, and that a
# missing match fails instead of falling back to /releases/latest.
#
# Usage: bash scripts/test_install_channel.sh [path-to-installer]
set -uo pipefail

script="${1:-installTorrServerLinux.sh}"
if [[ ! -f "$script" ]]; then
  echo "FAIL: $script not found (run from the repo root)" >&2
  exit 1
fi

VERSION_PREFIX="MatriX"
export VERSION_PREFIX

# Load only the functions under test; the installers run their main flow when
# executed, so they must not be sourced directly.
eval "$(sed -n '/^selectReleaseTag()/,/^}/p' "$script")"
if ! declare -f selectReleaseTag >/dev/null; then
  echo "FAIL: selectReleaseTag not found in $script" >&2
  exit 1
fi

fail=0
check() {
  local name="$1" json="$2" want="$3" got
  got=$(printf '%s' "$json" | selectReleaseTag)
  if [[ "$got" == "$want" ]]; then
    echo "ok   $name: '${got}'"
  else
    echo "FAIL $name: got '${got}', want '${want}'"
    fail=1
  fi
}

# Newest release is another engine: must be skipped.
check "skips the other engine" \
  '[{"tag_name":"Silo.2"},{"tag_name":"MatriX.150"},{"tag_name":"MatriX.149"}]' \
  "MatriX.150"

# GitHub serialises the field with a space; both forms must parse.
check "space after the colon" \
  '[{"tag_name": "MatriX.147.1"},{"tag_name":"MatriX.147"}]' \
  "MatriX.147.1"

# No release of our channel: empty result, so the caller aborts instead of
# falling back to /releases/latest.
check "no matching channel" \
  '[{"tag_name":"Silo.2"},{"tag_name":"Silo.1"}]' \
  ""

# An API error body must not be mistaken for a tag.
check "api error body" \
  '{"message":"API rate limit exceeded"}' \
  ""

# An empty channel must produce the message and a clean exit, not a silent
# `set -e` abort: the release list pipeline legitimately fails when no tag of
# our channel exists (that is the whole point of the filter).
run_empty_channel_case() {
  local script="$1" tmp rc out
  tmp=$(mktemp -d)
  cat >"$tmp/harness.sh" <<'HARNESS'
set -euo pipefail
VERSION_PREFIX=MatriX
REPO_URL=https://example.invalid
REPO_API_URL=https://example.invalid
specificVersion=""
colorize() { shift; printf '%s' "$*"; }
msg() { printf '%s\n' "$1"; }
curl() { printf '%s' '[]'; }
script="$1"
eval "$(sed -n '/^selectReleaseTag()/,/^}/p' "$script")"
eval "$(sed -n '/^getLatestRelease()/,/^}/p' "$script")"
eval "$(sed -n '/^getTargetVersion()/,/^}/p' "$script")"
getTargetVersion
HARNESS
  out=$(bash "$tmp/harness.sh" "$script" 2>&1)
  rc=$?
  rm -rf "$tmp"
  if [ "$rc" -ne 1 ]; then
    echo "FAIL empty channel: rc=$rc, want 1 (out='$out')"
    return 1
  fi
  case "$out" in
    *version_not_found* | *"not found"*)
      echo "ok   empty channel: message and rc=1"
      ;;
    *)
      echo "FAIL empty channel: exited without the message (out='$out')"
      return 1
      ;;
  esac
}

run_empty_channel_case "$script" || fail=1

if [[ $fail -ne 0 ]]; then
  echo "FAILED" >&2
  exit 1
fi
echo "PASS"
