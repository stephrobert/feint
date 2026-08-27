#!/usr/bin/env bash
# What "the stack works" means, as verdicts (#503).
#
# On 2026-08-26 four defects were found by hand on the example stacks — #475,
# #481, #483, #484 — and all four were green: apply exit 0, second plan empty,
# clean destroy, three of them without a single ERROR line. Nothing failed, so
# nothing could catch them except a person who opened a session on the host and
# looked. These functions are that person, for the half `witness.sh` does not
# cover.
#
# The division of labour between the two gates, because they look at the same
# stacks and must not be confused:
#
#   witness.sh    does the OBJECT exist on the runtime — a machine behind a
#                 `running` resource, a rule set on a NIC, a balancer on a
#                 network. It reads `incus`.
#   functional.sh does a PACKET arrive — a service that listens and answers, a
#                 port a rule opens and one it does not, a neighbour reachable
#                 and a foreign network not, a balancer that hands connections
#                 to the backends the pack says it delivered.
#
# An object can be perfectly present and carry nothing, which is exactly what
# #483 was.
#
# ---------------------------------------------------------------------------
# The design point that decides everything: by which door the machine is
# reached.
#
# A harness that runs `incus exec <target> -- curl localhost` talks to the
# machine THROUGH THE HOST, bypassing the network, the firewall and the
# published address. It would have found zero of the four defects above. So
# every verdict here is drawn from a two-legged trip:
#
#   1. from INSIDE the target, prove the service listens — /proc/net/tcp, the
#      port in hexadecimal, state 0A. Not `ss`, which the Alpine images do not
#      carry and whose absence has already been read here as "nothing is
#      listening"; not `nc`, for the same reason one layer up. /proc is in
#      every one of these images because it is the kernel.
#   2. only then, probe from where a user stands: another machine, or the
#      station, over the address the provider's own API publishes.
#
# Without step 1 a refusal cannot be told from a dead service. With it, a
# refusal can only be the network or the rule. Every verdict below that reads
# a refusal REQUIRES its listen-proof as an argument and fails without it —
# the discipline lives in the verdict rather than in the caller's goodwill,
# because the caller is what forgets.
#
# `incus exec` still appears, and only ever as the CONSOLE that originates a
# probe from inside a machine. The packet it sends then crosses the emulated
# network and the rule sets exactly as any other. What is forbidden is using
# that console to ask the target whether it is well.
#
# ---------------------------------------------------------------------------
# Three disciplines, each already paid for on this repository:
#
#   1. Every negative verdict carries its positive control in the same pass. A
#      port a rule opens must answer, or a closed port measures the reader
#      rather than the rule. Three readers lied here in one week.
#   2. Three outcomes, never two. A probe answers open, refused, or "nobody
#      could look" — a missing `bash` inside a machine, an `incus exec` that
#      failed. The first exploratory run for this file probed with
#      `sh -c 'echo >/dev/tcp/…'`, and Ubuntu's /bin/sh is dash, which has no
#      /dev/tcp: ninety probes read "closed", including every one that was
#      open. A harness measuring its own breakage.
#   3. Every reader proves it can find before it judges, on a planted witness
#      AND on a near miss it must not find.
#
# Sourced by tools/conformance/functional.sh and driven, with stub transports
# and planted files, by tools/conformance/functional_test.go. Every function
# expects `ok`, `fail` and `skip` to be defined by the caller.

# witnesslib.sh carries `witness_machine_status`, and this file uses it rather
# than writing a second instance reader: a verdict written twice is a verdict
# fixed in one of them, which CLAUDE.md names as the shape that cost the most
# here. Sourced from beside this file so the library is self-contained.
# shellcheck source=/dev/null
. "$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/witnesslib.sh"

# ---- readers: filters over stdin, no side effects ---------------------------

