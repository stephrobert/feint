#!/usr/bin/env bash
# The Day-2 leg (#673): a month of an operator's changes on one standing stack,
# each read back, and then the whole platform proved working again.
#
# A provisioning client asks "did it get created". A Day-2 client asks "is it
# still what I think it is, after I changed it". Four defects of this
# repository came from a generated Ansible collection asking the second
# question (#654, #658, #666, the reboot half of #637), and no gate here asked
# it: the suites replay one lifecycle per resource, in order, and never the
# middle of a resource's life, which is where a control plane rots.
#
# Three properties, in this order:
#
#   1. many Day-2 writes on one standing stack — the catalogue in day2lib.sh,
#      in do/undo pairs so the platform ends where it started;
#   2. a read after every write, and the read has to say the change happened,
#      because a 200 is not evidence (#654);
#   3. at the end, the platform proved working again: the machine's own table
#      compared with its capture from before the writes (#671's comparator,
#      reused rather than rewritten — a net-zero catalogue is exactly a
#      comparison of shape after mutations), the emulator's own verification
#      counters read (#670), and the stack gate's verdicts replayed on the
#      SAME stack — the services, the firewall pair, the network, the
#      balancer, the rule sets, the restart — thirty-odd writes older.
#
# The control-plane half needs no machine runtime. The data-plane half needs
# one, and with none it says so, by name, rather than skipping in silence: a
# pass that measured only the control plane must not read like a pass that
# measured the platform.
#
# Not the external collection. It lives in another repository, on its own
# cadence, and a gate that goes red because a collection moved measures the
# collection. Its module list is the map this catalogue was drawn from, and
# the clients here are the API the modules drive.
#
# Usage: FEINT_VM=off|incus-ovn tools/conformance/day2.sh [stack]   (default: scaleway)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ADDR="${FEINT_DAY2_ADDR:-127.0.0.1:4596}"
ENDPOINT="http://$ADDR"
RUNTIME="${FEINT_VM:-off}"
STACK="${1:-scaleway}"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

# shellcheck source=/dev/null
. "$SCRIPT_DIR/guard.sh"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/functionallib.sh"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/day2lib.sh"

guard_local "$ENDPOINT"

echo "conformance: the Day-2 leg on $ENDPOINT, stack $STACK, runtime $RUNTIME"
for tool in jq curl; do
	command -v "$tool" >/dev/null 2>&1 || fail "$tool is not installed; this leg cannot read anything without it"
done
[ "$STACK" = scaleway ] \
	|| fail "no Day-2 catalogue for the $STACK stack yet: day2lib.sh carries Scaleway's, and a stack with none is refused rather than played empty"

FEINT="$(feint_binary)"
[ -x "$FEINT" ] || fail "no feint binary at $FEINT; run \`mise run build\` first"

if [ "$RUNTIME" != off ]; then
	command -v incus >/dev/null 2>&1 \
		|| fail "the incus client is not on PATH, and FEINT_VM=$RUNTIME asks for the data-plane half"
	if ! "$FEINT" doctor --vm "$RUNTIME" >/dev/null 2>&1; then
		"$FEINT" doctor --vm "$RUNTIME" >&2 || true
		fail "this host cannot deliver the $RUNTIME runtime (doctor above)"
	fi
	"$FEINT" images --check --vm "$RUNTIME" >&2 \
		|| fail "the $RUNTIME runtime is missing images this stack boots. Run: $FEINT images --vm $RUNTIME"
	guard_leftovers_for "$RUNTIME" doorstep
fi

echo "- the readers find their planted witnesses"
d2_reader_control
fnl_shape_reader_control

# d2_live_shape is functional.sh's live_shape, spelled here because that file
# is a script and not a library: two execs, each one's failure its own verdict
# — a capture with one half missing compares as a machine that lost half its
# table, which is not a measurement.
d2_live_shape() { # machine out_file
	local routes addresses
	routes="$(incus exec "$1" -- ip -4 route show 2>/dev/null)" || return 1
	addresses="$(incus exec "$1" -- ip -4 -o addr show 2>/dev/null)" || return 1
	{
		printf '%s\n' "$addresses" | fnl_shape_addresses
		printf '%s\n' "$routes" | fnl_shape_routes
	} >"$2"
	[ -s "$2" ]
}

# ---- the stack, up -----------------------------------------------------------
WORK=""
UP=""
# The trap sweeps through d2_sweep (day2lib.sh), which downs from the stack
# directory while it exists, stops and sweeps by address when it is gone, and
# refuses to report a sweep it did not see. A sweep that fails makes the run
# red even when the run itself had passed: seven machines left standing is a
# failure of this leg, whatever the verdicts above said.
cleanup() {
	local rc=$?
	d2_sweep "$WORK" "$UP" "$FEINT" "$ADDR" "$RUNTIME" || rc=1
	WORK=""
	UP=""
	exit "$rc"
}
trap cleanup EXIT INT TERM

