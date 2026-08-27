#!/usr/bin/env bash
# What a stack must prove (#503): its machines do the thing, not merely exist.
#
# `tools/conformance/stacks.sh` asks three questions of every example stack:
# apply, second plan empty, clean destroy. On 2026-08-26 four defects passed all
# three — #475, #481, #483, #484 — and three of them left no ERROR line at all.
# The definition of "the stack works" was the exit code of a client, and it let
# through a public address handed out twice, two packs applying no firewall, and
# a load balancer that distributed nothing.
#
# What found them was a person opening a session on the host and looking. This
# gate is that look, written down. `witness.sh` covers the half that asks
# whether the OBJECT is on the runtime; this one asks whether a PACKET arrives.
#
# The four families, per stack, each declared by the stack itself in
# `examples/stacks/<name>/proof.json` rather than by a table living here:
#
#   service    a service listens inside the machine, answers over the address
#              the provider's own API publishes, and survives a restart of the
#              machine through that same API — both doors of it, and the machine
#              still reaches the subnet it reached before (service.restart_reaches)
#   rule_sets  the runtime holds, after those restarts, exactly the rule sets and
#              the references the stack declares
#   firewall   a port a rule opens answers and a port no rule opens refuses,
#              both in the same pass, both proved listening inside first
#   network    two machines of one network reach each other, and two networks
#              nothing peers do not
#   balancer   the balancer hands connections to the backends the pack recorded
#              as delivered, to none it recorded as withheld, and unregistering
#              one is visible from outside
#
# A family the stack cannot offer is written `{"skip": "<reason>"}`, said out
# loud, exit 0. A family simply absent is a FAIL: "this stack declares nothing
# about its firewall" is a finding, not a default.
#
# ---------------------------------------------------------------------------
# By which door the machine is reached, which is the decision the rest rests on.
#
# `incus exec <target> -- curl localhost` would talk to the machine through the
# host, past the network, past the rule sets, past the published address. It
# would have found none of the four defects. So every probe here leaves either
# another machine or the station, and travels the address the API published;
# `incus exec` appears only as the console that originates a probe from inside a
# machine, never as a way to ask the target whether it is well. The listen
# proof, and only it, is read from inside the target — /proc/net/tcp, state 0A —
# because without it a refusal cannot be told from a dead service.
#
# ---------------------------------------------------------------------------
# Three decisions taken from measurement rather than preference, all 2026-08-27
# under `--vm incus-ovn`, and each one found by this file going red:
#
#   The restart is BOTH doors, in order: the provider's own reboot verb first,
#   then poweroff + poweron. The reboot verb used to be excluded here, because
#   it answered success and restarted nothing — same container pid, uptime still
#   climbing, a transient marker unit still active (#547). It is asserted now
#   rather than avoided: the runtime process must differ across the call, which
#   is the witness that filed the issue, read from the host.
#
#   The restart runs LAST, after every other family. A machine that came back
#   from a restart used to have lost the guest routes to its peered subnets, so
#   anything probed after one measured that loss rather than the property it
#   names. The first ordering restarted a web machine before the firewall pair,
#   the pair went red on its positive half, and the control in the same pass —
#   the neighbour nobody restarted, still reaching — named the cause (#549).
#   The order stays, and what used to be a skip beside it is now the assertion:
#   the restarted machine must still reach the other subnet it reached before,
#   with that same unrestarted neighbour as the control of the pass.
#
#   The firewall pair runs between two subnets, never between neighbours. Within
#   one subnet the sender's permissive egress outranks the receiver's ingress
#   default (docs/limits.md; measured again here, web-0 reaching web-1 on a port
#   no rule opens), so a closure probed from a neighbour would measure nothing.
#   The same bound is why `control_source` is a machine of another subnet or a
#   declared reason there is none, never the target's own neighbour.
#
# ---------------------------------------------------------------------------
# What this does not cover, said rather than left to be discovered.
#
# Exoscale. Since #525 no Terraform is pointed at that pack — the published
# provider splits an apply between the emulator and a paying account — so
# `feint up` refuses `examples/stacks/exoscale` before anything starts and this
# gate cannot bring it up. Its machines, addresses and firewall are exercised by
# the `exo` CLI in tools/conformance/exoscale/. What that leaves unmeasured for
# Exoscale is exactly this file's subject: no service of an Exoscale stack is
# proved to listen, answer and survive a restart, no open/closed port pair is
# probed on one of its machines, and its NLB grades no backend at all
# (docs/limits.md), so there is no delivery record to compare a connection to.
# The day the published provider honours EXOSCALE_API_ENDPOINT, the stack gets a
# proof.json and joins the default list below.
#
# ---------------------------------------------------------------------------
# Where this runs. It boots machines, so it belongs on the same terms as
# `conformance:ssh` and `conformance:witness`: `mise run conformance:functional`
# by hand, and the incus-ovn leg of .github/workflows/runtime-proof.yml, which
# #504 wires. It must never join a gate the CI runs without a runtime: there it
# could not look, and a check that cannot look says so rather than passing.
#
# It is written to be called: stack names as arguments, one verdict line per
# assertion naming the stack and the object, exit 0 / 1, a doorstep that refuses
# before it starts rather than failing late, and its own emulator on its own
# port so it never stops the one another suite is measuring.
#
# The verdicts live in functionallib.sh, held by functional_test.go against
# planted defects, and tools/falsify/specs/stack-functional-proof.json replays
# those tests with each guard neutralised.
#
# Usage: tools/conformance/functional.sh [stack ...]   (default: scaleway outscale)
set -uo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
ADDR="${FEINT_FUNCTIONAL_ADDR:-127.0.0.1:4595}"
ENDPOINT="http://$ADDR"
RUNTIME="${FEINT_FUNCTIONAL_RUNTIME:-incus-ovn}"
ZONE="${FEINT_FUNCTIONAL_ZONE:-fr-par-1}"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

