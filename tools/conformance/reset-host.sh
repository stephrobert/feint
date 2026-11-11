#!/usr/bin/env bash
# Conformance check: a reset reconciles the HOST, and touches nothing else.
#
# This is #853's closing criterion that no unit test can reach: "no foreign
# runtime object is touched". The reconciliation is exercised elsewhere through
# a recording driver, which proves which names the code emits; this proves what
# a real runtime does with them.
#
# Three assertions, and the middle one is the product:
#
#   - the machine of a resource the checkpoint does NOT hold is gone;
#   - the machine of a resource the checkpoint DOES hold is still running. A
#     reset that swept both would return an operator to a checkpoint whose
#     servers answer nothing, which is worse than no reset;
#   - every container the emulator did not create is still there, by name.
#
# The third is why `incus list` is read before and after rather than counted:
# a count cannot tell a neighbour's container from one of ours, and this
# repository has already measured a forced removal reaching a name it did not
# own.
#
# Usage: tools/conformance/reset-host.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
FEINT="${FEINT_BIN:-./feint}"

# shellcheck source=/dev/null
. "$SCRIPT_DIR/guard.sh"
guard_local "$ENDPOINT"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

echo "conformance: a reset reconciles the host and touches nothing else"

mode="$(curl -fsS --max-time 20 "$ENDPOINT/_feint/health" 2>/dev/null \
  | python3 -I -c 'import sys,json; print(json.load(sys.stdin).get("machines",""))' 2>/dev/null || true)"
if [ -z "$mode" ] || [ "$mode" = "none" ] || [ "$mode" = "off" ]; then
  skip "no machine runtime (start with FEINT_VM=incus); the host half cannot be measured"
  exit 0
fi
command -v incus >/dev/null 2>&1 || { skip "incus is not on PATH, so what survives cannot be read"; exit 0; }
command -v scw   >/dev/null 2>&1 || { skip "the scw CLI is absent, and this suite drives a real client"; exit 0; }

# This suite ENDS with a machine alive on purpose: the point of its third
# assertion is that the reset did not take it down. So it sweeps after itself,
# through `feint clean`, which removes what the emulator created by the labels
# it wrote and nothing else. Armed before the first server is created, so a
# failure in the middle does not leak either — the first version of this suite
# left a container running on the maintainer's station, found by reading
# `incus list` after the pass rather than by any gate.
sweep() { "$FEINT" clean --vm "$mode" >/dev/null 2>&1 || true; }
trap sweep EXIT

ADDR="${ENDPOINT#http://}"
set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/scaleway/fake-credentials.env"
set +a
export SCW_API_URL="$ENDPOINT"

names()    { incus list -f csv -c n 2>/dev/null | tr -d '\r'; }
ours()     { names | grep '^feint-' || true; }
strangers(){ names | grep -v '^feint-' || true; }

before_strangers="$(strangers)"
[ -z "$(ours)" ] || fail "feint-owned containers exist before this suite; nothing below could be attributed"

echo "-- a machine the checkpoint will hold"
scw instance server create type=DEV1-S image=ubuntu_jammy name=reset-kept zone=fr-par-1 -o json >/dev/null
kept="$(ours | head -1)"
[ -n "$kept" ] || fail "no container was created for the first server, so this suite would measure nothing"
ok "the checkpoint's machine exists: $kept"

"$FEINT" checkpoint reset-host --addr "$ADDR" >/dev/null || fail "the checkpoint was refused"

echo "-- a machine it will not"
scw instance server create type=DEV1-S image=ubuntu_jammy name=reset-later zone=fr-par-1 -o json >/dev/null
after_second="$(ours | wc -l)"
[ "$after_second" -eq 2 ] || fail "expected two feint-owned containers, found $after_second"
extra="$(ours | grep -v "^$kept$" | head -1)"
ok "the second machine exists: $extra"

echo "-- feint reset reset-host"
"$FEINT" reset reset-host --addr "$ADDR" || fail "the reset reported that the lab did not converge"
ok "the reset reports convergence"

left="$(ours)"
printf '%s\n' "$left" | grep -qx "$extra" && fail "the machine the checkpoint does not hold outlived the reset: $extra"
ok "the machine the checkpoint does not hold is gone"

# The half a sweep would get wrong in the expensive direction.
printf '%s\n' "$left" | grep -qx "$kept" || fail "the reset destroyed the machine the checkpoint HOLDS: $kept"
ok "the machine the checkpoint holds is still there"

missing=""
while IFS= read -r stranger; do
  [ -n "$stranger" ] || continue
  printf '%s\n' "$(strangers)" | grep -qx "$stranger" || missing="$missing $stranger"
done <<EOF
$before_strangers
EOF
[ -z "$missing" ] || fail "the reset took a container it does not own:$missing"
ok "every container feint did not create is still there"

echo "conformance: the host half of a reset measured under $mode"
