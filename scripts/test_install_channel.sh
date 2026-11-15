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

if [[ $fail -ne 0 ]]; then
  echo "FAILED" >&2
  exit 1
fi
echo "PASS"