# fnl_listening_ports reads /proc/net/tcp and /proc/net/tcp6 concatenated on
# stdin and prints, one per line, the decimal ports in state 0A (LISTEN).
#
# State 0A and nothing else. An established connection (01) on port 40000 is
# not a listener, and a reader that returned it would report a machine as
# serving a port it merely dialled from.
fnl_listening_ports() {
	local hex
	awk '$4 == "0A" { split($2, a, ":"); print a[2] }' \
		| sort -u \
		| while IFS= read -r hex; do
			case "$hex" in
			[0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f][0-9A-Fa-f]) printf '%d\n' "$((16#$hex))" ;;
			esac
		done \
		| sort -n -u
}

# fnl_listens answers 0 when the port is in the listen file the caller
# captured, 1 otherwise. A file, never a live call: the capture is the caller's
# so that a transport failure is its own verdict and never an empty set.
fnl_listens() { # port listen_file
	grep -qx -- "$1" "$2"
}

# fnl_machine_named reads /_feint/state on stdin and prints the machine the
# runtime holds for the resource of that kind carrying that name, or nothing.
#
# The name is read from the two places the packs put it, because that is the
# one provider-shaped half of the question: Scaleway writes `Attrs.name`,
# Outscale a `Name` tag. A fourth pack adds its clause here rather than getting
# a reader of its own — what varies is a field, what does not is shared.
fnl_machine_named() { # kind name
	jq -r --arg k "$1" --arg n "$2" '
		[ .resources[]
		  | select(.Kind == $k)
		  | select(((.Attrs.name // ((.Attrs.Tags // []) | map(select(.Key == "Name") | .Value) | first)) // "") == $n)
		  | .Runtime.machine // "" ] | first // ""'
}

# fnl_machines_of_kind reads /_feint/state on stdin and prints one line per
# resource of that kind: `<name>\t<id>\t<state>\t<machine>`.
fnl_machines_of_kind() { # kind
	jq -r --arg k "$1" '
		.resources[]
		| select(.Kind == $k)
		| [ ((.Attrs.name // ((.Attrs.Tags // []) | map(select(.Key == "Name") | .Value) | first)) // ""),
		    .ID, (.State // ""), (.Runtime.machine // "") ] | @tsv'
}

# fnl_delivery_addresses prints the addresses of a balancer delivery record.
#
# `balancer-distributed` is comma-joined addresses; `balancer-undistributed` is
# `address (reason); address (reason)` — machine.BalancerDelivery.Lines writes
# both. The reason is prose and must not be mistaken for an address, which is
# why the first token of each part is taken rather than the part.
fnl_delivery_addresses() { # record
	printf '%s' "$1" | awk -F'[,;]' '{
		for (i = 1; i <= NF; i++) { split($i, part, " "); if (part[1] != "") print part[1] }
	}'
}

# ---- the controls: each reader finds a planted witness, and only it ---------
#
# Run before any verdict. A reader that cannot find its planted witness voids
# every absence it would report, so a failed control is a FAIL about the
# instrument, in as many words.

fnl_listen_reader_control() {
	local out
	# 0016 is 22 and 1F90 is 8080, both listening; 8AE1 (35553) is an
	# established connection and must not be reported as a listener.
	out="$(printf '%s\n' \
		'  sl  local_address rem_address   st tx_queue rx_queue tr tm->when retrnsmt   uid' \
		'   0: 00000000:0016 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0' \
		'   1: 0100007F:1F90 00000000:0000 0A 00000000:00000000 00:00000000 00000000     0' \
		'   2: 0100007F:8AE1 0100007F:1F90 01 00000000:00000000 00:00000000 00000000     0' \
		| fnl_listening_ports | tr '\n' ' ')"
	[ "$out" = "22 8080 " ] \
		|| fail "the listen reader cannot read /proc/net/tcp (got '$out', wanted '22 8080 '); every 'nothing is listening' it reported would be an instrument failure, not a measurement"
	out="$(printf '%s\n' '   0: 00000000:0016 00000000:0000 01 00000000:00000000 00:00000000 00000000 0' \
		| fnl_listening_ports | tr '\n' ' ')"
	[ -z "$out" ] \
		|| fail "the listen reader counted an established connection as a listener (got '$out'); a machine that dialled a port would read as serving it"
	ok "the listen reader finds a planted listener, and refuses an established connection"
}

fnl_delivery_reader_control() {
	local out
	out="$(fnl_delivery_addresses "10.50.1.4,10.50.1.7" | tr '\n' ' ')"
	[ "$out" = "10.50.1.4 10.50.1.7 " ] \
		|| fail "the delivery reader cannot read a distributed record (got '$out'); a balancer verdict drawn from it would be void"
	# The near miss: a reason is prose, and its words are not addresses.
	out="$(fnl_delivery_addresses "10.50.3.4 (outside fnt-abc's own block 10.50.1.0/24 (#457)); 10.50.3.9 (same)" | tr '\n' ' ')"
	[ "$out" = "10.50.3.4 10.50.3.9 " ] \
		|| fail "the delivery reader read the withheld record's prose as addresses (got '$out'); a backend would be excused or accused by a word of English"
	ok "the delivery reader reads both records, and no prose as an address"
}

fnl_name_reader_control() {
	local out
	out="$(printf '%s' '{"resources":[
		{"Kind":"vm","ID":"i-1","Attrs":{"Tags":[{"Key":"Name","Value":"platform-app"}]},"Runtime":{"machine":"feint-osc-i-1"}},
		{"Kind":"vm","ID":"i-2","Attrs":{"Tags":[{"Key":"Name","Value":"platform-app-2"}]},"Runtime":{"machine":"feint-osc-i-2"}},
		{"Kind":"instance/server","ID":"s-1","Attrs":{"name":"platform-web-0"},"Runtime":{"machine":"feint-scw-s-1"}}]}' \
		| fnl_machine_named vm platform-app)"
	[ "$out" = "feint-osc-i-1" ] \
		|| fail "the name reader cannot find a machine by its provider's own naming (got '$out'); every machine it failed to resolve would read as a stack that created nothing"
	out="$(printf '%s' '{"resources":[{"Kind":"vm","ID":"i-2","Attrs":{"Tags":[{"Key":"Name","Value":"platform-app-2"}]},"Runtime":{"machine":"feint-osc-i-2"}}]}' \
		| fnl_machine_named vm platform-app)"
	[ -z "$out" ] \
		|| fail "the name reader matched 'platform-app' inside 'platform-app-2' (got '$out'); a neighbouring machine would answer for the one under test"
	ok "the name reader resolves both naming shapes, and refuses a prefix match"
}

# ---- the verdicts -----------------------------------------------------------

# fnl_every_machine_started is the maintainer's own line, as a control: a stack
# whose machine does not start is red even though `terraform apply` returned 0.
# That is #484 reproduced.
#
# It is deliberately wider than witness.sh's running verdict, which judges only
# the resources the API already calls running. A machine whose start failed
# leaves a resource the pack correctly publishes as stopped, so that gate has
# nothing to say about it and is right not to. Here the population is every
# machine-kind resource the stack created, and `stopped` is a failure of the
# stack, named.
#
# machines_file: `<name>\t<id>\t<state>\t<machine>`, one per resource.
# instances_json: the file `incus list -f json` was captured into.
# floor: the count the stack's own declaration promises.
fnl_every_machine_started() { # stack machines_file instances_json floor running_state
	local stack="$1" machines="$2" instances="$3" floor="$4" running="$5"
	local name id state machine status count=0

	count="$(grep -c . "$machines" || true)"
	if [ "${count:-0}" -lt "$floor" ]; then
		fail "$stack: the machine reader found ${count:-0} machine resource(s) where this stack's own declaration promises $floor — the reader is the suspect, not the cloud"
	fi

	while IFS=$'\t' read -r name id state machine; do
		[ -n "$id" ] || continue
		if [ "$state" != "$running" ]; then
			fail "$stack: machine $name ($id) is '$state', not '$running', after an apply that returned 0 — a stack whose machine does not start is not a stack that works (#484)"
		fi
		if [ -z "$machine" ]; then
			fail "$stack: machine $name ($id) is '$running' and recorded no runtime machine — a state with nothing behind it (#484)"
		fi
		status="$(witness_machine_status "$machine" <"$instances")"
		if [ "$status" = "absent" ]; then
			fail "$stack: machine $name ($id) is '$running' and no machine $machine exists on the runtime (#484)"
		fi
		if [ "$status" != "Running" ]; then
			fail "$stack: machine $name ($id) is '$running' and machine $machine is $status on the runtime (#484)"
		fi
	done <"$machines"
	ok "$stack: $count machine(s), each '$running' and each Running on the host"
}

# fnl_service_listens is step 1 of the two-legged trip, and the precondition of
# every refusal this file reads afterwards.
fnl_service_listens() { # stack name machine port listen_file
	local stack="$1" name="$2" machine="$3" port="$4" listen="$5"
	if ! fnl_listens "$port" "$listen"; then
		fail "$stack: $name ($machine) is not listening on port $port inside the machine (listening: $(tr '\n' ' ' <"$listen")) — the service the stack declares is not running, and every refusal measured from outside would be measuring that instead"
	fi
	ok "$stack: $name listens on $port inside the machine"
}

# fnl_service_answers is step 2: the same service, over the address the
# provider's own API publishes, fetched rather than merely connected to.
#
# The body must name the machine. A connection that opens proves a route; a
# body that names the machine proves which machine is behind it, and that is
# what a balancer verdict needs later and what a fixed string could never give.
fnl_service_answers() { # stack name machine address port body
	local stack="$1" name="$2" machine="$3" address="$4" port="$5" body="$6"
	if [ -z "$body" ]; then
		fail "$stack: $name ($machine) listens on $port inside, and nothing answers at $address:$port from the station — the service is alive, so this is the network or the address the API published"
	fi
	if [ "$body" != "$machine" ]; then
		fail "$stack: $address:$port answered '$body' where machine $machine was expected — the published address of $name reaches another machine"
	fi
	ok "$stack: $name answers at $address:$port, and names itself"
}

# fnl_firewall_pair is the two halves in one pass, and it refuses to draw
# either verdict until both ports are proved listening inside the target.
#
# The source is named twice, and the pair is not redundant: `source` is what the
# stack calls the machine and what every message must carry, `from` is the
# runtime's name for it, which is what the probe is run on. Collapsing them was
# this file's own first defect — the declared name reached `incus exec`, every
# probe answered "nobody could look", and the run failed for the right reason by
# accident rather than measuring anything.
#
# probe is injected and answers three ways: 0 open, 1 refused, 2 nobody could
# look. The positive half is measured first on purpose: on a path that is broken
# end to end, a suite that read the closed port first would report a firewall
# doing its job.
fnl_firewall_pair() { # stack source from target address open closed listen_file probe
	local stack="$1" source="$2" from="$3" target="$4" address="$5" open="$6" closed="$7" listen="$8" probe="$9"
	local code

	fnl_listens "$open" "$listen" \
		|| fail "$stack: $target does not listen on $open inside the machine (listening: $(tr '\n' ' ' <"$listen")); the open half of the firewall pair would measure a dead service"
	fnl_listens "$closed" "$listen" \
		|| fail "$stack: $target does not listen on $closed inside the machine (listening: $(tr '\n' ' ' <"$listen")); a refusal on that port could not be told from a dead service, which is the whole reason this pair exists"

	"$probe" "$from" "$address" "$open"
	code=$?
	case "$code" in
	2) fail "cannot look: the probe from $source ($from) towards $address:$open could not be made at all; no measurement is not a measurement" ;;
	1) fail "$stack: a rule of $target's group opens $open and $source does not reach $address:$open — the group describes a port the host does not open" ;;
	esac

	"$probe" "$from" "$address" "$closed"
	code=$?
	case "$code" in
	2) fail "cannot look: the probe from $source ($from) towards $address:$closed could not be made at all; no measurement is not a measurement" ;;
	0) fail "$stack: no rule of $target's group opens $closed, $target is listening on it, and $source reached $address:$closed — the group is not enforced on this path" ;;
	esac
	ok "$stack: $source reaches $target on $open and is refused on $closed, both listening inside"
}

# fnl_reaches: two machines of one emulated network reach each other.
fnl_reaches() { # stack source from target address port listen_file probe
	local stack="$1" source="$2" from="$3" target="$4" address="$5" port="$6" listen="$7" probe="$8"
	local code
	fnl_listens "$port" "$listen" \
		|| fail "$stack: $target does not listen on $port inside the machine (listening: $(tr '\n' ' ' <"$listen")); 'reachable' would be measuring the listener, not the network"
	"$probe" "$from" "$address" "$port"
	code=$?
	case "$code" in
	2) fail "cannot look: the probe from $source ($from) towards $address:$port could not be made at all" ;;
	1) fail "$stack: $source does not reach $target at $address:$port, on a network they share and a port $target is listening on — the emulated network carries nothing between two of its own machines" ;;
	esac
	ok "$stack: $source reaches $target at $address:$port"
}