# shellcheck source=/dev/null
. "$SCRIPT_DIR/guard.sh"
# shellcheck source=/dev/null
. "$SCRIPT_DIR/functionallib.sh"

guard_local "$ENDPOINT"

echo "conformance: what the example stacks must prove, on $ENDPOINT, runtime $RUNTIME"

# ---- doorstep: can anybody look? -------------------------------------------
#
# "No measurement because nobody could look" and "no defect" are two different
# verdicts, and only the second may be silent.
for tool in jq curl; do
	command -v "$tool" >/dev/null 2>&1 || fail "$tool is not installed; this gate cannot read anything without it"
done
if ! command -v incus >/dev/null 2>&1; then
	skip "the incus client is not on PATH: no machine can be asked what it listens on, so NOTHING WAS MEASURED"
	exit 0
fi

FEINT="$(feint_binary)"
[ -x "$FEINT" ] || fail "no feint binary at $FEINT; run \`mise run build\` first"

if ! "$FEINT" doctor --vm "$RUNTIME" >/dev/null 2>&1; then
	echo "--- feint doctor --vm $RUNTIME ---" >&2
	"$FEINT" doctor --vm "$RUNTIME" >&2 || true
	skip "this host cannot deliver the $RUNTIME runtime (doctor above): NOTHING WAS MEASURED"
	exit 0
fi
if ! "$FEINT" images --check --vm "$RUNTIME" >&2; then
	fail "the $RUNTIME runtime is missing images these stacks boot. Run: $FEINT images --vm $RUNTIME"
fi
guard_leftovers_for "$RUNTIME" doorstep

# ---- the readers prove they can find before anything is judged --------------
echo "- the readers find their planted witnesses"
fnl_listen_reader_control
fnl_name_reader_control
fnl_delivery_reader_control
fnl_rule_set_reader_control

# ---- live transports --------------------------------------------------------
#
# Every one of them distinguishes three outcomes, and none of them calls `fail`:
# they are read through command substitution, where `fail` would exit the
# subshell alone and leave the caller reading an empty string as an absence.
# That is rule 2 of the measurement-integrity skill and the shape that reported
# a live account empty for forty minutes. They return non-zero instead, and
# every call site says what it could not obtain.

# live_listeners writes one decimal port per line into a file, non-zero when the
# machine could not be read at all.
live_listeners() { # machine out_file
	local raw
	raw="$(incus exec "$1" -- sh -c 'cat /proc/net/tcp /proc/net/tcp6 2>/dev/null' 2>/dev/null)" || return 1
	[ -n "$raw" ] || return 1
	printf '%s\n' "$raw" | fnl_listening_ports >"$2"
}

# live_probe: 0 the port answers, 1 it refuses or times out, 2 nobody could look.
#
# The third outcome is not decoration. This file's first exploratory run probed
# with `sh -c 'echo >/dev/tcp/…'`; Ubuntu's /bin/sh is dash, which has no
# /dev/tcp, and ninety probes read "closed" including every one that was open.
live_probe() { # machine address port
	incus exec "$1" -- true >/dev/null 2>&1 || return 2
	# `sh -c` and not `incus exec … -- command -v bash`: incus execs the
	# argument directly, with no shell, and `command` is a shell builtin, so the
	# bare form fails on every machine including the ones that carry bash. The
	# first run of this gate reported "cannot look" on a perfectly good probe for
	# exactly that reason — and reported it rather than guessing, which is what
	# the third outcome is for.
	incus exec "$1" -- sh -c 'command -v bash >/dev/null' >/dev/null 2>&1 || return 2
	incus exec "$1" -- timeout 6 bash -c "exec 3<>/dev/tcp/$2/$3" >/dev/null 2>&1 && return 0
	return 1
}

# live_fetch prints the body one machine gets from another, or nothing.
live_fetch() { # machine address port
	incus exec "$1" -- timeout 6 python3 -c \
		'import sys,urllib.request; print(urllib.request.urlopen("http://%s:%s/" % (sys.argv[1], sys.argv[2]), timeout=5).read().decode().strip())' \
		"$2" "$3" 2>/dev/null
}

# station_fetch is the same trip from where the operator stands.
station_fetch() { # address port
	curl -sf --max-time 8 "http://$1:$2/" 2>/dev/null | tr -d '\r\n'
}

# live_machine_pid answers the runtime process holding a machine, empty when
# nobody could look. It is the witness #547 was filed on: a reboot that leaves
# the same process leaves the same kernel and the same uptime. Read from the
# host, like witness.sh reads an instance's status — never from inside the
# target, which this gate only uses as the console that originates a probe.
live_machine_pid() { # machine
	incus query "/1.0/instances/$1/state" 2>/dev/null | jq -r '.pid // empty'
}

# live_machine_acls answers the rule sets a machine's interfaces carry, comma
# separated, non-zero when the instance could not be read at all. The reading of
# the document is witnesslib's, not a second one written here.
live_machine_acls() { # machine
	local doc
	doc="$(incus query "/1.0/instances/$1" 2>/dev/null)" || return 1
	printf '%s' "$doc" | witness_instance_acls | paste -sd, -
}

