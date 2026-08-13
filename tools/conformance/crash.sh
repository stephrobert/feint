#!/usr/bin/env bash
# Conformance check: the crash policy, proven by a kill (#135).
#
# docs/limits.md states the lifecycle in one table: die and the labelled
# leftovers survive; restart and nothing is adopted but the leftovers are
# named; `feint clean` and the runtime is empty again. Every row of that table
# that can be triggered is triggered here, in order, against a real runtime:
# SIGKILL on a serving emulator with a machine up, the leftovers asserted
# present and labelled, the restart asserted to warn and to adopt nothing, the
# sweep asserted to leave zero labelled objects — with the runtime queried
# directly, because the store is not a witness for what the host holds.
#
# Requires a machine runtime (FEINT_VM=incus, incus-vm or incus-ovn). With
# FEINT_VM unset or off the whole suite is skipped: there is no runtime to
# leave anything on.
#
# This suite starts its own emulator on its own port and must not run beside
# another feint that is serving machines: the final sweep removes everything
# carrying the emulator's label, and labels identify the emulator's work, not
# one run's.
#
# Usage: FEINT_VM=incus tools/conformance/crash.sh
set -euo pipefail

MODE="${FEINT_VM:-off}"
PORT="${FEINT_CRASH_PORT:-4661}"
ADDR="127.0.0.1:$PORT"
EP="http://$ADDR"
ZONE="fr-par-1"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=/dev/null
. "$SCRIPT_DIR/guard.sh"
guard_local "$EP"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

if [ "$MODE" = "off" ]; then
  skip "no machine runtime (set FEINT_VM=incus); a crash with no machines leaves nothing to prove"
  exit 0
fi
command -v incus >/dev/null 2>&1 || fail "FEINT_VM=$MODE but no incus client to verify the runtime with"

FEINT_BIN="${FEINT_BIN:-$SCRIPT_DIR/../../feint}"
[ -x "$FEINT_BIN" ] || fail "no feint binary at $FEINT_BIN (build it: mise run build)"

echo "conformance: crash policy against $EP (mode $MODE)"

# The failure path frees the runtime too: a suite that dies holding its
# emulator blocks whatever runs next for its whole timeout, which was measured
# on this repository at ten minutes for one missing trap.
cleanup() {
  "$FEINT_BIN" stop --addr "$ADDR" >/dev/null 2>&1 || true
  "$FEINT_BIN" clean --vm "$MODE" >/dev/null 2>&1 || true
}
trap cleanup EXIT

# Labelled leftovers on the runtime, counted by asking the runtime itself.
labelled_instances() {
  incus list -f json | jq '[.[] | select((.config["user.feint.provider"] // "") != "")] | length'
}
labelled_networks() {
  incus network list -f json | jq '[.[] | select((.config["user.feint.provider"] // "") != "")] | length'
}
owned_rulesets() {
  incus network acl list -f json | jq '[.[] | select(((.description // "") | startswith("feint security group")) or (.name | startswith("iso-fnt-")))] | length'
}

echo "- a clean runtime to start from"
"$FEINT_BIN" clean --vm "$MODE" >/dev/null
[ "$(labelled_instances)" = 0 ] || fail "labelled machines exist before the suite began; refusing to measure over them"
ok "zero labelled machines"

echo "- an emulator serving machines"
out="$("$FEINT_BIN" start --addr "$ADDR" --vm "$MODE" --timeout 90s)"
pid="$(echo "$out" | sed -n 's/.*(pid \([0-9]\+\)).*/\1/p' | head -1)"
[ -n "$pid" ] || fail "start did not report a pid: $out"
ok "pid $pid"

echo "- a server booted through the API"
id="$(curl -sf -X POST "$EP/instance/v1/zones/$ZONE/servers" \
  -H 'Content-Type: application/json' \
  -d '{"name":"crash-proof","commercial_type":"DEV1-S","image":"alpine"}' | jq -r '.server.id')"
[ -n "$id" ] && [ "$id" != null ] || fail "the server was not created"
curl -sf -X POST "$EP/instance/v1/zones/$ZONE/servers/$id/action" \
  -H 'Content-Type: application/json' -d '{"action":"poweron"}' >/dev/null
machine="feint-scw-$id"
running=""
for _ in $(seq 1 60); do
  if incus list -f json | jq -e --arg n "$machine" \
       'any(.[]; .name == $n and .status == "Running")' >/dev/null; then running=yes; break; fi
  sleep 2
done
[ -n "$running" ] || fail "$machine never reached Running"
ok "$machine is running"

echo "- kill -9: the crash nothing announces"
kill -9 "$pid"
for _ in $(seq 1 10); do kill -0 "$pid" 2>/dev/null || break; sleep 1; done
kill -0 "$pid" 2>/dev/null && fail "pid $pid survived SIGKILL"
ok "the emulator is dead"

echo "- the leftovers survive, labelled"
incus list -f json | jq -e --arg n "$machine" \
  'any(.[]; .name == $n and .status == "Running" and .config["user.feint.provider"] == "scaleway")' >/dev/null \
  || fail "$machine is gone or unlabelled: the crash policy says it must survive, labelled"
ok "$machine still runs and carries user.feint.provider=scaleway"

echo "- a restart names them and adopts nothing"
"$FEINT_BIN" start --addr "$ADDR" --vm "$MODE" --timeout 90s >/dev/null
log="$("$FEINT_BIN" logs --addr "$ADDR" -n 80)"
echo "$log" | grep -q "labelled machines from a previous run exist" \
  || fail "the restart said nothing about the leftovers (the notice #135 requires): $log"
echo "$log" | grep -q "$machine" \
  || fail "the notice does not name $machine: $log"
servers="$(curl -sf "$EP/instance/v1/zones/$ZONE/servers" | jq '.servers | length')"
[ "$servers" = 0 ] || fail "the restart adopted $servers server(s); the store must start empty"
ok "warned, named $machine, store empty"

echo "- clean leaves zero labelled objects, asked of the runtime itself"
"$FEINT_BIN" stop --addr "$ADDR" >/dev/null
# The sweep's exit code is reported but does not render the verdict: the state
# of the runtime does. A clean that exits 0 while leaving a machine and a
# clean that exits 1 having removed nothing must both fail on the assertions
# below — measured: a Prune mutated to skip machines failed here as "network
# is in use" and a set -e abort, one line before the assertion that names the
# actual defect.
if sweep="$("$FEINT_BIN" clean --vm "$MODE" 2>&1)"; then
  echo "  sweep: ${sweep%%$'\n'*}"
else
  echo "  sweep reported: ${sweep%%$'\n'*}"
fi
[ "$(labelled_instances)" = 0 ] || fail "labelled machines remain after clean"
[ "$(labelled_networks)" = 0 ] || fail "labelled networks remain after clean"
[ "$(owned_rulesets)" = 0 ] || fail "emulator-owned rule sets remain after clean"
ok "zero labelled machines, networks and rule sets"

echo "conformance: crash policy passed"