# fnl_isolated: two networks nothing peers do not reach each other.
#
# The listen proof is what separates this verdict from silence. A machine that
# never booted refuses exactly as an isolated one does, and this suite has
# already read the first as a pass on the assertion that carries the product's
# strongest claim (#219).
fnl_isolated() { # stack source from target address port listen_file probe
	local stack="$1" source="$2" from="$3" target="$4" address="$5" port="$6" listen="$7" probe="$8"
	local code
	fnl_listens "$port" "$listen" \
		|| fail "$stack: $target does not listen on $port inside the machine (listening: $(tr '\n' ' ' <"$listen")); 'unreachable' would be measuring a dead machine rather than isolation"
	"$probe" "$from" "$address" "$port"
	code=$?
	case "$code" in
	2) fail "cannot look: the probe from $source ($from) towards $address:$port could not be made at all" ;;
	0) fail "$stack: $source reached $target at $address:$port across two networks nothing peers, and this runtime declares isolation — the separation the product is sold on is not there" ;;
	esac
	ok "$stack: $source cannot reach $target at $address:$port, which is listening"
}

# fnl_balancer_delivers: a balancer hands connections to the backends the pack
# recorded as delivered, and to no backend it recorded as withheld.
#
# The population is the pack's own delivery record rather than a number written
# here, and that is deliberate: under OVN the runtime distributes only to
# backends of the balancer's own subnet (#457), so a declaration naming "two
# backends" would be wrong on the stacks that exercise that bound and would go
# stale the day it moves. Reading the record makes the verdict strengthen by
# itself.
#
# hits_file: one line per probe, the body the balancer answered — a machine
# name, or empty when nothing answered.
# machines_file: `<address>\t<machine>` for every machine on the host.
fnl_balancer_delivers() { # stack name hits_file distributed undistributed machines_file
	local stack="$1" name="$2" hits="$3" distributed="$4" undistributed="$5" machines="$6"
	local address machine hit want="" deny="" seen="" probes=0 answered=0

	for address in $(fnl_delivery_addresses "$distributed"); do
		machine="$(awk -F'\t' -v a="$address" '$1 == a { print $2 }' "$machines")"
		[ -n "$machine" ] \
			|| fail "$stack: the pack recorded $address as a distributed backend of $name and no machine on the host carries it — the record and the runtime disagree about who the balancer serves"
		want="$want $machine"
	done
	for address in $(fnl_delivery_addresses "$undistributed"); do
		machine="$(awk -F'\t' -v a="$address" '$1 == a { print $2 }' "$machines")"
		[ -n "$machine" ] || continue
		deny="$deny $machine"
	done
	[ -n "$want" ] \
		|| fail "$stack: $name is registered with a listener and a backend, and the pack recorded no distributed backend at all — a balancer that hands connections to nobody (#483)"

	while IFS= read -r hit; do
		probes=$((probes + 1))
		if [ -z "$hit" ]; then
			continue
		fi
		answered=$((answered + 1))
		case " $deny " in
		*" $hit "*) fail "$stack: $name answered from $hit, a backend the runtime recorded as withheld — the emulator's own account of what it delivered is wrong" ;;
		esac
		case " $want " in
		*" $hit "*) ;;
		*) fail "$stack: $name answered from $hit, which is none of the backends the pack recorded as distributed ($want) — something other than the balancer's own backends is serving this address" ;;
		esac
		case " $seen " in
		*" $hit "*) ;;
		*) seen="$seen $hit" ;;
		esac
	done <"$hits"

	[ "$probes" -gt 0 ] \
		|| fail "$stack: $name was probed zero times; a balancer verdict that compared nothing is not a verdict"
	[ "$answered" = "$probes" ] \
		|| fail "$stack: $name answered $answered of $probes probes — a balancer that drops connections is not distributing them"

	for machine in $want; do
		case " $seen " in
		*" $machine "*) ;;
		*) fail "$stack: $name never sent a connection to $machine in $probes probes, although the pack recorded it as distributed — a backend that receives nothing is registered, not delivered (#483)" ;;
		esac
	done

	local count
	count="$(printf '%s' "$want" | wc -w)"
	if [ "$count" -lt 2 ]; then
		skip "$stack: $name distributes to $count backend, so this pass did not exercise a spread over several. The runtime distributes only to backends of the balancer's own subnet (#457, docs/limits.md); tools/conformance/outscale/balancer.sh measures the multi-backend spread on a topology built for it"
	fi
	ok "$stack: $name answered $probes/$probes over $count recorded backend(s):$seen, and none the runtime withheld"
}