src="$ROOT/examples/stacks/$STACK"
[ -d "$src" ] || fail "no stack at $src"
echo "- $STACK: feint up --runtime $RUNTIME"
WORK="$(mktemp -d)"
cp "$src"/*.tf "$src/feint.yaml" "$WORK/"
[ -d "$src/modules" ] && cp -R "$src/modules" "$WORK/"
d2_declare_endpoint "$WORK/feint.yaml" "$ADDR"
# The emulator this leg starts cleans up after itself on stop: the sweep's
# fallback relies on it (d2_sweep, day2lib.sh).
d2_declare_cleanup "$WORK/feint.yaml"
(cd "$WORK" && "$FEINT" up --runtime "$RUNTIME" --timeout 900s) >"$WORK/up.log" 2>&1 \
	|| { tail -n 40 "$WORK/up.log" >&2; fail "$STACK: feint up failed (log above)"; }
UP="yes"
ok "up, applied, every ready condition confirmed"

curl -sf "$ENDPOINT/_feint/state" >"$WORK/state.json" || fail "$STACK: the emulator does not answer /_feint/state"
# Read by the transports in day2lib.sh, which shellcheck cannot see from here.
# shellcheck disable=SC2034
D2_ENDPOINT="$ENDPOINT"
# shellcheck disable=SC2034
D2_READS_FILE="$WORK/reads"
d2_resolve_scaleway "$WORK/state.json"

# The machine whose own table is compared across the catalogue: the one the
# stack gate restarts, resolved the way that gate resolves it.
machine=""
if [ "$RUNTIME" != off ]; then
	machine="$(fnl_machine_named instance/server platform-web-0 <"$WORK/state.json")"
	[ -n "$machine" ] || fail "$STACK: no instance/server named platform-web-0 carries a machine, and the runtime was asked for"
	d2_live_shape "$machine" "$WORK/shape-before.txt" \
		|| fail "cannot look: the shape of platform-web-0 ($machine) could not be read before the catalogue"
fi

# ---- the catalogue -----------------------------------------------------------
echo "- $STACK: the Day-2 catalogue, ${#D2_SCALEWAY_STEPS[@]} steps, each write read back"
d2_catalogue_scaleway
ok "$STACK: $D2_WRITES writes, $(d2_reads) reads, every read said the change happened"

# ---- the platform, after -----------------------------------------------------
if [ "$RUNTIME" != off ]; then
	echo "- $STACK: platform-web-0 carries after $D2_WRITES writes what it carried before"
	d2_live_shape "$machine" "$WORK/shape-after.txt" \
		|| fail "cannot look: the shape of platform-web-0 ($machine) could not be read after the catalogue"
	fnl_shape_survives_restart "$STACK" platform-web-0 "$machine" \
		"$WORK/shape-before.txt" "$WORK/shape-after.txt" "after $D2_WRITES Day-2 writes"
fi

echo "- $STACK: what the emulator read back against every plan, across the catalogue"
"$SCRIPT_DIR/guard.sh" verification "$ENDPOINT" || fail "$STACK: the emulator's own verification refused the catalogue (above)"

if [ "$RUNTIME" != off ]; then
	echo "- $STACK: the stack gate, on the same stack, $D2_WRITES writes older"
	FEINT_STACK_UP="$WORK" FEINT_FUNCTIONAL_ADDR="$ADDR" FEINT_FUNCTIONAL_PASSES=1 FEINT_VM="$RUNTIME" \
		"$SCRIPT_DIR/functional.sh" "$STACK" \
		|| fail "$STACK: the platform does not work after $D2_WRITES Day-2 writes; the stack gate above says what stopped"
else
	skip "$STACK: the data-plane half was not asked — no machine runtime, so the services, the firewall pair, the network, the balancer and the machine's own table were NOT proved after these $D2_WRITES writes. FEINT_VM=incus-ovn tools/conformance/day2.sh $STACK asks it"
fi

# ---- down --------------------------------------------------------------------
echo "- $STACK: feint down"
(cd "$WORK" && "$FEINT" down) >"$WORK/down.log" 2>&1 \
	|| { tail -n 20 "$WORK/down.log" >&2; fail "$STACK: feint down failed"; }
UP=""
if curl -sf --max-time 2 "$ENDPOINT/_feint/health" >/dev/null 2>&1; then
	fail "$STACK: down returned and something still answers on $ADDR"
fi
ok "down, nothing answers on $ADDR"
D2_READ_COUNT="$(d2_reads)"
rm -rf "$WORK"
WORK=""
[ "$RUNTIME" = off ] || guard_leftovers_for "$RUNTIME" doorstep
echo "day2: the $STACK stack took $D2_WRITES writes and $D2_READ_COUNT reads, runtime $RUNTIME"
