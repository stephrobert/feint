# shellcheck shell=bash
# What a Day-2 client asks, as verdicts (#673).
#
# A provisioning client asks "did it get created". A Day-2 client asks "is it
# still what I think it is, after I changed it" — and four defects of this
# repository came from a generated Ansible collection asking exactly that,
# none from a gate this repository owned: a reboot that answered success and
# changed nothing observable (#654), an offer table declined because no
# provisioning client reads it (#658), four load-balancer reads a Day-2 client
# asks and a Terraform run never does (#666). The middle of a resource's life
# is where a control plane rots, and nothing here replayed it.
#
# Two disciplines, both already paid for on this repository:
#
#   1. A 200 is not evidence. Every write is followed by a read, and the read
#      has to say the change happened — #654 is the whole argument. The
#      comparator below judges a document against a path and a value, and
#      names the step, the path, the wanted value and the one found.
#   2. Every wait must be able to fail and say how long it waited, and every
#      check must be able to find something. d2_settles is bounded and
#      reports its wait; d2_reader_control plants a wrong value and requires
#      the comparator to refuse it, before any verdict is drawn.
#
# Sourced by tools/conformance/day2.sh and driven, with planted documents, by
# tools/conformance/day2_test.go. Every function expects `fail`, `ok` and
# `skip` to be defined by the caller, like functionallib.sh's.

# d2_says is the read-back verdict: the document on stdin, read at the jq path,
# must equal the wanted value. The value is compared as jq renders it with -r,
# so a string, a number and a boolean each read as themselves and an absent
# path reads as the literal "null" — which then compares unequal to anything
# a write could have meant, and is named as such.
#
# The step is named in the refusal because the catalogue plays thirty of them
# and "expected x, got y" without the step is a defect nobody can find.
d2_says() { # step path wanted <document on stdin
	local step="$1" path="$2" wanted="$3" got
	got="$(jq -r "$path" 2>/dev/null)" || fail "$step: the read-back is not a document jq can read at $path"
	[ "$got" = "$wanted" ] \
		|| fail "$step: the read after the write does not say the change happened — $path is '$got', the write asked for '$wanted' (a 200 is not evidence, #654)"
	ok "$step: read back, $path is '$wanted'"
}

# d2_settles waits, bounded, until a read at a path answers the wanted value,
# and says how long it waited. It is for the one class of write whose read
# legitimately lags: a lifecycle action walking its transient states. The
# reader is a command that prints the document; the verdict stays the
# caller's — a wait that gives up answers 1 and the caller names the failure.
#
# It prints the wait to stderr for the reason station_fetch_within does: a
# window that is 0 s on the station and seconds on a runner is a fact worth
# keeping, and the next person reading a red night can see whether it widens.
d2_settles() { # seconds step path wanted reader...
	local budget="$1" step="$2" path="$3" wanted="$4" waited=0 got
	shift 4
	while :; do
		got="$("$@" 2>/dev/null | jq -r "$path" 2>/dev/null)"
		if [ "$got" = "$wanted" ]; then
			[ "$waited" -gt 0 ] && echo "    ($step: $path became '$wanted' after ${waited}s)" >&2
			return 0
		fi
		if [ "$waited" -ge "$budget" ]; then
			echo "    ($step: $path is still '$got' after ${budget}s, wanted '$wanted')" >&2
			return 1
		fi
		sleep 1
		waited=$((waited + 1))
	done
}

# d2_reader_control is the planted control: the comparator finds a wanted
# value, refuses a wrong one, and refuses an absent path rather than reading
# it as anything. Run before any verdict. A comparator that had never refused
# anything is indistinguishable from one that compares nothing.
d2_reader_control() {
	printf '%s' '{"server":{"name":"platform-web-0","tags":["platform","web"]}}' \
		| d2_says control .server.name platform-web-0 >/dev/null \
		|| fail "the read-back comparator cannot find a planted value; every 'read back' it reported would be an instrument failure"
	if (printf '%s' '{"server":{"name":"platform-web-0"}}' | d2_says control .server.name platform-web-1) >/dev/null 2>&1; then
		fail "the read-back comparator passed a planted wrong value; every Day-2 write it judged would pass on a 200 alone (#654)"
	fi
	if (printf '%s' '{"server":{"name":"platform-web-0"}}' | d2_says control .server.renamed platform-web-0) >/dev/null 2>&1; then
		fail "the read-back comparator passed an absent path; a field the emulator stopped round-tripping would read as unchanged"
	fi
	d2_settles 1 control .a b printf '%s' '{"a":"b"}' >/dev/null 2>&1 \
		|| fail "the bounded wait cannot see a value that is already there"
	if d2_settles 1 control .a c printf '%s' '{"a":"b"}' >/dev/null 2>&1; then
		fail "the bounded wait gave up nothing: a value that never arrives was waited for and passed"
	fi
	ok "the read-back comparator finds a planted value, refuses a wrong one and an absent path, and the wait is bounded"
}

