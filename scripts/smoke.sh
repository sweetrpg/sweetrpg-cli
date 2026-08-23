#!/usr/bin/env bash
# End-to-end smoke test for catalog-cli against a local stack (openspec
# add-sweetrpg-catalog-cli task 6.2): login -> add -> link -> edit --cover ->
# view --json -> delete.
#
# Prerequisites:
#   - local catalog-api and assets-web running, with URLs exported:
#       export SWEETRPG_CATALOG_API_URL=http://localhost:PORT/api/0/catalog
#       export SWEETRPG_ASSETS_WEB_URL=http://localhost:ASSETS_PORT
#   - jq on PATH
#   - a device-flow login session; pass -l to run `auth login` first (it needs
#     a browser), otherwise the script assumes you are already logged in.
#
# Usage: scripts/smoke.sh [-l]
set -euo pipefail

LOGIN_FIRST=0
[ "${1:-}" = "-l" ] && LOGIN_FIRST=1

cd "$(dirname "$0")/.."
CLI=(go run ./cmd/sweetrpg-catalog)

fail() {
    printf 'smoke: FAIL: %s\n' "$1" >&2
    exit 1
}
step() { printf 'smoke: %s\n' "$1"; }

command -v jq >/dev/null || fail "jq is required"
[ -n "${SWEETRPG_CATALOG_API_URL:-}" ] || fail "SWEETRPG_CATALOG_API_URL is not set"
[ -n "${SWEETRPG_ASSETS_WEB_URL:-}" ] || fail "SWEETRPG_ASSETS_WEB_URL is not set"

if [ "$LOGIN_FIRST" -eq 1 ]; then
    step "login (device flow; approve in your browser)"
    "${CLI[@]}" auth login || fail "auth login failed"
fi

TAG="smoke-$$"
PUB_ID=""
VOL_ID=""
cleanup() {
    [ -n "$VOL_ID" ] && "${CLI[@]}" delete volume "$VOL_ID" --force --yes >/dev/null 2>&1 || true
    [ -n "$PUB_ID" ] && "${CLI[@]}" delete publisher "$PUB_ID" --force --yes >/dev/null 2>&1 || true
}
trap cleanup EXIT

step "add publisher"
OUT=$("${CLI[@]}" add publisher "$TAG-publisher") || fail "add publisher failed"
PUB_ID=${OUT##* }
case "$PUB_ID" in ????????????????????????) ;; *) fail "unexpected add output: $OUT" ;; esac

step "add volume"
OUT=$("${CLI[@]}" add volume "$TAG-volume") || fail "add volume failed"
VOL_ID=${OUT##* }
case "$VOL_ID" in ????????????????????????) ;; *) fail "unexpected add output: $OUT" ;; esac

step "link volume to publisher"
"${CLI[@]}" link volume "$VOL_ID" publisher "$PUB_ID" || fail "link failed"

step "edit volume --cover"
COVER=$(mktemp /tmp/smoke-cover.XXXXXX.png)
trap 'rm -f "$COVER"; cleanup' EXIT
base64 -d >"$COVER" <<'EOF'
iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR42mNk+M9QDwADhgGAWjR9awAAAABJRU5ErkJggg==
EOF
"${CLI[@]}" edit volume "$VOL_ID" --cover "$COVER" || fail "edit --cover failed"

step "view volume --json"
VIEW=$("${CLI[@]}" view volume "$VOL_ID" --json) || fail "view --json failed"
echo "$VIEW" | jq -e --arg id "$VOL_ID" '.data.id == $id' >/dev/null || fail "view returned wrong record"
TITLE=$(echo "$VIEW" | jq -r '.data.attributes.title')
[ "$TITLE" = "$TAG-volume" ] || fail "title mismatch: $TITLE"

step "delete created records"
"${CLI[@]}" delete volume "$VOL_ID" --force --yes || fail "delete volume failed"
"${CLI[@]}" delete publisher "$PUB_ID" --force --yes || fail "delete publisher failed"
VOL_ID=""
PUB_ID=""

printf 'smoke: PASS\n'
