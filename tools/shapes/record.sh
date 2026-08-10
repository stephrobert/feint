#!/usr/bin/env bash
# Record what the three real clouds answer, and fold it into the versioned
# shape catalogues.
#
# This is the half of drift detection the SDK scan cannot do. `mise run
# drift:check` reads the upstream SDK and reports what a provider *offers*;
# nothing reported what it *returns*, and that is where this emulator was
# measurably wrong — a real Outscale account showed ReadVms omitting twenty
# fields, every one of which had passed the contract gate.
#
# It runs on an operator's own station, against their own accounts, on purpose.
# There are no cloud credentials in this repository's CI and there will not be:
# what is committed is the field tree (paths and types, no values), never the
# recording.
#
# READ-ONLY. `feint shapes --record` refuses any call that is not a read, at the
# only place a request is built, so a run interrupted halfway leaves no trace on
# any account. The shapes of *creation* responses need writing and are captured
# deliberately, elsewhere.
#
# One verb per provider, all three the same. It used to be three different
# things — scw and exo driven through `feint proxy`, Outscale read by a Python
# signer beside this file — which meant a copy of the signature per provider and
# one of the pacing. Seven copies of the Outscale signature existed in throwaway
# scripts at one point, and the difference between two of them cost an hour.
#
# Reading directly also sidesteps what defeats the proxy for two of the three
# (#92): nothing here is configured to talk to an intermediary, so no client can
# be handed a different address or refuse a signature computed over the wrong
# host. A proxy records what a client does; this records what a cloud answers,
# and for learning a shape the second is what is wanted. Measured: Exoscale
# through the proxy described 4 operations, read directly it describes 15.
#
# Usage:
#   tools/shapes/record.sh                 all three providers
#   tools/shapes/record.sh outscale        one of them
#
# Environment:
#   OSC_PROFILE   credential profile to use (its meaning is per provider)
#   FEINT_SHAPES  where the catalogues live (default: shapes)
set -uo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/../.." && pwd)"
SHAPES="${FEINT_SHAPES:-$ROOT/shapes}"
WORK="$(mktemp -d)"; chmod 700 "$WORK"
BIN="$WORK/feint"

cd "$ROOT" || exit 1

cleanup() {
  local status=$?
  echo
  echo "-- cleanup"
  rm -rf "$WORK"
  echo "   temporary directory removed"
  exit "$status"
}
trap cleanup EXIT

step() { printf '\n== %s\n' "$1"; }
note() { printf '   %s\n' "$1"; }

step "building"
if ! timeout 300 go build -o "$BIN" ./cmd/feint; then
  note "build failed"; exit 1
fi
note "ok"

wanted=("$@")
[ ${#wanted[@]} -eq 0 ] && wanted=(scaleway outscale exoscale)

failed=()
for p in "${wanted[@]}"; do
  step "$p"
  if [ -n "${OSC_PROFILE:-}" ]; then
    timeout 1800 "$BIN" shapes --record --provider "$p" --dir "$SHAPES" --profile "$OSC_PROFILE" || failed+=("$p")
  else
    timeout 1800 "$BIN" shapes --record --provider "$p" --dir "$SHAPES" || failed+=("$p")
  fi
done

step "done"
for f in "$SHAPES"/*.json; do
  [ -e "$f" ] || continue
  n=$(python3 -c "import json,sys; print(len(json.load(open(sys.argv[1]))['operations']))" "$f" 2>/dev/null || echo '?')
  note "$(basename "$f"): $n operation(s) described"
done
if [ ${#failed[@]} -gt 0 ]; then
  note "provider(s) that recorded nothing: ${failed[*]}"
  exit 1
fi
note "read \`git diff -- $SHAPES\` : that diff is the behavioural drift report"