# ---- the transports the catalogue rides --------------------------------------
#
# Raw HTTP rather than `scw`, and that is a choice about which client is being
# imitated: the Day-2 client this suite is modelled on is a generated Ansible
# collection, which drives the API through the SDK and never through the CLI.
# What a module does is a write and a read at two URLs, and this is exactly
# that. The CLI suites keep proving the CLI.

D2_ENDPOINT=""
D2_STEP=""
D2_WRITES=0
# The reads are counted in a file rather than a variable: a read runs on the
# left of a pipe into d2_says, which is a subshell, and a counter incremented
# there is lost — measured on the first run, which reported 4 reads for 44
# writes. The file survives the subshell; d2_reads sums it.
D2_READS_FILE=""

# d2_write plays one write and prints the answer; anything but a 2xx fails the
# step by name, with the status and the body's head. A caller capturing the
# answer through `$( )` adds `|| exit 1`: the fail inside exits the subshell
# alone, and the caller would otherwise carry on with an empty answer — the
# same trap functional.sh's transports document.
d2_write() { # method path [body]
	local out code body
	local -a data=()
	[ -n "${3:-}" ] && data=(-d "$3")
	out="$(mktemp)"
	code="$(curl -s -o "$out" -w '%{http_code}' -X "$1" -H 'Content-Type: application/json' "${data[@]}" "$D2_ENDPOINT$2")"
	D2_WRITES=$((D2_WRITES + 1))
	case "$code" in
		2*) cat "$out"; rm -f "$out"; return 0 ;;
	esac
	body="$(head -c 300 "$out")"
	rm -f "$out"
	fail "$D2_STEP: $1 $2 answered $code: $body"
}

# d2_count_read records one read, in the file when the caller opened one.
d2_count_read() {
	[ -n "$D2_READS_FILE" ] && echo r >>"$D2_READS_FILE"
	return 0
}

# d2_reads answers how many reads the catalogue made so far.
d2_reads() {
	if [ -n "$D2_READS_FILE" ] && [ -s "$D2_READS_FILE" ]; then
		wc -l <"$D2_READS_FILE" | tr -d ' '
	else
		echo 0
	fi
}

# d2_read prints the document a path answers, and fails the step when it
# answers nothing: a write that cannot be read back is a write nobody can
# judge.
d2_read() { # path
	d2_count_read
	curl -sf "$D2_ENDPOINT$1" || fail "$D2_STEP: GET $1 answered nothing, so the write cannot be read back"
}

# d2_gone is the read after a delete: the path must answer 404, not the
# document it answered before.
d2_gone() { # path
	local code
	d2_count_read
	code="$(curl -s -o /dev/null -w '%{http_code}' "$D2_ENDPOINT$1")"
	[ "$code" = 404 ] \
		|| fail "$D2_STEP: $1 still answers $code after its delete; a resource deleted and still readable is a 200 standing in for evidence"
	ok "$D2_STEP: read back, $1 is gone (404)"
}

# ---- the Scaleway catalogue ---------------------------------------------------
#
# What an operator does to examples/stacks/scaleway over a month, in pairs: a
# change, read back, and its undo, read back. Pairs on purpose — the platform
# must end where it started, so the data-plane verdicts that follow, and the
# machine's own table compared with its capture from before (#671), judge a
# stack thirty writes older and not a different one.
#
# The identifiers are resolved by name off /_feint/state once, by
# d2_resolve_scaleway; the zone and region are the stack's.

