#!/usr/bin/env bash
# Conformance check: `feint run` sweeps the machines it created, and only those.
#
# #848's sixth required behaviour is "cleans up Feint-owned runtime objects on
# normal exit". Stopping the emulator does not do that: the machines and
# networks it created outlive the process, so the owned lab is armed with
# `serve --cleanup`, which sweeps by the labels the emulator itself wrote.
#
# Both halves are measured here, and the second is the one that matters on an
# operator's own station: the container the run created is GONE afterwards, and
# every container that was already there is UNTOUCHED. A sweep that took a
# neighbour with it would be worse than no sweep, and this repository has
# already measured `incus delete --force` reaching a name it did not own.
#
# This suite owns its lab, unlike the other four in the runtime leg: the
# subject is the command that starts and stops one, so it cannot be handed an
# endpoint somebody else started. The endpoint argument is read only to ask
# whether a machine runtime is configured at all.
#
# Usage: tools/conformance/run-sweep.sh [endpoint]
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

echo "conformance: feint run sweeps its own machines"

mode="$(curl -fsS --max-time 20 "$ENDPOINT/_feint/health" 2>/dev/null \
  | python3 -I -c 'import sys,json; print(json.load(sys.stdin).get("machines",""))' 2>/dev/null || true)"
if [ -z "$mode" ] || [ "$mode" = "none" ] || [ "$mode" = "off" ]; then
  skip "no machine runtime (start with FEINT_VM=incus); nothing to sweep"
  exit 0
fi
if ! command -v incus >/dev/null 2>&1; then
  skip "incus is not on PATH, so what survives cannot be read"
  exit 0
fi
if ! command -v scw >/dev/null 2>&1; then
  skip "the scw CLI is absent, and this suite drives a real client on purpose"
  exit 0
fi

names() { incus list -f csv -c n 2>/dev/null | tr -d '\r'; }

before="$(names || true)"
neighbours="$(printf '%s\n' "$before" | grep -v '^feint-' || true)"
work="$(mktemp -d)"
trap 'rm -rf "$work"' EXIT

cat > "$work/child.sh" <<'CHILD'
set -eu
scw instance server create type=DEV1-S image=ubuntu_jammy \
  name=feint-run-sweep zone=fr-par-1 -o json > /dev/null
# Read from inside, where the container is supposed to exist: asserting after
# the run only proves the absence, which an emulator that started nothing also
# satisfies.
incus list -f csv -c n 2>/dev/null | grep -c '^feint-' > "$FEINT_SWEEP_DURING"
CHILD

export FEINT_SWEEP_DURING="$work/during"
"$FEINT" run --runtime "$mode" --json "$work/receipt.json" -- sh "$work/child.sh" >/dev/null \
  || fail "the run failed; nothing can be concluded about its sweep"

during="$(cat "$work/during" 2>/dev/null || echo 0)"
[ "$during" -ge 1 ] || fail "no feint-owned container existed during the run, so the sweep below proves nothing"
ok "$during feint-owned container(s) existed while the run was going"

after="$(names || true)"
left="$(printf '%s\n' "$after" | grep -c '^feint-' || true)"
[ "$left" = "0" ] || fail "$left feint-owned container(s) outlived the run: $(printf '%s' "$after" | tr '\n' ' ')"
ok "nothing feint created outlived the run"

missing=""
while IFS= read -r neighbour; do
  [ -n "$neighbour" ] || continue
  printf '%s\n' "$after" | grep -qx "$neighbour" || missing="$missing $neighbour"
done <<EOF
$neighbours
EOF
[ -z "$missing" ] || fail "the sweep took a container it does not own:$missing"
ok "every container feint did not create is still there"

cleanup="$(python3 -I -c 'import sys,json; print(json.load(open(sys.argv[1])).get("cleanup",""))' "$work/receipt.json")"
[ "$cleanup" = "clean" ] || fail "the receipt reports cleanup=$cleanup after a sweep that worked"
ok "the receipt says clean, and the host agrees"

echo "conformance: feint run's sweep measured under $mode"