# fnl_backend_withdrawn: removing a backend is visible from outside.
#
# Both shapes are covered, because the honest answer depends on what is left:
# with backends remaining, the balancer must go on answering and never from the
# withdrawn one; with none remaining, it must stop answering altogether. A
# verdict written for only one of the two would pass by luck on the other.
fnl_backend_withdrawn() { # stack name withdrawn hits_file remaining
	local stack="$1" name="$2" withdrawn="$3" hits="$4" remaining="$5"
	local hit answered=0 probes=0
	while IFS= read -r hit; do
		probes=$((probes + 1))
		[ -n "$hit" ] || continue
		answered=$((answered + 1))
		[ "$hit" != "$withdrawn" ] \
			|| fail "$stack: $withdrawn still receives connections through $name after the API unregistered it — the runtime did not follow the control plane"
	done <"$hits"
	[ "$probes" -gt 0 ] \
		|| fail "$stack: $name was probed zero times after the withdrawal; nothing was compared"
	if [ "$remaining" -gt 0 ]; then
		[ "$answered" -gt 0 ] \
			|| fail "$stack: $name answered none of $probes probes after $withdrawn was unregistered, although $remaining backend(s) remain — the withdrawal took the whole balancer with it"
		ok "$stack: $withdrawn receives nothing through $name, and the remaining $remaining backend(s) still answer"
		return
	fi
	[ "$answered" = 0 ] \
		|| fail "$stack: $name answered $answered of $probes probes with no backend left registered — something is serving that address which the control plane does not describe"
	ok "$stack: $name answers nothing once its last backend is unregistered, which is the withdrawal seen from outside"
}

