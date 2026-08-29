#!/usr/bin/env bash
# `feint up` and `feint down` on a fixture repository, with the real binary.
#
# #190 asks for exactly this, and the reason is that the verbs cannot be proved
# in a Go test: `up` starts the emulator by re-execing the binary, and in a test
# that binary is the test binary. So internal/cli tests the decisions — the
# refusals, the wait, the flags rendered — and this proves the act.
#
# What it asserts, and the last one is the one that finds things:
#
#   1. `feint up` reaches a state where the emulator answers and every ready
#      condition passed, asserted against the emulator;
#   2. the instance it started is one the existing lifecycle verbs know about,
#      because `up` composes `start` rather than growing a second lifecycle;
#   3. `feint down` leaves nothing: no process, and nothing answering the port.
#
# Usage: tools/conformance/environment/up.sh [addr]
set -uo pipefail

# Its own port, never the shared one: this suite starts and stops an emulator of
# its own, and pointing it at the address the rest of the run shares would have
# its cleanup stop somebody else's process.
ADDR="${1:-127.0.0.1:4598}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../../.." && pwd)"
FEINT="${FEINT_BIN:-$ROOT/feint}"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

WORK=""
cleanup() {
  # The emulator goes even when an assertion failed: a leftover process holds
  # the port and the next run then measures it instead of the code under test.
  "$FEINT" stop --addr "$ADDR" >/dev/null 2>&1
  [ -n "$WORK" ] && rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

[ -x "$FEINT" ] || fail "no feint binary at $FEINT; run \`mise run build\` first"

echo "conformance: feint up and feint down on $ADDR"

# A copy, so the repository nobody asked to dirty stays clean: `up` records an
# instance and the engine, when one is declared, writes state beside the file.
WORK="$(mktemp -d)"
cp "$SCRIPT_DIR/fixture/feint.yaml" "$WORK/"
sed -i "s|addr: 127.0.0.1:4599|addr: $ADDR|g; s|tcp:127.0.0.1:4599|tcp:$ADDR|g" "$WORK/feint.yaml"
cd "$WORK" || fail "cannot enter the work directory"

echo "- feint up"
UP_OUT="$WORK/up.log"
"$FEINT" up --timeout 60s >"$UP_OUT" 2>&1
UP=$?
[ "$UP" -eq 0 ] || { cat "$UP_OUT"; fail "up exited $UP"; }
ok "up exited 0"

# Every declared condition was named while it was waited on, and confirmed. A
# green run that printed nothing would be indistinguishable from one that
# skipped the wait.
for condition in "http:/_feint/health" "http:/instance/v1/zones/fr-par-1/servers" "tcp:$ADDR"; do
  grep -qF "ok: $condition" "$UP_OUT" || { cat "$UP_OUT"; fail "the ready condition $condition was never confirmed"; }
done
ok "every ready condition was said out loud and confirmed"

# Asserted against the emulator rather than against up's own output.
curl -sf "http://$ADDR/_feint/health" >/dev/null || fail "the emulator up brought up does not answer"
ok "the emulator answers"

# The declared project catalogue reached the emulator (#572). Asserted against
# the running emulator, never against `up`'s output: what is under test is that
# a field of the declaration became a flag, became a serve, and changed an
# answer a client reads — four hops, and only the last one is worth anything.
org="99999999-9999-4999-8999-999999999999"
declared="$(curl -sf "http://$ADDR/account/v3/projects?organization_id=$org&name=platform-prod")" \
  || fail "the account's projects do not answer"
printf '%s' "$declared" | jq -e '.total_count == 1 and .projects[0].name == "platform-prod"' >/dev/null \
  || fail "the project this declaration names is not held by the emulator it started: $declared"
undeclared="$(curl -sf "http://$ADDR/account/v3/projects?organization_id=$org&name=never-declared")" \
  || fail "the account's projects do not answer"
printf '%s' "$undeclared" | jq -e '.total_count == 0' >/dev/null \
  || fail "a name nobody declared answered a project, which is the echo #572 refuses: $undeclared"
ok "the declared project catalogue reached the emulator, and an undeclared name still answers nothing"

# The instance is one the existing verbs know about: `up` composes `start`, and
# a second lifecycle would show up exactly here.
"$FEINT" status --addr "$ADDR" >/dev/null 2>&1 || fail "status does not know the instance up started"
"$FEINT" logs --addr "$ADDR" -n 1 >/dev/null 2>&1 || fail "logs does not know the instance up started"
ok "status and logs know the instance, so up composed start rather than replacing it"

echo "- feint down"
"$FEINT" down >"$WORK/down.log" 2>&1
DOWN=$?
[ "$DOWN" -eq 0 ] || { cat "$WORK/down.log"; fail "down exited $DOWN"; }
ok "down exited 0"

# Checked, not assumed.
if curl -sf --max-time 2 "http://$ADDR/_feint/health" >/dev/null 2>&1; then
  fail "down returned and something still answers on $ADDR"
fi
ok "nothing answers on $ADDR"

echo "conformance: the environment declaration was brought up and taken down"