D2_ZONE="fr-par-1"
D2_REGION="fr-par"
D2_WEB0=""
D2_BASTION=""
D2_SGWEB=""
D2_PNAPP=""
D2_LB=""
D2_BACKEND=""
D2_FRONTEND=""
D2_VOL0=""
D2_PG=""
D2_VPC=""
D2_GW=""
D2_SSHKEY=""
D2_IPWEB0=""
D2_PROJECT=""

# d2_id answers the identifier of a resource by kind and name, read off the
# state document, or fails: a step on a resource the stack does not hold is a
# step measuring nothing.
d2_id() { # state_file kind name
	local id
	id="$(jq -r --arg k "$2" --arg n "$3" '[.resources[] | select(.Kind == $k and (.Attrs.name // "") == $n) | .ID] | first // ""' <"$1")"
	[ -n "$id" ] || fail "day2: the stack holds no $2 named $3, so its steps cannot be played"
	printf '%s' "$id"
}

d2_resolve_scaleway() { # state_file
	D2_WEB0="$(d2_id "$1" instance/server platform-web-0)"
	D2_BASTION="$(d2_id "$1" instance/server platform-bastion)"
	D2_SGWEB="$(d2_id "$1" instance/security-group platform-web)"
	D2_PNAPP="$(d2_id "$1" vpc/private-network platform-app)"
	D2_LB="$(d2_id "$1" lb/lb platform-front)"
	D2_BACKEND="$(d2_id "$1" lb/backend platform-web)"
	D2_FRONTEND="$(d2_id "$1" lb/frontend platform-https)"
	D2_VOL0="$(d2_id "$1" instance/volume platform-web-data-0)"
	D2_PG="$(d2_id "$1" instance/placement-group platform-app)"
	D2_VPC="$(d2_id "$1" vpc/vpc platform-workload)"
	D2_GW="$(d2_id "$1" vpcgw/gateway platform-egress)"
	D2_SSHKEY="$(d2_id "$1" iam/ssh-key platform)"
	local server
	D2_STEP="resolve platform-web-0"
	server="$(d2_read "/instance/v1/zones/$D2_ZONE/servers/$D2_WEB0")" || exit 1
	D2_IPWEB0="$(printf '%s' "$server" | jq -r '.server.public_ips[0].id // ""')"
	D2_PROJECT="$(printf '%s' "$server" | jq -r '.server.project // ""')"
	[ -n "$D2_IPWEB0" ] && [ -n "$D2_PROJECT" ] \
		|| fail "day2: platform-web-0 publishes no public address or no project, and the address steps need both"
}

# A field set and read back, then set back and read back: one pair.
# A RED READ-BACK REDDENS THE LEG, and it did not (measured 2026-09-04 on
# 116d181): d2_says fails inside the subshell of `d2_read | d2_says`, its
# `fail` left the pipeline and nothing else, the leg went on through 43 more
# writes, the shape compare, the verification counters and the stack gate, and
# exited 0 with one FAIL printed on the way. Every read-back pipeline of a step
# now ends the leg on failure (`|| exit 1`, the trap sweeps), and the catalogue
# refuses to go past a step that returned 1. TestARedReadBackReddensTheLeg and
# TestAStepThatFailsStopsTheCatalogue fail without this.
d2_pair() { # step method path field(jq) set_body set_want back_body back_want
	D2_STEP="$1"
	d2_write "$2" "$3" "$5" >/dev/null
	d2_read "$3" | d2_says "$1" "$4" "$6" || exit 1
	d2_write "$2" "$3" "$7" >/dev/null
	d2_read "$3" | d2_says "$1 (undone)" "$4" "$8" || exit 1
}