# ---- the lifecycle: what a machine must still be after a restart ------------
#
# Every family above starts its machines once and measures afterwards. None of
# them restarted one, so the gap between "launched" and "relaunched" was
# measured nowhere — and three defects lived in it (#547, #549, #498), all found
# by hand on 2026-08-27. The three verdicts below are that gap, written down.

# fnl_restart_replaced_the_machine: the provider's own reboot verb really
# restarted the machine.
#
# The witness is the runtime's process for the instance, read from the host the
# way witness.sh reads an instance's status — never from inside the target,
# which this file only ever uses as the console that originates a probe. A
# reboot that leaves the same process leaves the same kernel, the same uptime
# and the same transient units: that is #547, where the action answered
# "success", the API answered "running", and nothing had happened.
#
# Empty on either side is "nobody could look", never "it did not restart": the
# whole point of the third outcome.
fnl_restart_replaced_the_machine() { # stack name machine before after
	local stack="$1" name="$2" machine="$3" before="$4" after="$5"
	{ [ -n "$before" ] && [ -n "$after" ]; } \
		|| fail "cannot look: the runtime did not say which process holds $machine before ('$before') and after ('$after') the reboot; no measurement is not a measurement"
	[ "$before" != "$after" ] \
		|| fail "$stack: $name ($machine) came out of a reboot through the provider's own API on the same runtime process ($after) — the action was accepted and the machine never restarted (#547)"
	ok "$stack: $name really restarts on the provider's own reboot verb (process $before then $after)"
}

