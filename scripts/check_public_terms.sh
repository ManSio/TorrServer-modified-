#!/usr/bin/env bash
# check_public_terms.sh - guard before publishing: infrastructure addresses must
# not leave the machine.
#
# In a public repository a commit is a permanent publication, and an address is
# a target: a LAN IP reveals the network layout, a VPS or DDNS name ties the
# repository to its owner. Documentation ranges (RFC 5737) are the way to show
# an address in an example.
#
# Usage:
#   scripts/check_public_terms.sh --cached          # what is about to be committed
#   scripts/check_public_terms.sh <rev>             # a branch tip
#   scripts/check_public_terms.sh <A..B>            # every commit in a push
#   scripts/check_public_terms.sh --file <path>     # one file (docs, drafts)
#
# Exit codes: 0 = clean, 1 = real addresses found, 2 = bad usage / scan failed.
#
# Worthy of note: the check is a grep, not a classifier. It cannot tell your
# VPS from a stranger's IP in a quoted log, so a finding is "stop and decide",
# not "the sky is falling". Whitelisted: loopback, unspecified, broadcast,
# RFC 5737 documentation ranges, and public resolvers that appear in examples.
set -uo pipefail

# IPv4, IPv6 and dynamic-DNS names.
ipv4='((25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])\.){3}(25[0-5]|2[0-4][0-9]|1[0-9]{2}|[1-9]?[0-9])'
# Four or more groups, or a link-local/loopback form: a clock time (hh:mm:ss)
# has two colons and must not be reported.
ipv6='(([0-9a-fA-F]{1,4}:){3,7}[0-9a-fA-F]{1,4}|fe80::[0-9a-fA-F:]*|::1)'
ddns='[a-z0-9-]+\.(freemyip\.com|no-ip\.(org|biz|info)|dyndns\.[a-z]+|ddns\.[a-z]+|duckdns\.org)'
pattern="($ipv4|$ipv6|$ddns)"

# Allowed IPv4 literals: loopback, unspecified, broadcast, RFC 5737 examples,
# public resolvers used in examples.
allowed_ipv4='^(0\.0\.0\.0|127\.[0-9]+\.[0-9]+\.[0-9]+|255\.255\.255\.255|192\.0\.2\.[0-9]+|198\.51\.100\.[0-9]+|203\.0\.113\.[0-9]+|8\.8\.8\.8|8\.8\.4\.4|1\.1\.1\.1|1\.0\.0\.1|9\.9\.9\.9|208\.67\.222\.222|208\.67\.220\.220|149\.112\.112\.112)$'
# Allowed hostnames / IPv6 that are never infrastructure: loopback and the
# examples reserved by RFC 3849 (2001:db8::/32).
allowed_other='^(::1|::|2001:db8([:0-9a-fA-F]*))$'

# Paths that are never published in this project: the pre-push hook refuses to
# push them, so their addresses are local by design and not reported here.
personal='^(AGENT_DIARY\.md|EXPERIMENTS_LOG\.md|scripts/torrserver-auth\.sh)$|^(docs|experiments)/'

# The guard's own files carry address patterns by definition.
exempt='^(\.public-allow|scripts/check_public_terms\.sh)$'

# Optional allowlist: one regex per line, matched against "path:line:text".
# Used for legitimate code that contains address literals (blocklists, tests).
# Comments and blank lines are stripped: an empty pattern would match every line.
allowfile="${PUBLIC_TERMS_ALLOW:-.public-allow}"
allowpatterns=""
if [ -f "$allowfile" ]; then
    allowpatterns=$(mktemp)
    grep -vE '^[[:space:]]*(#|$)' "$allowfile" >"$allowpatterns" || true
fi
cleanup() { [ -n "$allowpatterns" ] && rm -f "$allowpatterns"; }
trap cleanup EXIT