d2_step_server_rename() {
	d2_pair "rename platform-web-0" PATCH "/instance/v1/zones/$D2_ZONE/servers/$D2_WEB0" .server.name \
		'{"name":"platform-web-0-day2"}' platform-web-0-day2 '{"name":"platform-web-0"}' platform-web-0
}
d2_step_server_tags() {
	d2_pair "retag platform-web-0" PATCH "/instance/v1/zones/$D2_ZONE/servers/$D2_WEB0" '.server.tags | join(",")' \
		'{"tags":["platform","web","day2"]}' "platform,web,day2" '{"tags":["platform","web"]}' "platform,web"
}
d2_step_server_protected() {
	d2_pair "protect platform-web-0" PATCH "/instance/v1/zones/$D2_ZONE/servers/$D2_WEB0" .server.protected \
		'{"protected":true}' true '{"protected":false}' false
}
d2_step_ip_tags() {
	d2_pair "retag the public address of platform-web-0" PATCH "/instance/v1/zones/$D2_ZONE/ips/$D2_IPWEB0" '.ip.tags | join(",")' \
		'{"tags":["day2"]}' day2 '{"tags":[]}' ""
}
# The reverse is set back with "" rather than null, and that is a measured
# choice rather than a preference: on 2026-09-04 `{"reverse": null}` answered
# 200 and the read still carried the name. Whether the real API clears the
# reverse on null is a measurement nobody here has made, so this step does not
# assert it; it is recorded in the commit that added it.
d2_step_ip_reverse() {
	# Undone with null, read back as null: measured on a real account (#676,
	# fr-par-1, 2026-09-04), {"reverse": null} and {"reverse": ""} both clear
	# the reverse and the read answers null. The step used to undo with ""
	# and expect "" back, which is what this emulator answered before #676
	# fixed it; the leg went red on exactly that line the day #676 reached
	# main, as #676's commit message had said it would.
	d2_pair "set the reverse of platform-web-0's address" PATCH "/instance/v1/zones/$D2_ZONE/ips/$D2_IPWEB0" .ip.reverse \
		'{"reverse":"web0.platform.example"}' web0.platform.example '{"reverse":null}' null
}
d2_step_sg_rename() {
	d2_pair "rename the web security group" PATCH "/instance/v1/zones/$D2_ZONE/security_groups/$D2_SGWEB" .security_group.name \
		'{"name":"platform-web-day2"}' platform-web-day2 '{"name":"platform-web"}' platform-web
}
d2_step_sg_policy() {
	d2_pair "open then close the web group's inbound default" PATCH "/instance/v1/zones/$D2_ZONE/security_groups/$D2_SGWEB" .security_group.inbound_default_policy \
		'{"inbound_default_policy":"accept"}' accept '{"inbound_default_policy":"drop"}' drop
}
d2_step_pn_rename() {
	d2_pair "rename the app private network" PATCH "/vpc/v2/regions/$D2_REGION/private-networks/$D2_PNAPP" .name \
		'{"name":"platform-app-day2"}' platform-app-day2 '{"name":"platform-app"}' platform-app
}
d2_step_pn_tags() {
	d2_pair "retag the app private network" PATCH "/vpc/v2/regions/$D2_REGION/private-networks/$D2_PNAPP" '.tags | join(",")' \
		'{"tags":["day2"]}' day2 '{"tags":["platform","app"]}' "platform,app"
}
d2_step_vpc_rename() {
	d2_pair "rename the workload VPC" PATCH "/vpc/v2/regions/$D2_REGION/vpcs/$D2_VPC" .name \
		'{"name":"platform-workload-day2"}' platform-workload-day2 '{"name":"platform-workload"}' platform-workload
}
d2_step_lb_rename() {
	d2_pair "rename the load balancer" PUT "/lb/v1/zones/$D2_ZONE/lbs/$D2_LB" .name \
		'{"name":"platform-front-day2","description":"","tags":["platform","web"]}' platform-front-day2 \
		'{"name":"platform-front","description":"","tags":["platform","web"]}' platform-front
}
d2_step_frontend_rename() {
	d2_pair "rename the https frontend" PUT "/lb/v1/zones/$D2_ZONE/frontends/$D2_FRONTEND" .name \
		"{\"name\":\"platform-https-day2\",\"inbound_port\":443,\"backend_id\":\"$D2_BACKEND\"}" platform-https-day2 \
		"{\"name\":\"platform-https\",\"inbound_port\":443,\"backend_id\":\"$D2_BACKEND\"}" platform-https
}
d2_step_volume_rename() {
	d2_pair "rename platform-web-data-0" PATCH "/instance/v1/zones/$D2_ZONE/volumes/$D2_VOL0" .volume.name \
		'{"name":"platform-web-data-0-day2"}' platform-web-data-0-day2 '{"name":"platform-web-data-0"}' platform-web-data-0
}
d2_step_pg_rename() {
	d2_pair "rename the app placement group" PATCH "/instance/v1/zones/$D2_ZONE/placement_groups/$D2_PG" .placement_group.name \
		'{"name":"platform-app-day2"}' platform-app-day2 '{"name":"platform-app"}' platform-app
}
d2_step_gateway_rename() {
	d2_pair "rename the public gateway" PATCH "/vpc-gw/v2/zones/$D2_ZONE/gateways/$D2_GW" .name \
		'{"name":"platform-egress-day2"}' platform-egress-day2 '{"name":"platform-egress"}' platform-egress
}
d2_step_sshkey_rename() {
	d2_pair "rename the platform ssh key" PATCH "/iam/v1alpha1/ssh-keys/$D2_SSHKEY" .name \
		'{"name":"platform-day2"}' platform-day2 '{"name":"platform"}' platform
}
# The web backend gains a server and loses it: the Day-2 question about a
# balancer, and the one #666 is about on the read side.
d2_step_backend_servers() {
	local path="/lb/v1/zones/$D2_ZONE/backends/$D2_BACKEND"
	D2_STEP="add a server to the web backend"
	d2_write PUT "$path/servers" '{"server_ip":["10.30.1.10","10.30.1.11","10.30.1.99"]}' >/dev/null
	d2_read "$path" | d2_says "$D2_STEP" '.pool | index("10.30.1.99") != null' true || exit 1
	D2_STEP="remove that server from the web backend"
	d2_write PUT "$path/servers" '{"server_ip":["10.30.1.10","10.30.1.11"]}' >/dev/null
	d2_read "$path" | d2_says "$D2_STEP" '.pool | index("10.30.1.99") != null' false || exit 1
}
# A rule added and removed: the read is the rule list, not the 201.
d2_step_sg_rule() {
	local path="/instance/v1/zones/$D2_ZONE/security_groups/$D2_SGWEB/rules" rule
	D2_STEP="add a rule to the web security group"
	rule="$(d2_write POST "$path" '{"protocol":"TCP","direction":"inbound","action":"accept","ip_range":"10.0.0.0/8","dest_port_from":9999,"dest_port_to":9999}' | jq -r '.rule.id // ""')" || exit 1
	[ -n "$rule" ] || fail "$D2_STEP: the create answered no rule id"
	d2_read "$path" | d2_says "$D2_STEP" "[.rules[] | select(.id == \"$rule\")] | length" 1 || exit 1
	D2_STEP="remove that rule"
	d2_write DELETE "$path/$rule" >/dev/null
	d2_read "$path" | d2_says "$D2_STEP" "[.rules[] | select(.id == \"$rule\")] | length" 0 || exit 1
}
# A public address created, attached, detached and deleted: under a runtime
# each door routes and unroutes the /32 on the machine, and the layer reads
# every one of them back (#670).
d2_step_ip_attach_detach() {
	local ips="/instance/v1/zones/$D2_ZONE/ips" server="/instance/v1/zones/$D2_ZONE/servers/$D2_WEB0" ip
	D2_STEP="create a public address"
	ip="$(d2_write POST "$ips" "{\"project\":\"$D2_PROJECT\"}" | jq -r '.ip.id // ""')" || exit 1
	[ -n "$ip" ] || fail "$D2_STEP: the create answered no ip id"
	D2_STEP="attach it to platform-web-0"
	d2_write PATCH "$ips/$ip" "{\"server\":\"$D2_WEB0\"}" >/dev/null
	d2_read "$ips/$ip" | d2_says "$D2_STEP" '.ip.server.id' "$D2_WEB0" || exit 1
	d2_read "$server" | d2_says "$D2_STEP (seen from the server)" "[.server.public_ips[] | select(.id == \"$ip\")] | length" 1 || exit 1
	D2_STEP="detach it"
	d2_write PATCH "$ips/$ip" '{"server":null}' >/dev/null
	d2_read "$ips/$ip" | d2_says "$D2_STEP" '.ip.server' null || exit 1
	d2_read "$server" | d2_says "$D2_STEP (seen from the server)" "[.server.public_ips[] | select(.id == \"$ip\")] | length" 0 || exit 1
	D2_STEP="delete it"
	d2_write DELETE "$ips/$ip" >/dev/null
	d2_gone "$ips/$ip"
}
# A private NIC on a second network, then gone: the hot join and the hot
# leave, which a provisioning client never plays on a running machine.
d2_step_nic_join_leave() {
	local path="/instance/v1/zones/$D2_ZONE/servers/$D2_WEB0/private_nics" nic
	D2_STEP="join platform-web-0 to the app network"
	nic="$(d2_write POST "$path" "{\"private_network_id\":\"$D2_PNAPP\"}" | jq -r '.private_nic.id // ""')" || exit 1
	[ -n "$nic" ] || fail "$D2_STEP: the create answered no nic id"
	d2_read "$path" | d2_says "$D2_STEP" "[.private_nics[] | select(.private_network_id == \"$D2_PNAPP\")] | length" 1 || exit 1
	D2_STEP="leave the app network"
	d2_write DELETE "$path/$nic" >/dev/null
	d2_read "$path" | d2_says "$D2_STEP" "[.private_nics[] | select(.private_network_id == \"$D2_PNAPP\")] | length" 0 || exit 1
}
d2_step_volume_create_delete() {
	local path="/instance/v1/zones/$D2_ZONE/volumes" volume
	D2_STEP="create a scratch volume"
	volume="$(d2_write POST "$path" "{\"name\":\"platform-day2-scratch\",\"volume_type\":\"l_ssd\",\"size\":10000000000,\"project\":\"$D2_PROJECT\"}" | jq -r '.volume.id // ""')" || exit 1
	[ -n "$volume" ] || fail "$D2_STEP: the create answered no volume id"
	d2_read "$path/$volume" | d2_says "$D2_STEP" '.volume.size' 10000000000 || exit 1
	D2_STEP="delete the scratch volume"
	d2_write DELETE "$path/$volume" >/dev/null
	d2_gone "$path/$volume"
}
d2_step_snapshot_create_delete() {
	local path="/instance/v1/zones/$D2_ZONE/snapshots" snapshot
	D2_STEP="snapshot platform-web-data-0"
	snapshot="$(d2_write POST "$path" "{\"name\":\"platform-day2-snap\",\"volume_id\":\"$D2_VOL0\",\"project\":\"$D2_PROJECT\"}" | jq -r '.snapshot.id // ""')" || exit 1
	[ -n "$snapshot" ] || fail "$D2_STEP: the create answered no snapshot id"
	d2_read "$path/$snapshot" | d2_says "$D2_STEP" '.snapshot.base_volume.id' "$D2_VOL0" || exit 1
	D2_STEP="delete the snapshot"
	d2_write DELETE "$path/$snapshot" >/dev/null
	d2_gone "$path/$snapshot"
}
# User data is text, not JSON: the one write of the catalogue that is not a
# document, read back as one.
d2_step_user_data() {
	local path="/instance/v1/zones/$D2_ZONE/servers/$D2_BASTION/user_data" code
	D2_STEP="set a user data key on the bastion"
	code="$(curl -s -o /dev/null -w '%{http_code}' -X PATCH -H 'Content-Type: text/plain' --data-binary 'day2 was here' "$D2_ENDPOINT$path/day2")"
	D2_WRITES=$((D2_WRITES + 1))
	[ "$code" = 204 ] || fail "$D2_STEP: the write answered $code"
	d2_count_read
	[ "$(curl -sf "$D2_ENDPOINT$path/day2")" = "day2 was here" ] \
		|| fail "$D2_STEP: the read after the write does not carry the text that was written"
	d2_read "$path" | d2_says "$D2_STEP" '.user_data | index("day2") != null' true || exit 1
	D2_STEP="remove that key"
	d2_write DELETE "$path/day2" >/dev/null
	d2_read "$path" | d2_says "$D2_STEP" '.user_data | index("day2") != null' false || exit 1
}
# The reboot, and the argument of #654 in one step: the first read after the
# action must NOT say running — that is what it said before, and an action
# whose every read says what the last one said is an action nobody can see —
# and a bounded wait must then see running again, saying how long it waited.
d2_step_reboot() {
	local path="/instance/v1/zones/$D2_ZONE/servers/$D2_WEB0" first
	D2_STEP="reboot platform-web-0"
	d2_write POST "$path/action" '{"action":"reboot"}' >/dev/null
	first="$(d2_read "$path" | jq -r '.server.state')"
	[ "$first" != running ] \
		|| fail "$D2_STEP: the first read after the reboot says running, which is what it said before the action — accepted, and nothing observable happened (#654)"
	ok "$D2_STEP: the first read after the action says '$first', not running"
	d2_settles 180 "$D2_STEP" .server.state running d2_read "$path" \
		|| fail "$D2_STEP: the server never came back running"
	ok "$D2_STEP: read back, .server.state is 'running' again"
}