# fnl_restart_keeps_reaching: a machine restarted through the provider's API
# still reaches the machine one subnet away that it reached before it went down.
#
# This is #549, and its shape is the reason it needs four probes rather than
# one. The defect is not "unreachable": it is "reachable, then not, while an
# identical neighbour that was not restarted goes on reaching it in the same
# pass". So the before-probe is the positive control of the after-probe, and the
# control machine is the positive control of the whole pass — without them, a
# target that simply died reads exactly like a restart that lost its routes.
#
# before, after and control_after are probe codes the caller captured: 0 the
# port answers, 1 refused or timed out, 2 nobody could look. control is the
# unrestarted machine's declared name, empty when the stack wrote a reason it
# has none.
fnl_restart_keeps_reaching() { # stack restarted target address port listen_file before after control control_after
	local stack="$1" restarted="$2" target="$3" address="$4" port="$5" listen="$6"
	local before="$7" after="$8" control="$9" control_after="${10}"

	fnl_listens "$port" "$listen" \
		|| fail "$stack: $target does not listen on $port inside the machine (listening: $(tr '\n' ' ' <"$listen")); 'no longer reachable' would be measuring a dead service rather than a restart"

	[ "$before" != 2 ] \
		|| fail "cannot look: the probe from $restarted towards $address:$port could not be made at all before the restart"
	[ "$before" = 0 ] \
		|| fail "$stack: $restarted does not reach $target at $address:$port BEFORE any restart, on a port $target is listening on — this run cannot say what a restart cost, and the pair the stack declares does not hold in the first place"

	[ "$after" != 2 ] \
		|| fail "cannot look: the probe from $restarted towards $address:$port could not be made at all after the restart"

	if [ "$after" != 0 ]; then
		# Which of the two findings this is, said rather than left to the
		# reader: the neighbour is what separates "the restart lost its routes"
		# from "the target or the network went away". Blaming the restart in the
		# second case would be reporting the wrong subject, which is the failure
		# this repository has paid for most often.
		if [ -n "$control" ] && [ "$control_after" = 2 ]; then
			fail "cannot look: the control probe from $control towards $address:$port could not be made at all, so what $restarted lost cannot be attributed"
		fi
		if [ -n "$control" ] && [ "$control_after" != 0 ]; then
			fail "$stack: neither $restarted, which was restarted, nor $control, which was not, reaches $target at $address:$port — and $restarted reached it before. The target or the network went away between the two probes, so this pass says nothing about the restart"
		fi
		if [ -n "$control" ]; then
			fail "$stack: $restarted reached $target at $address:$port before being restarted through the API and does not afterwards, while $control — the same subnet, the same group, never restarted — still reaches it in the same pass (#549)"
		fi
		fail "$stack: $restarted reached $target at $address:$port before being restarted through the API and does not afterwards (#549)"
	fi

	if [ -n "$control" ]; then
		[ "$control_after" != 2 ] \
			|| fail "cannot look: the control probe from $control towards $address:$port could not be made at all"
		[ "$control_after" = 0 ] \
			|| fail "$stack: $restarted reaches $target at $address:$port and $control, which was never restarted, does not — this pass is measuring something other than the restart, and reading it either way would be reading an instrument"
		ok "$stack: $restarted still reaches $target at $address:$port after a restart, and so does $control, which was not restarted"
		return
	fi
	ok "$stack: $restarted still reaches $target at $address:$port after a restart"
}