scan() {
    # stdin: "file:line:text" lines from grep
    local hits=0 raw file line_no text value key
    while IFS= read -r raw; do
        [ -z "$raw" ] && continue
        file=${raw%%:*}
        text=${raw#*:}
        line_no=${text%%:*}
        text=${text#*:}
        key="$file:$line_no:$text"
        if printf '%s' "$file" | grep -qE "$personal"; then continue; fi
        if printf '%s' "$file" | grep -qE "$exempt"; then continue; fi
        value=$(printf '%s\n' "$text" | grep -oE "$pattern" | tail -n1)
        [ -z "$value" ] && continue
        if printf '%s' "$value" | grep -qE "$allowed_ipv4"; then continue; fi
        if printf '%s' "$value" | grep -qE "$allowed_other"; then continue; fi
        if [ -n "$allowpatterns" ] && [ -s "$allowpatterns" ] &&
           printf '%s' "$key" | grep -qE -f "$allowpatterns"; then continue; fi
        printf '%s:%s: %s\n' "$file" "$line_no" "$value"
        hits=$((hits + 1))
    done
    return "$hits"
}

mode="${1:-}"

fail_scan() {
    echo "check_public_terms: scan failed ($1), refusing to call it clean" >&2
    exit 2
}

case "$mode" in
    --cached)
        out=$(git grep --cached -nE -I "$pattern" -- .)
        rc=$?
        [ "$rc" -gt 1 ] && fail_scan "git grep --cached rc=$rc"
        [ "$rc" -eq 1 ] && out=""
        ;;
    --file)
        path="${2:-}"
        if [ -z "$path" ] || [ ! -f "$path" ]; then
            echo "usage: $0 --file <path>" >&2
            exit 2
        fi
        out=$(grep -nE -I "$pattern" -- "$path")
        rc=$?
        [ "$rc" -gt 1 ] && fail_scan "grep rc=$rc"
        [ "$rc" -eq 1 ] && out=""
        if [ -n "$out" ]; then
            # Forward slashes also keep awk from eating Windows backslashes.
            pathp=$(printf '%s' "$path" | tr '\\' '/')
            out=$(printf '%s\n' "$out" | awk -v p="$pathp" '{ print p ":" $0 }')
        fi
        ;;
    "" | -h | --help)
        sed -n '2,26p' "$0" | sed 's/^# \{0,1\}//'
        exit 2
        ;;
    *)
        if printf '%s' "$mode" | grep -q '\.\.'; then
            from=${mode%%..*}
            to=${mode#*..}
            to=${to#.}
            for side in "$from" "$to"; do
                if ! git rev-parse --quiet --verify "$side^{commit}" >/dev/null 2>&1; then
                    echo "check_public_terms: unknown revision in range: $side" >&2
                    exit 2
                fi
            done
            out=""
            for commit in $(git rev-list "$mode"); do
                part=$(git grep -nE -I "$pattern" "$commit" -- . | sed "s|^$commit:||")
                rc=$?
                [ "$rc" -gt 1 ] && fail_scan "git grep $commit rc=$rc"
                [ -n "$part" ] && out="${out}${part}"$'\n'
            done
            out=$(printf '%s' "$out" | sort -u)
        else
            if ! git rev-parse --quiet --verify "$mode^{commit}" >/dev/null 2>&1; then
                echo "check_public_terms: unknown revision: $mode" >&2
                exit 2
            fi
            out=$(git grep -nE -I "$pattern" "$mode" -- . | sed "s|^$mode:||")
            rc=$?
            [ "$rc" -gt 1 ] && fail_scan "git grep $mode rc=$rc"
            [ "$rc" -eq 1 ] && out=""
        fi
        ;;
esac

if [ -z "$out" ]; then
    echo "check_public_terms: clean"
    exit 0
fi

found=$(printf '%s\n' "$out" | scan)
rc=$?
if [ "$rc" -eq 0 ]; then
    echo "check_public_terms: clean (only documentation/loopback addresses)"
    exit 0
fi

echo "" >&2
echo "check_public_terms: $rc real address(es) about to be published:" >&2
printf '%s\n' "$found" >&2
cat >&2 <<'EOF'

Replace each with "local server" (LAN host) or "pub server" (VPS/DDNS), or use a
RFC 5737 example (192.0.2.0/24, 198.51.100.0/24, 203.0.113.0/24) when the form
of the address is what matters. Real addresses belong in unpublished files.
EOF
exit 1