# The catalogue, in the order it plays. The reboot is last: the machine's own
# table is compared after it (#671) and the data-plane verdicts follow, so a
# reboot that lost a route is found by the two instruments that name it.
D2_SCALEWAY_STEPS=(
	server_rename server_tags server_protected
	ip_tags ip_reverse ip_attach_detach
	sg_rename sg_rule sg_policy
	pn_rename pn_tags vpc_rename
	lb_rename backend_servers frontend_rename
	volume_rename volume_create_delete snapshot_create_delete
	user_data pg_rename gateway_rename sshkey_rename
	nic_join_leave reboot
)

d2_catalogue_scaleway() {
	local step
	for step in "${D2_SCALEWAY_STEPS[@]}"; do
		"d2_step_$step" || exit 1
	done
}

# ---- the sweep ---------------------------------------------------------------
#
# d2_sweep takes the run's stack down whichever way the run ended, and refuses
# to say "swept" on anything it did not see (#673).
#
# What it replaces was `(cd "$WORK" && feint down)` in an EXIT trap, and it
# failed exactly the way this repository has paid for before (forty-eight live
# loops behind a cleanup that reported success): on 2026-09-04 the leg went
# red on #660, the stack gate's own trap removed the work directory, the cd
# failed, the down never ran, `rm -rf` of a directory that no longer existed
# succeeded, and seven machines stayed up behind an exit that said nothing.
#
# Three rules, each held by a test in day2_test.go:
#
#   - the down runs from the stack directory while it exists; when it is gone
#     there is no feint.yaml to read, so the emulator is stopped by address
#     and the runtime swept with `feint clean -closing`, and the sweep says so
#     (TestTheSweepStopsTheEmulatorWhenTheStackDirectoryIsGone);
#   - "swept" is a reading, not a report of intent: the emulator must have
#     stopped answering, and the runtime must hold nothing of this run
#     (`feint clean -check -closing`); either finding makes the sweep fail
#     loudly (TestTheSweepRefusesToClaimWhatItDidNotSee,
#     TestTheSweepReportsALeakTheRuntimeStillHolds);
#   - the directory goes last, after the down that needs it.
#
# A fourth rule, written after the sweep above was measured on a station it
# did not have to itself: `feint clean -closing` sweeps the RUNTIME, not the
# run. On 2026-09-04 at 13:01:29 this sweep ran its fallback while another
# emulator (127.0.0.1:4690, a NAT probe) was alive with one machine of its
# own; the sweep answered rc 0, `machines left: 0`, `clean -check` found
# nothing — every reading green — and the probe's machine had gone with the
# run's. A sweep that succeeds by taking somebody else's machines is green
# and false, the exact family every other fix of that day belongs to.
#
#   - before any runtime-wide clean, the sweep reads the run registry feint
#     keeps (one directory per address under $XDG_RUNTIME_DIR/feint) and asks
#     each other address whether it still answers; one that does is a foreign
#     emulator, and the runtime-wide clean AND the runtime-wide reading are
#     refused, naming it, rc 1 (TestTheSweepRefusesARuntimeWideSweepWhileAnotherEmulatorAnswers).
#     An entry that no longer answers is a stale registration and blocks
#     nothing (TestAStaleRegistryEntryDoesNotBlockTheSweep).
#   - what the run owns is removed by `feint stop -addr`: an emulator started
#     with --cleanup takes its own machines down with it, which is why the leg
#     declares emulator.cleanup on the stack it brings up. The runtime-wide
#     clean is the safety net under that, never the removal itself.
#
# The probe is a function of its own so a test can plant an emulator that
# still answers without opening a port.
d2_still_answers() { # endpoint
	curl -sf --max-time 2 "$1/_feint/health" >/dev/null 2>&1
}