# private_address answers the address a machine carries on an emulated network,
# read off the runtime. A NIC that names no network is the routed one carrying
# the public address (#202), and the emulated-network assertions are not about
# it.
private_address() { # machine
	local doc
	doc="$(incus query "/1.0/instances/$1" 2>/dev/null)" || return 1
	printf '%s' "$doc" | jq -r '[.expanded_devices // {} | to_entries[]
	          | select(.value.type == "nic" and ((.value.network // "") != ""))
	          | .value["ipv4.address"] // empty] | first // ""'
}

# ---- the claims each provider's own API publishes ---------------------------
#
# A provider this gate has no reader for must fail loudly the day a stack names
# it: a gate that silently reads nothing passes on the population it exists to
# judge. `unknown provider` is returned as exit 3 rather than `fail` for the
# subshell reason above, and every call site turns it into a named failure.

api() { curl -sf -H 'Content-Type: application/json' "$ENDPOINT$1"; }
osc() { # action [body]
	local body="${2:-}"
	[ -n "$body" ] || body='{}'
	curl -sf -X POST -H 'Content-Type: application/json' -d "$body" "$ENDPOINT/api/v1/$1"
}

published_address() { # provider name -> the public address the API publishes, or empty
	local doc
	case "$1" in
	scaleway)
		doc="$(api "/instance/v1/zones/$ZONE/servers")" || return 1
		printf '%s' "$doc" | jq -r --arg n "$2" '[.servers[] | select(.name == $n) | .public_ips[]?.address] | first // ""' ;;
	outscale)
		doc="$(osc ReadVms)" || return 1
		printf '%s' "$doc" | jq -r --arg n "$2" '[.Vms[] | select(((.Tags // []) | map(select(.Key == "Name") | .Value) | first) == $n) | .PublicIp // empty] | first // ""' ;;
	*) return 3 ;;
	esac
}

resource_state() { # provider id
	local doc
	case "$1" in
	scaleway)
		doc="$(api "/instance/v1/zones/$ZONE/servers/$2")" || return 1
		printf '%s' "$doc" | jq -r '.server.state' ;;
	outscale)
		doc="$(osc ReadVms)" || return 1
		printf '%s' "$doc" | jq -r --arg i "$2" '[.Vms[] | select(.VmId == $i) | .State] | first // ""' ;;
	*) return 3 ;;
	esac
}

power() { # provider id off|on|reboot
	local action
	case "$1" in
	scaleway)
		action=poweroff
		[ "$3" = on ] && action=poweron
		[ "$3" = reboot ] && action=reboot
		curl -sf -X POST -H 'Content-Type: application/json' -d "{\"action\":\"$action\"}" \
			"$ENDPOINT/instance/v1/zones/$ZONE/servers/$2/action" >/dev/null ;;
	outscale)
		action=StopVms
		[ "$3" = on ] && action=StartVms
		[ "$3" = reboot ] && action=RebootVms
		osc "$action" "{\"VmIds\":[\"$2\"]}" >/dev/null ;;
	*) return 3 ;;
	esac
}

balancer_address() { # provider name
	local doc
	case "$1" in
	outscale)
		doc="$(osc ReadLoadBalancers)" || return 1
		printf '%s' "$doc" | jq -r --arg n "$2" '[.LoadBalancers[] | select(.LoadBalancerName == $n) | .PrivateIp // empty] | first // ""' ;;
	*) return 3 ;;
	esac
}

unlink_backend() { # provider balancer vm_id
	case "$1" in
	outscale) osc UnlinkLoadBalancerBackendMachines "{\"LoadBalancerName\":\"$2\",\"BackendVmIds\":[\"$3\"]}" >/dev/null ;;
	*) return 3 ;;
	esac
}

# ---- one stack --------------------------------------------------------------

WORK=""
UP=""
cleanup() {
	if [ -n "$UP" ] && [ -n "$WORK" ]; then
		(cd "$WORK" && "$FEINT" down >/dev/null 2>&1)
	fi
	[ -n "$WORK" ] && rm -rf "$WORK"
	WORK=""
	UP=""
}
trap cleanup EXIT INT TERM

# declare_or_fail reads one family out of the stack's declaration and refuses an
# absent one. A family written `{"skip": "<reason>"}` is honoured by the caller;
# a family with neither content nor reason is the silence this gate exists to
# remove. It writes to a global rather than to stdout for the subshell reason
# the transports carry.
DECLARED=""
declare_or_fail() { # stack proof family
	DECLARED="$(jq -c --arg f "$3" 'if has($f) then .[$f] else "MISSING" end' <"$2")"
	[ "$DECLARED" != '"MISSING"' ] \
		|| fail "$1: examples/stacks/$1/proof.json declares nothing about its $3 — a stack that says nothing about what its machines must do cannot be measured, and passing it would be the silence #503 was opened about"
}

skip_reason() { # json -> the reason, or nothing when this is not a skip
	printf '%s' "$1" | jq -r 'if type == "object" and has("skip") then .skip else "" end'
}

MACHINE=""
LISTEN=""
ADDRESS=""
# What the service family hands to the restart step, which runs last. Reset per
# stack: carried over, the second stack would restart a machine of the first.
RESTART_NAME=""
RESTART_UNIT=""
RESTART_PORT=""

