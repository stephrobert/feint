#!/usr/bin/env bash
# Run an older release's consumer expressions against this one, and say which
# bucket each lands in (#170).
#
#   bash --noprofile --norc tools/compat/compat.sh [ref]
#
# ref defaults to the latest tag. Offline throughout: it builds the older binary
# from the repository's own history, starts both on loopback, seeds them
# identically with the protocol probe, and evaluates every expression on both.
#
# The classification lives in internal/compat, in Go, so it is unit-tested and
# falsifiable; this script's job is to produce the two payloads honestly. What it
# must never do is read the frozen fixtures — a harness that only knows the shapes
# we already froze agrees with itself.
#
# Exit 0 when nothing is silently wrong beyond the accepted list, 1 otherwise.
set -uo pipefail

cd "$(dirname "$0")/../.." || exit 1
WORK="$(mktemp -d)"
OLD_PORT=4801
NEW_PORT=4802
trap 'kill %1 %2 2>/dev/null; git worktree remove --force "$WORK/old" 2>/dev/null; rm -rf "$WORK"' EXIT

REF="${1:-$(git describe --tags --abbrev=0)}"
echo "comparing $REF with the working tree"

for tool in jq go git; do
  command -v "$tool" >/dev/null 2>&1 || { echo "FAIL: $tool is required" >&2; exit 1; }
done

echo "- building both binaries"
git worktree add -q --detach "$WORK/old" "$REF" || { echo "FAIL: cannot check out $REF" >&2; exit 1; }
(cd "$WORK/old" && go build -o "$WORK/feint-old" ./cmd/feint) \
  || { echo "FAIL: $REF does not build" >&2; exit 1; }
go build -o "$WORK/feint-new" ./cmd/feint || { echo "FAIL: the working tree does not build" >&2; exit 1; }

echo "- starting both, and seeding them the same way"
"$WORK/feint-old" serve --addr "127.0.0.1:$OLD_PORT" >"$WORK/old.log" 2>&1 &
"$WORK/feint-new" serve --addr "127.0.0.1:$NEW_PORT" >"$WORK/new.log" 2>&1 &
for port in "$OLD_PORT" "$NEW_PORT"; do
  ready=""
  for _ in $(seq 1 40); do
    curl -sf "http://127.0.0.1:$port/_feint/health" >/dev/null 2>&1 && { ready=yes; break; }
    sleep 0.5
  done
  [ -n "$ready" ] || { echo "FAIL: nothing answered on $port" >&2; exit 1; }
done

# Seeded identically, because an expression that counts evidence measures nothing
# against an emulator nobody drove. The probe is the only seed both releases have
# in common — a conformance suite would need clients this script must not require.
"$WORK/feint-old" probe --endpoint "http://127.0.0.1:$OLD_PORT" >/dev/null 2>&1 || true
"$WORK/feint-new" probe --endpoint "http://127.0.0.1:$NEW_PORT" >/dev/null 2>&1 || true

for surface in health routes conformance trace; do
  curl -s "http://127.0.0.1:$OLD_PORT/_feint/$surface" >"$WORK/old-$surface.json" 2>/dev/null
  curl -s "http://127.0.0.1:$NEW_PORT/_feint/$surface" >"$WORK/new-$surface.json" 2>/dev/null
done

echo "- evaluating the consumer's expressions on both"
: >"$WORK/results.json"
# A selector is reduced to what it still discriminates rather than to how many
# it matched. none/some/all is invariant to the emulator growing, which a count
# is not — the first draft of this harness reported a finding purely because 24
# operations had been added since the older release.
signature() { # payload expr kind population
  local payload=$1 expr=$2 kind=$3 population=$4 value total
  value="$(jq -c "$expr" "$payload" 2>/dev/null)" || { echo '<error>'; return; }
  [ -n "$value" ] || { echo '<error>'; return; }
  if [ "$kind" != "selector" ]; then
    printf '%s\n' "$value"
    return
  fi
  total="$(jq -c "$population" "$payload" 2>/dev/null)" || { echo '<error>'; return; }
  case "$total" in
  ''|0) echo '<empty population>' ;;
  *)
    if [ "$value" = "0" ]; then echo none
    elif [ "$value" = "$total" ]; then echo all
    else echo some
    fi
    ;;
  esac
}

jq -c '.expressions[]' tools/compat/consumer.json | while read -r entry; do
  name="$(printf '%s' "$entry" | jq -r '.name')"
  surface="$(printf '%s' "$entry" | jq -r '.surface')"
  expr="$(printf '%s' "$entry" | jq -r '.expr')"
  means="$(printf '%s' "$entry" | jq -r '.means')"
  kind="$(printf '%s' "$entry" | jq -r '.kind')"
  population="$(printf '%s' "$entry" | jq -r '.population // ""')"

  before="$(signature "$WORK/old-$surface.json" "$expr" "$kind" "$population")"
  after="$(signature "$WORK/new-$surface.json" "$expr" "$kind" "$population")"

  jq -nc --arg n "$name" --arg s "$surface" --arg e "$expr" --arg m "$means" \
        --arg b "$before" --arg a "$after" \
    '{name:$n, surface:$s, source:$e, means:$m, before:$b, after:$a}' >>"$WORK/results.json"
done

# The version of each surface on each side. Absent is not zero: schema_version
# did not exist before #132, and a release that carried none gave its consumers
# nothing to key on.
: >"$WORK/versions.json"
for surface in health routes conformance trace; do
  bv="$(jq -r 'if has("schema_version") then (.schema_version|tostring) else "" end' "$WORK/old-$surface.json" 2>/dev/null)"
  av="$(jq -r 'if has("schema_version") then (.schema_version|tostring) else "" end' "$WORK/new-$surface.json" 2>/dev/null)"
  jq -nc --arg s "$surface" --arg b "$bv" --arg a "$av" \
    '{surface:$s, before:$b, after:$a, beforeKnown:($b != ""), afterKnown:($a != "")}' \
    >>"$WORK/versions.json"
done

echo
go run ./tools/compat/classify "$WORK/results.json" "$WORK/versions.json" tools/compat/accepted.json