# d2_run_dir is where feint registers its running instances, one directory per
# address with the colon written as an underscore (internal/cli/instance.go).
# FEINT_RUN_DIR is the test's door: a planted registry instead of the station's.
d2_run_dir() {
	if [ -n "${FEINT_RUN_DIR:-}" ]; then
		echo "$FEINT_RUN_DIR"
	else
		echo "${XDG_RUNTIME_DIR:-/run/user/$(id -u)}/feint"
	fi
}

# d2_foreign_emulators prints the address of every registered emulator other
# than the run's own that still answers. Nothing printed means the runtime is
# this run's alone, as far as the registry can say.
d2_foreign_emulators() { # own_addr
	local own="$1" dir entry addr
	dir="$(d2_run_dir)"
	[ -d "$dir" ] || return 0
	for entry in "$dir"/*/; do
		[ -d "$entry" ] || continue
		addr="$(basename "$entry")"
		addr="${addr%_*}:${addr##*_}"
		[ "$addr" = "$own" ] && continue
		if d2_still_answers "http://$addr"; then
			echo "$addr"
		fi
	done
	return 0
}

d2_sweep() { # work up feint addr runtime
	local work="$1" up="$2" feint="$3" addr="$4" runtime="$5" rc=0 foreign=""
	if [ -n "$up" ]; then
		if [ "$runtime" != off ]; then
			foreign="$(d2_foreign_emulators "$addr" | paste -sd ' ')"
		fi
		if [ -d "$work" ]; then
			(cd "$work" && "$feint" down) >/dev/null 2>&1 \
				|| { echo "cleanup: feint down failed in $work" >&2; rc=1; }
		else
			echo "cleanup: the stack directory $work is gone, so feint down has nothing to read; stopping the emulator on $addr, which takes its own machines down with it" >&2
			"$feint" stop -addr "$addr" >/dev/null 2>&1 \
				|| { echo "cleanup: feint stop -addr $addr failed" >&2; rc=1; }
			if [ "$runtime" != off ]; then
				if [ -n "$foreign" ]; then
					echo "FAIL: cleanup: another emulator answers on $foreign; feint clean -vm $runtime -closing sweeps the whole runtime, its machines included, and is refused here — stop that emulator, or sweep by hand" >&2
					rc=1
				else
					"$feint" clean -vm "$runtime" -closing >/dev/null 2>&1 \
						|| { echo "cleanup: feint clean -vm $runtime -closing failed" >&2; rc=1; }
				fi
			fi
		fi
		if d2_still_answers "http://$addr"; then
			echo "FAIL: cleanup: the emulator still answers on $addr after the sweep, so nothing was swept" >&2
			rc=1
		fi
		if [ "$runtime" != off ]; then
			if [ -n "$foreign" ]; then
				echo "FAIL: cleanup: another emulator answers on $foreign, so whether this run left anything on the $runtime runtime cannot be read apart from theirs; not read" >&2
				rc=1
			elif ! "$feint" clean -check -vm "$runtime" -closing >/dev/null 2>&1; then
				echo "FAIL: cleanup: machines or networks of this run are still on the $runtime runtime; feint clean -check -vm $runtime -closing names them" >&2
				rc=1
			fi
		fi
	fi
	if [ -n "$work" ] && [ -d "$work" ]; then
		rm -rf "$work"
	fi
	if [ "$rc" = 0 ] && [ -n "$up" ]; then
		echo "  cleanup: swept, nothing answers on $addr"
	fi
	return "$rc"
}

# d2_declare_cleanup makes the copied declaration start its emulator with
# --cleanup, so that `feint stop -addr` — the removal d2_sweep falls back on
# when the stack directory is gone — takes the run's own machines down with
# the emulator, and nothing wider is needed for them. The example stack does
# not declare it (it runs under `off` by default and belongs to the
# maintainer); the leg's copy does. Inserted once, under `emulator:`, and left
# alone when already there.
# TestTheDay2LegDeclaresAnEmulatorThatCleansUpAfterItself fails without this.
d2_declare_cleanup() { # feint.yaml
	local file="$1"
	grep -q '^  cleanup: true$' "$file" && return 0
	awk '{ print } /^emulator:/ && !done { print "  cleanup: true"; done = 1 }' "$file" >"$file.cleanup" \
		&& mv "$file.cleanup" "$file"
}