# fnl_rule_sets: the rule sets the runtime holds for this stack's machines, in
# the two numbers the stack declares.
#
# It runs after the restarts on purpose. A rule set lives on a NIC, a restart
# re-plugs NICs, and a machine that came back without its set is a machine the
# API describes as filtered and the host does not filter — the same family of
# defect as #549, one layer up. The counts are the stack's own declaration
# rather than a table here, so a stack that grows a tier changes one file.
#
# sets_file: `<machine>\t<comma-separated rule sets>`, one line per machine, the
# whole set the runtime reports; the prefix is what selects this pack's own.
fnl_rule_sets() { # stack prefix sets_file want_sets want_references
	local stack="$1" prefix="$2" file="$3" want_sets="$4" want_refs="$5"
	local machine acls acl seen="" sets=0 refs=0 machines=0

	[ -s "$file" ] \
		|| fail "cannot look: no machine of $stack was read for its rule sets; every count this run would report is an instrument failure"
	while IFS=$'\t' read -r machine acls; do
		[ -n "$machine" ] || continue
		machines=$((machines + 1))
		for acl in $(printf '%s' "$acls" | tr ',' ' '); do
			case "$acl" in
			"$prefix"*) ;;
			*) continue ;;
			esac
			refs=$((refs + 1))
			case " $seen " in
			*" $acl "*) ;;
			*)
				seen="$seen $acl"
				sets=$((sets + 1))
				;;
			esac
		done
	done <"$file"

	[ "$sets" = "$want_sets" ] \
		|| fail "$stack: the runtime holds $sets rule set(s) named $prefix* across its $machines machine(s), where the stack declares $want_sets ($(echo "$seen" | tr -s ' ')) — a count that moved is a group that stopped being applied or one that was applied twice, and either is a finding"
	[ "$refs" = "$want_refs" ] \
		|| fail "$stack: the $sets rule set(s) named $prefix* are referenced $refs time(s) across its $machines machine(s), where the stack declares $want_refs — a machine lost or gained a group without the control plane saying so"
	ok "$stack: $sets rule set(s) $prefix*, $refs reference(s) across $machines machine(s), as declared"
}