run_stack() { # name
	local name="$1"
	RESTART_NAME=""
	RESTART_UNIT=""
	RESTART_PORT=""
	local src="$ROOT/examples/stacks/$name"
	[ -d "$src" ] || fail "no stack at $src"
	local proof="$src/proof.json"
	[ -f "$proof" ] \
		|| fail "$name: no examples/stacks/$name/proof.json — this gate asserts what a stack declares, and a stack that declares nothing is not measured by it"
	jq -e . <"$proof" >/dev/null 2>&1 || fail "$name: proof.json is not valid JSON"

	local provider kind floor running
	provider="$(jq -r '.provider // ""' <"$proof")"
	kind="$(jq -r '.machine_kind // ""' <"$proof")"
	floor="$(jq -r '.expect_machines // 0' <"$proof")"
	running="$(jq -r '.running_state // ""' <"$proof")"
	[ -n "$provider" ] && [ -n "$kind" ] && [ -n "$running" ] && [ "$floor" -gt 0 ] \
		|| fail "$name: proof.json must name provider, machine_kind, running_state and a non-zero expect_machines; without the floor, a reader that found nothing would read as a cloud that holds nothing"

	echo "- $name: feint up --runtime $RUNTIME"
	WORK="$(mktemp -d)"
	cp "$src"/*.tf "$src/feint.yaml" "$WORK/"
	[ -d "$src/modules" ] && cp -R "$src/modules" "$WORK/"
	sed -i "s|127.0.0.1:4599|$ADDR|g" "$WORK/feint.yaml"

	(cd "$WORK" && "$FEINT" up --runtime "$RUNTIME" --timeout 900s) >"$WORK/up.log" 2>&1 \
		|| { tail -n 40 "$WORK/up.log" >&2; fail "$name: feint up failed (log above)"; }
	UP="yes"
	ok "up, applied, every ready condition confirmed"

	local health
	health="$(curl -sf "$ENDPOINT/_feint/health")" || fail "$name: the emulator does not answer /_feint/health"
	[ "$(printf '%s' "$health" | jq -r '.machines // "none"')" != "none" ] \
		|| fail "$name: up was asked for --runtime $RUNTIME and health says no machine runtime; a gate that went on would measure nothing"

	curl -sf "$ENDPOINT/_feint/state" >"$WORK/state.json" || fail "$name: the emulator does not answer /_feint/state"
	incus list -f json >"$WORK/instances.json" 2>/dev/null \
		|| fail "cannot look: incus list failed; no witness because nobody could look is not 'no witness'"

	# need_machine, listen_of and address_of set globals rather than printing:
	# read through `$( )` their `fail` would exit the subshell alone, and the
	# caller would carry on with an empty string.
	need_machine() { # declared name
		MACHINE="$(fnl_machine_named "$kind" "$1" <"$WORK/state.json")"
		[ -n "$MACHINE" ] \
			|| fail "$name: proof.json names the machine '$1' and no $kind resource carries that name — the declaration and the stack have drifted apart"
	}
	listen_of() { # machine
		LISTEN="$WORK/listen-$1.txt"
		[ -f "$LISTEN" ] && return 0
		live_listeners "$1" "$LISTEN" \
			|| fail "cannot look: reading /proc/net/tcp inside $1 failed; every 'not listening' this run would report is an instrument failure"
	}
	address_of() { # declared_name machine
		ADDRESS="$(private_address "$2")" \
			|| fail "cannot look: reading the interfaces of $2 off the runtime failed"
		[ -n "$ADDRESS" ] \
			|| fail "$name: $1 carries no address on an emulated network, so nothing about the emulated network can be measured against it"
	}
	id_of() { # name
		jq -r --arg k "$kind" --arg n "$1" '[.resources[] | select(.Kind == $k) | select(((.Attrs.name // ((.Attrs.Tags // []) | map(select(.Key == "Name") | .Value) | first)) // "") == $n) | .ID] | first // ""' <"$WORK/state.json"
	}

	# ---- every machine started (#484 as a control) --------------------------
	echo "- $name: every machine the stack created is running, and running on the host"
	fnl_machines_of_kind "$kind" <"$WORK/state.json" >"$WORK/machines.tsv"
	fnl_every_machine_started "$name" "$WORK/machines.tsv" "$WORK/instances.json" "$floor" "$running"

	# address -> machine, for the balancer verdict below. A machine carrying no
	# emulated-network address is left out rather than failing the run: a server
	# whose only interface is the routed one is an ordinary shape (#202), and it
	# can never be a balancer backend.
	local rowmachine rowaddress
	: >"$WORK/addresses.tsv"
	while IFS=$'\t' read -r _ _ _ rowmachine; do
		[ -n "$rowmachine" ] || continue
		rowaddress="$(private_address "$rowmachine")" \
			|| fail "cannot look: reading the interfaces of $rowmachine off the runtime failed"
		[ -n "$rowaddress" ] || continue
		printf '%s\t%s\n' "$rowaddress" "$rowmachine" >>"$WORK/addresses.tsv"
	done <"$WORK/machines.tsv"

	# assert_restart is defined here and called at the very end of this stack's
	# run, after every other family, and the order is a measurement rather than
	# a taste: #549 measured a machine coming back from a poweroff/poweron
	# without the routes to its peered subnets, so anything probed after a
	# restart measured that loss instead of the property it names. The first
	# version of this file restarted a web machine before the firewall pair and
	# went red on the pair's positive half, which is how #549 was found.
	#
	# The order stays now that #549 is fixed, and it is not superstition: every
	# family above states a property of a machine as the stack built it, and a
	# restarted machine is a different measurement. That measurement is made
	# here, deliberately and by itself, with its own before-probe and its own
	# unrestarted control.
	assert_restart() { # declared_name unit port
		local restart="$1" unit="$2" port="$3" body address
		echo "- $name: the service survives a restart of the machine"
		need_machine "$restart"
		local rmachine="$MACHINE" id state waited status
		id="$(id_of "$restart")"
		[ -n "$id" ] || fail "$name: no resource id for $restart"

		# What must still be reachable afterwards, measured BEFORE anything is
		# restarted: the after-probe alone cannot tell a restart that lost its
		# routes from a pair that never held (#549).
		local reaches rtarget rport rcontrol rmachine_t raddr rbefore rafter rcontrol_after rcmachine
		reaches="$(printf '%s' "$service" | jq -c 'if has("restart_reaches") then .restart_reaches else "MISSING" end')"
		[ "$reaches" != '"MISSING"' ] \
			|| fail "$name: the service family declares a restart and nothing about what must still be reachable after it — that silence is where #549 lived, a machine coming back running, on its address, reaching its own subnet and nothing beyond it"
		rtarget=""
		reason="$(skip_reason "$reaches")"
		if [ -n "$reason" ]; then
			skip "$name service.restart_reaches: $reason"
		else
			rtarget="$(printf '%s' "$reaches" | jq -r '.target')"
			rport="$(printf '%s' "$reaches" | jq -r '.port')"
			# The control is the unrestarted machine that must go on reaching
			# the same target in the same pass — the half that told #549 apart
			# from a target that simply died. A stack with no such machine
			# writes the reason; absent, it is the silence this gate removes.
			local control
			control="$(printf '%s' "$reaches" | jq -c 'if has("control") then .control else "MISSING" end')"
			[ "$control" != '"MISSING"' ] \
				|| fail "$name: service.restart_reaches declares no control; a machine that stops reaching after a restart is told from a target that died by a neighbour that did not restart, or by a written reason there is none"
			rcontrol=""
			rcmachine=""
			reason="$(skip_reason "$control")"
			if [ -n "$reason" ]; then
				skip "$name service.restart_reaches.control: $reason"
			else
				rcontrol="$(printf '%s' "$control" | jq -r '.source')"
			fi
			need_machine "$rtarget"
			rmachine_t="$MACHINE"
			address_of "$rtarget" "$rmachine_t"
			raddr="$ADDRESS"
			listen_of "$rmachine_t"
			live_probe "$rmachine" "$raddr" "$rport"
			rbefore=$?
			if [ -n "$rcontrol" ]; then
				need_machine "$rcontrol"
				rcmachine="$MACHINE"
			fi
		fi

		# ---- the provider's own reboot verb, which used to restart nothing ---
		echo "- $name: the provider's own reboot verb restarts the machine"
		local pid_before pid_after
		pid_before="$(live_machine_pid "$rmachine")"
		power "$provider" "$id" reboot || fail "$name: $provider refused the reboot of $restart"
		waited=0
		while [ "$waited" -lt 240 ]; do
			state="$(resource_state "$provider" "$id")" || fail "cannot look: $provider's API did not answer the state of $restart"
			if [ "$state" = "$running" ] && incus exec "$rmachine" -- true >/dev/null 2>&1; then
				pid_after="$(live_machine_pid "$rmachine")"
				[ -n "$pid_after" ] && [ "$pid_after" != "$pid_before" ] && break
			fi
			sleep 4
			waited=$((waited + 4))
		done
		[ "$state" = "$running" ] \
			|| fail "$name: $restart came back '$state' ${waited}s after the API was asked to reboot it"
		pid_after="$(live_machine_pid "$rmachine")"
		fnl_restart_replaced_the_machine "$name" "$restart" "$rmachine" "$pid_before" "$pid_after"

		# ---- and the whole cycle through the other door ----------------------
		power "$provider" "$id" off || fail "$name: $provider refused the stop of $restart"
		waited=0
		state="$running"
		while [ "$waited" -lt 120 ]; do
			state="$(resource_state "$provider" "$id")" || fail "cannot look: $provider's API did not answer the state of $restart"
			[ "$state" != "$running" ] && break
			sleep 4
			waited=$((waited + 4))
		done
		[ "$state" != "$running" ] \
			|| fail "$name: $restart is still '$running' ${waited}s after the API was asked to stop it"
		incus list -f json >"$WORK/instances-stopped.json" 2>/dev/null \
			|| fail "cannot look: incus list failed while checking that the stop reached the runtime"
		status="$(witness_machine_status "$rmachine" <"$WORK/instances-stopped.json")"
		[ "$status" != "Running" ] \
			|| fail "$name: the API reports $restart '$state' and the host still holds machine $rmachine Running — the stop did not reach the runtime, so what follows would measure a machine that never went down (#547)"

		power "$provider" "$id" on || fail "$name: $provider refused the start of $restart"
		waited=0
		state=""
		while [ "$waited" -lt 240 ]; do
			state="$(resource_state "$provider" "$id")" || fail "cannot look: $provider's API did not answer the state of $restart"
			if [ "$state" = "$running" ] && incus exec "$rmachine" -- true >/dev/null 2>&1; then break; fi
			sleep 4
			waited=$((waited + 4))
		done
		[ "$state" = "$running" ] \
			|| fail "$name: $restart came back '$state' ${waited}s after the API was asked to start it — a stack whose machine does not restart is not a stack that works"
		# The unit needs its own boot to come up, so the wait is on the port
		# rather than on a fixed sleep, and the verdict below names how long
		# it waited.
		rm -f "$WORK/listen-$rmachine.txt"
		waited=0
		while [ "$waited" -lt 90 ]; do
			live_listeners "$rmachine" "$WORK/listen-$rmachine.txt" \
				|| fail "cannot look: reading /proc/net/tcp inside $rmachine failed after its restart"
			fnl_listens "$port" "$WORK/listen-$rmachine.txt" && break
			sleep 5
			waited=$((waited + 5))
		done
		fnl_service_listens "$name" "$restart ($unit, ${waited}s after a restart through the API)" "$rmachine" "$port" "$WORK/listen-$rmachine.txt"
		address="$(published_address "$provider" "$restart")" \
			|| fail "cannot look: $provider's API did not answer when asked for $restart's published address"
		if [ -n "$address" ]; then
			body="$(station_fetch "$address" "$port")"
			fnl_service_answers "$name" "$restart (after a restart)" "$rmachine" "$address" "$port" "$body"
		fi

		# ---- what the restarts must not have cost (#549) ---------------------
		if [ -n "$rtarget" ]; then
			echo "- $name: the restarted machine still reaches the subnet it reached before"
			rm -f "$WORK/listen-$rmachine_t.txt"
			listen_of "$rmachine_t"
			live_probe "$rmachine" "$raddr" "$rport"
			rafter=$?
			rcontrol_after=1
			if [ -n "$rcmachine" ]; then
				live_probe "$rcmachine" "$raddr" "$rport"
				rcontrol_after=$?
			fi
			fnl_restart_keeps_reaching "$name" "$restart" "$rtarget" "$raddr" "$rport" \
				"$LISTEN" "$rbefore" "$rafter" "$rcontrol" "$rcontrol_after"
		fi
	}

	# ---- service ------------------------------------------------------------
	local service reason
	declare_or_fail "$name" "$proof" service
	service="$DECLARED"
	reason="$(skip_reason "$service")"
	if [ -n "$reason" ]; then
		skip "$name service: $reason"
	else
		local port unit restart target body address smachine
		port="$(printf '%s' "$service" | jq -r '.port')"
		unit="$(printf '%s' "$service" | jq -r '.unit')"
		restart="$(printf '%s' "$service" | jq -r '.restart // ""')"
		echo "- $name: the declared service listens inside, and answers over the published address"
		if [ "$(printf '%s' "$health" | jq -r '.capabilities.firewall_public_only // false')" != "true" ]; then
			skip "$name: this runtime declares capabilities.firewall_public_only false (#337), so an address published on a routed NIC carries no rule set (#548). The station leg below therefore proves the service is reachable and asserts nothing about the firewall; the firewall pair runs on the emulated network."
		fi
		for target in $(printf '%s' "$service" | jq -r '.machines[]'); do
			need_machine "$target"
			smachine="$MACHINE"
			listen_of "$smachine"
			fnl_service_listens "$name" "$target ($unit)" "$smachine" "$port" "$LISTEN"
			address="$(published_address "$provider" "$target")" \
				|| fail "cannot look: $provider's API did not answer when asked for $target's published address"
			if [ -z "$address" ]; then
				skip "$name: $target publishes no public address, so the station leg has nothing to travel; its service was proved listening inside"
				continue
			fi
			body="$(station_fetch "$address" "$port")"
			fnl_service_answers "$name" "$target" "$smachine" "$address" "$port" "$body"
		done
		# Handed to the end of the run rather than asserted here: see
		# assert_restart above for why the order is a measurement (#549).
		RESTART_NAME="$restart"
		RESTART_UNIT="$unit"
		RESTART_PORT="$port"
	fi

	# ---- firewall -----------------------------------------------------------
	local firewall
	declare_or_fail "$name" "$proof" firewall
	firewall="$DECLARED"
	reason="$(skip_reason "$firewall")"
	echo "- $name: a port a rule opens answers, a port no rule opens refuses"
	if [ -n "$reason" ]; then
		skip "$name firewall: $reason"
	elif [ "$(printf '%s' "$health" | jq -r '.capabilities.firewall // false')" != "true" ]; then
		skip "$name: this runtime does not declare capabilities.firewall; nothing was promised here, nothing is demanded"
	elif ! printf '%s' "$health" | witness_enforced "$provider" firewall; then
		skip "$name: $provider does not declare enforced.firewall — a property it never promised is not demanded of it (#481)"
	else
		local source target open closed control smachine tmachine taddr code
		source="$(printf '%s' "$firewall" | jq -r '.source')"
		target="$(printf '%s' "$firewall" | jq -r '.target')"
		open="$(printf '%s' "$firewall" | jq -r '.open_port')"
		closed="$(printf '%s' "$firewall" | jq -r '.closed_port')"
		# control_source is one thing and only one: a source the target's rule
		# does NOT name, which must be refused on the very port the named source
		# reaches. A stack that has no such machine writes a reason and this run
		# says it out loud — the first version of this file took a neighbour of
		# the target's own subnet for that role, and the documented same-subnet
		# bound lets a neighbour through whatever the group says, so the control
		# was asserting the opposite of what it measured.
		control="$(printf '%s' "$firewall" | jq -c 'if has("control_source") then .control_source else "MISSING" end')"
		[ "$control" != '"MISSING"' ] \
			|| fail "$name: the firewall family declares no control_source; a rule that filters by source needs a source it refuses, or write the reason there is none"
		need_machine "$source"
		smachine="$MACHINE"
		need_machine "$target"
		tmachine="$MACHINE"
		address_of "$target" "$tmachine"
		taddr="$ADDRESS"
		listen_of "$tmachine"
		fnl_firewall_pair "$name" "$source" "$smachine" "$target" "$taddr" "$open" "$closed" "$LISTEN" live_probe
		reason="$(skip_reason "$control")"
		if [ -n "$reason" ]; then
			skip "$name firewall.control_source: $reason"
		else
			# The same open port, from a source the rule does not name, refused:
			# it tells a rule that filters by source apart from one that opens a
			# port to the world.
			local csource
			csource="$(printf '%s' "$control" | jq -r '.source')"
			need_machine "$csource"
			live_probe "$MACHINE" "$taddr" "$open"
			code=$?
			case "$code" in
			2) fail "cannot look: the control probe from $csource towards $taddr:$open could not be made at all" ;;
			0) fail "$name: $csource reached $target at $taddr:$open, and no rule of $target's group names its block — the group's source restriction is not enforced" ;;
			esac
			ok "$name: $csource is refused on $open, which $source reaches — the rule filters by source"
		fi
	fi

	# ---- network ------------------------------------------------------------
	local network reaches isolated
	declare_or_fail "$name" "$proof" network
	network="$DECLARED"
	reason="$(skip_reason "$network")"
	if [ -n "$reason" ]; then
		skip "$name network: $reason"
	else
		reaches="$(printf '%s' "$network" | jq -c 'if has("reaches") then .reaches else "MISSING" end')"
		isolated="$(printf '%s' "$network" | jq -c 'if has("isolated") then .isolated else "MISSING" end')"
		{ [ "$reaches" != '"MISSING"' ] && [ "$isolated" != '"MISSING"' ]; } \
			|| fail "$name: the network family must declare both halves, reaches and isolated — one of them alone measures the reader rather than the network"

		echo "- $name: two machines of one network reach each other"
		reason="$(skip_reason "$reaches")"
		if [ -n "$reason" ]; then
			skip "$name network.reaches: $reason"
		else
			local rsrc rdst rport rsm rdm raddr
			rsrc="$(printf '%s' "$reaches" | jq -r '.source')"
			rdst="$(printf '%s' "$reaches" | jq -r '.target')"
			rport="$(printf '%s' "$reaches" | jq -r '.port')"
			need_machine "$rsrc"
			rsm="$MACHINE"
			need_machine "$rdst"
			rdm="$MACHINE"
			address_of "$rdst" "$rdm"
			raddr="$ADDRESS"
			listen_of "$rdm"
			fnl_reaches "$name" "$rsrc" "$rsm" "$rdst" "$raddr" "$rport" "$LISTEN" live_probe
		fi

		echo "- $name: two networks nothing peers do not reach each other"
		reason="$(skip_reason "$isolated")"
		if [ -n "$reason" ]; then
			skip "$name network.isolated: $reason"
		elif [ "$(printf '%s' "$health" | jq -r '.capabilities.isolation // false')" != "true" ]; then
			skip "$name: this runtime does not declare capabilities.isolation (docs/limits.md: two managed bridges of one host are routed together) — asserting a separation nobody promised is what this gate exists to avoid"
		else
			local isrc idst iport ism idm iaddr
			isrc="$(printf '%s' "$isolated" | jq -r '.source')"
			idst="$(printf '%s' "$isolated" | jq -r '.target')"
			iport="$(printf '%s' "$isolated" | jq -r '.port')"
			need_machine "$isrc"
			ism="$MACHINE"
			need_machine "$idst"
			idm="$MACHINE"
			address_of "$idst" "$idm"
			iaddr="$ADDRESS"
			listen_of "$idm"
			fnl_isolated "$name" "$isrc" "$ism" "$idst" "$iaddr" "$iport" "$LISTEN" live_probe
		fi
	fi

	# ---- balancer -----------------------------------------------------------
	local balancer
	declare_or_fail "$name" "$proof" balancer
	balancer="$DECLARED"
	reason="$(skip_reason "$balancer")"
	echo "- $name: the balancer hands connections to the backends the pack delivered"
	if [ -n "$reason" ]; then
		skip "$name balancer: $reason"
	elif [ "$(printf '%s' "$health" | jq -r '.capabilities.balancing // false')" != "true" ]; then
		skip "$name: this runtime does not declare capabilities.balancing; a balancer here records its configuration and that is the documented degraded mode"
	elif ! printf '%s' "$health" | witness_enforced "$provider" balancing; then
		skip "$name: $provider does not declare enforced.balancing — a property it never promised is not demanded of it (#481)"
	else
		local bname bport bclient bprobes bmachine baddr distributed undistributed i
		bname="$(printf '%s' "$balancer" | jq -r '.name')"
		bport="$(printf '%s' "$balancer" | jq -r '.port')"
		bclient="$(printf '%s' "$balancer" | jq -r '.client')"
		bprobes="$(printf '%s' "$balancer" | jq -r '.probes // 6')"
		need_machine "$bclient"
		bmachine="$MACHINE"
		baddr="$(balancer_address "$provider" "$bname")" \
			|| fail "cannot look: $provider's API did not answer when asked for balancer $bname"
		[ -n "$baddr" ] \
			|| fail "$name: the API publishes no private address for balancer $bname; nothing can be probed, and the public face of an internet-facing balancer is refused on purpose (#315)"
		distributed="$(jq -r --arg n "$bname" '[.resources[] | select(.ID == $n or ((.Attrs.LoadBalancerName // "") == $n)) | .Runtime["balancer-distributed"] // ""] | first // ""' <"$WORK/state.json")"
		undistributed="$(jq -r --arg n "$bname" '[.resources[] | select(.ID == $n or ((.Attrs.LoadBalancerName // "") == $n)) | .Runtime["balancer-undistributed"] // ""] | first // ""' <"$WORK/state.json")"

		# One line per probe, always: a fetch that answers nothing prints
		# nothing, and appending its output alone would make a dropped
		# connection vanish from the count instead of being reported.
		: >"$WORK/hits.txt"
		i=0
		while [ "$i" -lt "$bprobes" ]; do
			printf '%s\n' "$(live_fetch "$bmachine" "$baddr" "$bport")" >>"$WORK/hits.txt"
			i=$((i + 1))
		done
		fnl_balancer_delivers "$name" "$bname" "$WORK/hits.txt" "$distributed" "$undistributed" "$WORK/addresses.tsv"

		echo "- $name: unregistering a backend is visible from outside"
		local withdrawn_addr withdrawn_machine withdrawn_id remaining
		withdrawn_addr="$(fnl_delivery_addresses "$distributed" | head -n1)"
		withdrawn_machine="$(awk -F'\t' -v a="$withdrawn_addr" '$1 == a { print $2 }' "$WORK/addresses.tsv")"
		[ -n "$withdrawn_machine" ] || fail "$name: no machine on the host carries $withdrawn_addr; the withdrawal cannot be aimed"
		remaining=$(($(fnl_delivery_addresses "$distributed" | wc -l) - 1))
		withdrawn_id="$(jq -r --arg m "$withdrawn_machine" '[.resources[] | select((.Runtime.machine // "") == $m) | .ID] | first // ""' <"$WORK/state.json")"
		[ -n "$withdrawn_id" ] || fail "$name: no resource carries machine $withdrawn_machine; the withdrawal cannot be sent"
		unlink_backend "$provider" "$bname" "$withdrawn_id" \
			|| fail "$name: $provider refused to unregister $withdrawn_id from $bname"
		sleep 5
		: >"$WORK/hits-after.txt"
		i=0
		while [ "$i" -lt "$bprobes" ]; do
			printf '%s\n' "$(live_fetch "$bmachine" "$baddr" "$bport")" >>"$WORK/hits-after.txt"
			i=$((i + 1))
		done
		fnl_backend_withdrawn "$name" "$bname" "$withdrawn_machine" "$WORK/hits-after.txt" "$remaining"
	fi

	# ---- the restart, last ---------------------------------------------------
	if [ -n "${RESTART_NAME:-}" ]; then
		assert_restart "$RESTART_NAME" "$RESTART_UNIT" "$RESTART_PORT"
	fi

	# ---- the rule sets, after the restarts -----------------------------------
	#
	# After, and that is the measurement: a rule set lives on a NIC, a restart
	# re-plugs NICs, and a machine that came back without its set is a machine
	# the API describes as filtered and the host does not filter. The counts are
	# the stack's own, so a number that moves fails a gate instead of waiting to
	# be noticed by somebody reading `incus network acl list`.
	local rulesets prefix wantsets wantrefs aclmachine
	declare_or_fail "$name" "$proof" rule_sets
	rulesets="$DECLARED"
	reason="$(skip_reason "$rulesets")"
	echo "- $name: the runtime holds the rule sets this stack declares"
	if [ -n "$reason" ]; then
		skip "$name rule_sets: $reason"
	else
		prefix="$(printf '%s' "$rulesets" | jq -r '.prefix')"
		wantsets="$(printf '%s' "$rulesets" | jq -r '.sets')"
		wantrefs="$(printf '%s' "$rulesets" | jq -r '.references')"
		: >"$WORK/rulesets.tsv"
		while IFS=$'\t' read -r _ _ _ aclmachine; do
			[ -n "$aclmachine" ] || continue
			printf '%s\t%s\n' "$aclmachine" "$(live_machine_acls "$aclmachine")" >>"$WORK/rulesets.tsv" \
				|| fail "cannot look: reading the rule sets of $aclmachine off the runtime failed"
		done <"$WORK/machines.tsv"
		fnl_rule_sets "$name" "$prefix" "$WORK/rulesets.tsv" "$wantsets" "$wantrefs"
	fi

	# ---- down ---------------------------------------------------------------
	echo "- $name: feint down"
	(cd "$WORK" && "$FEINT" down) >"$WORK/down.log" 2>&1 \
		|| { tail -n 20 "$WORK/down.log" >&2; fail "$name: feint down failed"; }
	UP=""
	if curl -sf --max-time 2 "$ENDPOINT/_feint/health" >/dev/null 2>&1; then
		fail "$name: down returned and something still answers on $ADDR"
	fi
	ok "down, nothing answers on $ADDR"
	rm -rf "$WORK"
	WORK=""
}

# The population, on a line of its own so a test can read it: adding a stack
# here without declaring what its machines must prove fails in milliseconds
# (TestEveryStackTheGateNamesDeclaresWhatItMustProve) instead of at the first
# `feint up`, minutes in.
DEFAULT_STACKS=(scaleway outscale)
STACKS=("$@")
[ "${#STACKS[@]}" -gt 0 ] || STACKS=("${DEFAULT_STACKS[@]}")
for stack in "${STACKS[@]}"; do
	run_stack "$stack"
done

# The host this run found is the host it leaves. A run that measured every
# assertion and left a container behind has broken the next run rather than this
# one (#493), so the doorstep question is asked again on the way out.
guard_leftovers_for "$RUNTIME" "the end of the run"

echo "conformance: every stack proved what it declares, and every bound it cannot measure was named"