# The rule-set reader proves it can find before it judges, and that it refuses a
# near miss: another pack's sets on the same host must not be counted as this
# one's, which is exactly what a run of two stacks would otherwise do.
fnl_rule_set_reader_control() {
	local dir out code
	dir="$(mktemp -d)"
	printf '%s\t%s\n' \
		"feint-scw-1" "scw-aaa" \
		"feint-scw-2" "scw-aaa,scw-bbb" \
		"feint-osc-1" "osc-zzz" >"$dir/sets"
	out="$(fnl_rule_sets control scw- "$dir/sets" 2 3 2>&1)"
	code=$?
	[ "$code" = 0 ] \
		|| { rm -rf "$dir"; fail "the rule-set reader cannot count two planted sets and three references: $out"; }
	# The near miss, and it is the one a two-stack run would hit: the osc- line
	# is on the same host and must not be counted here. Declaring three sets
	# must therefore go red, or the reader is counting somebody else's.
	out="$(fnl_rule_sets control scw- "$dir/sets" 3 4 2>&1)"
	code=$?
	rm -rf "$dir"
	[ "$code" != 0 ] \
		|| fail "the rule-set reader accepted a count that includes another pack's sets: $out"
	ok "the rule-set reader counts a pack's own sets and references, and no other pack's"
}
