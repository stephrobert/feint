#!/usr/bin/env bash
# Conformance check: an Outscale load balancer distributes real packets (#315).
#
# octl.sh proves the configuration — listeners, backends, health-check
# settings, the delete guards — and every bit of that is true of a server that
# stores JSON and forwards nothing, which is exactly what this emulator did
# until now and what docs/limits.md said. This suite measures the other half,
# and only the half that was measured to hold: a balancer's own private address,
# probed from inside the network it sits in, answers and spreads connections
# over its registered machines.
#
# What it deliberately does not measure: the public face of an internet-facing
# balancer. #315 measured that address going dark within three minutes, so the
# emulator refuses to configure it and docs/limits.md carries the figures. A
# suite asserting it would be asserting a defect.
#
# The gate is the conjunction of the two declared halves, never a mode name:
# `capabilities.balancing` says the runtime can distribute, `enforced.balancing`
# says this pack hands its balancers to it (#481). Each half has been measured
# true while the other was false — the capability was true on a process whose
# Scaleway pack left no balancer on the host — so a suite keying on one half
# asserts a property nobody promised. An undeclared half reads as absent and
# the suite skips.
#
# Requires a machine runtime with balancing: `feint serve --vm incus-ovn`.
#
# Usage: tools/conformance/outscale/balancer.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# shellcheck source=/dev/null
. "$SCRIPT_DIR/../guard.sh"
guard_local "$ENDPOINT"

command -v octl >/dev/null 2>&1 || { echo "FAIL: octl is not installed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }
command -v incus >/dev/null 2>&1 || { echo "FAIL: the incus client is not on PATH" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

# Waiting for a condition instead of for a duration (#459). The file states the
# one rule that decides where these may stand, and why the two waits still
# written `sleep` below cannot become polls.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../shared/waiting.sh"

echo "conformance: outscale load balancer dataplane against $ENDPOINT"

health="$(curl -sf "$ENDPOINT/_feint/health")" || fail "the emulator does not answer /_feint/health"
if [ "$(printf '%s' "$health" | jq -r '.machines')" = "none" ]; then
  skip "no machine runtime (start with FEINT_VM=incus-ovn); nothing to measure"
  exit 0
fi
# `// empty` on purpose: a build older than this capability answers no key at
# all, and reading that as "false" is right, while reading it as an error would
# make the suite fail on a binary that never claimed anything.
BALANCING="$(printf '%s' "$health" | jq -r '.capabilities.balancing // empty')"
if [ "$BALANCING" != "true" ]; then
  skip "this runtime does not declare balancing; a load balancer here records its configuration and forwards nothing (docs/limits.md)"
  exit 0
fi
# The other half of the claim (#481): the runtime can distribute, and this pack
# must say it hands its balancers over. `// []` for the same reason as above —
# a build older than `enforced.balancing` never claimed anything, and a suite
# that asserted distribution against it would be asserting a property nobody
# promised, which is the exact trap #481 measured on the Scaleway pack.
ENFORCED="$(printf '%s' "$health" | jq -r '(.enforced.balancing // []) | index("outscale") != null')"
if [ "$ENFORCED" != "true" ]; then
  skip "the outscale pack does not declare enforced.balancing; its load balancer records its configuration and this suite has nothing to measure"
  exit 0
fi

BLOCK="${FEINT_TEST_LB_BLOCK:-10.186.0.0/16}"
SUBBLOCK="${FEINT_TEST_LB_SUBNET_BLOCK:-10.186.7.0/24}"

WORK="$(mktemp -d)"
chmod 700 "$WORK"

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
# The endpoint comes from the argument, not from the credentials file, and it
# carries /api/v1: octl reads that from osc-sdk-go, whose default endpoint is
# https://api.<region>.outscale.com/api/v1, so the path is part of the value.
# The archived oapi-cli this replaced wanted the bare host (#460).
# shellcheck disable=SC2034 # read by octl from the environment
OSC_ENDPOINT_API="$ENDPOINT/api/v1"
set +a
cat >"$WORK/config.json" <<JSON
{"default": {"access_key": "$OSC_ACCESS_KEY", "secret_key": "$OSC_SECRET_KEY",
 "protocol": "http", "region": "eu-west-2", "endpoints": {"api": "$ENDPOINT/api/v1"}}}
JSON
# See tools/conformance/outscale/octl.sh for why each of these three is not
# optional: the API and not an alias, the API's own body and not the CLI's, and
# a request body that can only come from flags.
osc() { octl --config "$WORK/config.json" --no-upgrade -o raw iaas api "$@" </dev/null; }

lb_name="feint-lbu-data"
net_id=""; sub_id=""; vm_a=""; vm_b=""; vm_c=""; lb_made=""

cleanup() {
  [ -n "$lb_made" ] && osc DeleteLoadBalancer --LoadBalancerName "$lb_name" >/dev/null 2>&1
  for vm in "$vm_a" "$vm_b" "$vm_c"; do
    [ -n "$vm" ] && osc DeleteVms --VmIds "$vm" >/dev/null 2>&1
  done
  # waits on silence: the Subnet cannot go while a machine still sits on it, and
  # this is a trap handler — the ids may be empty and the deletes may have been
  # refused, so there is no single object whose disappearance to wait for.
  sleep 2
  [ -n "$sub_id" ] && osc DeleteSubnet --SubnetId "$sub_id" >/dev/null 2>&1
  [ -n "$net_id" ] && osc DeleteNet --NetId "$net_id" >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "- a Net, a Subnet and three machines"
net_id="$(osc CreateNet --IpRange "$BLOCK" | jq -r '.Net.NetId')"
[ -n "$net_id" ] || fail "CreateNet answered no id"
sub_id="$(osc CreateSubnet --NetId "$net_id" --IpRange "$SUBBLOCK" | jq -r '.Subnet.SubnetId')"
[ -n "$sub_id" ] || fail "CreateSubnet answered no id"

launch() {
  local doc
  doc="$(osc CreateVms --ImageId ami-00000003 --VmType tinav6.c1r1p2 --SubnetId "$sub_id")" \
    || { echo "CreateVms rejected: $doc" >&2; return 1; }
  printf '%s' "$doc" | jq -r '.Vms[0].VmId + " " + .Vms[0].PrivateIp'
}
read -r vm_a ip_a <<<"$(launch)" || fail "the first backend was not created"
read -r vm_b ip_b <<<"$(launch)" || fail "the second backend was not created"
read -r vm_c ip_c <<<"$(launch)" || fail "the client machine was not created"
[ -n "$ip_a" ] && [ -n "$ip_b" ] && [ -n "$ip_c" ] || fail "a machine came back with no PrivateIp"
ok "$vm_a=$ip_a $vm_b=$ip_b, client $vm_c=$ip_c"

# Each backend answers its own name, so a reply identifies which machine served
# it. Without that, "the VIP answers" cannot be told from "one machine answers
# and the balancer sends everything to it", which is the whole assertion.
echo "- each backend answers its own name on :80"
serve() {
  incus exec "feint-osc-$1" -- sh -c \
    "nohup sh -c 'while true; do printf \"HTTP/1.1 200 OK\r\nContent-Length: ${#1}\r\n\r\n$1\" | nc -l -p 80; done' >/dev/null 2>&1 &" \
    >/dev/null 2>&1
}
booted=""
all_up() {
  incus exec "feint-osc-$vm_a" -- true >/dev/null 2>&1 &&
    incus exec "feint-osc-$vm_b" -- true >/dev/null 2>&1 &&
    incus exec "feint-osc-$vm_c" -- true >/dev/null 2>&1
}
if wait_until 24 all_up; then booted="yes"; fi
[ -n "$booted" ] || fail "the machines never came up; nothing can be measured"
serve "$vm_a"
serve "$vm_b"

# The positive control, before any verdict is drawn from silence: each backend
# answers on its own address. A machine whose responder never started refuses a
# connection exactly as a balancer that forwards nothing does (#219).
#
# The three seconds this replaces were waiting for the two responders to bind,
# and that is exactly what the control below asks. Polling it first leaves both
# verdicts as they were: a responder that never binds still fails the suite.
from_client() { incus exec "feint-osc-$vm_c" -- wget -q -T 4 -O - "http://$1/" 2>/dev/null; }
a_answers() { [ "$(from_client "$ip_a")" = "$vm_a" ]; }
b_answers() { [ "$(from_client "$ip_b")" = "$vm_b" ]; }
wait_until 30 a_answers || fail "the first backend does not answer on its own address"
wait_until 30 b_answers || fail "the second backend does not answer on its own address"
ok "both backends answer directly"

echo "- an internal load balancer, its two machines registered"
lb="$(osc CreateLoadBalancer --LoadBalancerName "$lb_name" --LoadBalancerType internal \
      --Subnets "$sub_id" \
      --Listeners.0.LoadBalancerPort 80 --Listeners.0.LoadBalancerProtocol TCP \
      --Listeners.0.BackendPort 80 --Listeners.0.BackendProtocol TCP)" \
  || fail "CreateLoadBalancer rejected: $lb"
lb_made="yes"
vip="$(printf '%s' "$lb" | jq -r '.LoadBalancer.PrivateIp // empty')"
[ -n "$vip" ] || fail "the load balancer came back with no PrivateIp: $lb"
case "$vip" in
  "${SUBBLOCK%.*}."*) ;;
  *) fail "the balancer's PrivateIp $vip is outside its own Subnet $SUBBLOCK" ;;
esac
osc RegisterVmsInLoadBalancer --LoadBalancerName "$lb_name" --BackendVmIds "$vm_a" >/dev/null \
  || fail "RegisterVmsInLoadBalancer rejected the first machine"
osc RegisterVmsInLoadBalancer --LoadBalancerName "$lb_name" --BackendVmIds "$vm_b" >/dev/null \
  || fail "RegisterVmsInLoadBalancer rejected the second machine"
# The VIP answering is the condition, and every verdict below rests on it, so it
# is asked rather than assumed after two seconds.
vip_answers() { [ -n "$(from_client "$vip")" ]; }
wait_until 30 vip_answers || true
ok "$lb_name answers on $vip"

# probe: N connections from the client machine, printed as the names that
# answered. TIMEOUT is kept in the output rather than dropped, so a partial
# failure reads as one instead of looking like a lopsided split.
probe() {
  local n="$1" out=""
  for _ in $(seq 1 "$n"); do
    out="$out $(from_client "$vip" || echo TIMEOUT)"
  done
  printf '%s' "$out"
}

echo "- the balancer distributes to both machines"
hits="$(probe 6)"
case "$hits" in
  *TIMEOUT*) fail "the balancer did not answer every probe: $hits" ;;
esac
case "$hits" in *"$vm_a"*) ;; *) fail "no connection reached $vm_a: $hits" ;; esac
case "$hits" in *"$vm_b"*) ;; *) fail "no connection reached $vm_b: $hits" ;; esac
ok "6/6 answered, over both machines:$hits"

# The measurement #315 turned on. An address the runtime has to announce outside
# the network answers for two minutes and goes dark; an address of the network's
# own block does not, and that is the difference this whole capability rests on.
# Sixty seconds is short of the three minutes at which the other one died, and
# it is what a conformance run can afford; the full curve is in docs/limits.md.
echo "- and it is still there a minute later"
# waits on silence: this sixty seconds IS the measurement, not a wait for
# something to become true. #315 measured an address the runtime has to announce
# outside the network answering for two minutes and then going dark; the claim
# here is that an address of the network's OWN block does not. There is no
# condition to poll: the property is that nothing changes, and the only way to
# observe nothing changing is to let time pass. Shortening it shortens the
# claim. docs/limits.md carries the full curve.
sleep 60
hits="$(probe 6)"
case "$hits" in
  *TIMEOUT*) fail "the balancer went dark within a minute: $hits" ;;
esac
ok "6/6 answered again:$hits"

# The replace-not-patch rule, measured rather than asserted in a unit test: a
# machine the API has stopped listing must stop receiving connections.
echo "- an unlinked machine stops receiving connections"
osc UnlinkLoadBalancerBackendMachines --LoadBalancerName "$lb_name" --BackendVmIds "$vm_a" >/dev/null \
  || fail "UnlinkLoadBalancerBackendMachines rejected"
# waits on silence: the verdict below is that $vm_a NEVER answers again across
# six connections. Nothing announces an unlink reaching the runtime, and the
# only poll available — "six probes that avoided $vm_a" — is satisfied by luck
# one run in sixty-four with the unlink not applied at all. That is #559's
# defect with a different number, so this wait stays fixed.
sleep 2
hits="$(probe 6)"
case "$hits" in
  *TIMEOUT*) fail "the balancer stopped answering after an unlink: $hits" ;;
  *"$vm_a"*) fail "$vm_a still receives connections after being unlinked: $hits" ;;
esac
case "$hits" in *"$vm_b"*) ;; *) fail "the remaining machine receives nothing: $hits" ;; esac
ok "everything goes to $vm_b:$hits"

# The listener half of the same rule (#344), and the reason it belongs in this
# suite rather than in a unit test: the control plane can now move a listener,
# and a move that the API records while the runtime keeps the old port is the
# lie the whole dataplane axis exists to catch.
#
# This is the exact sequence every provider drives for a one-line port change —
# delete the departing front port, create the arriving one — so the balancer
# really does stand with no listener at all in between. Both ends are measured:
# the old port must stop answering, and the new one must start.
echo "- a moved listener moves the balancer with it"
osc DeleteLoadBalancerListeners --LoadBalancerName "$lb_name" --LoadBalancerPorts 80 >/dev/null \
  || fail "DeleteLoadBalancerListeners rejected"
osc CreateLoadBalancerListeners --LoadBalancerName "$lb_name" \
  --Listeners.0.LoadBalancerPort 8080 --Listeners.0.LoadBalancerProtocol TCP \
  --Listeners.0.BackendPort 80 --Listeners.0.BackendProtocol TCP >/dev/null \
  || fail "CreateLoadBalancerListeners rejected"

# The new port answers, from the same client machine, over the machine still
# registered. Asserted first, because it is the positive half: a suite that only
# checked the old port going quiet would pass on a balancer that was simply gone.
#
# The three seconds that stood here were redundant: the loop below already
# polled the same condition, so they were paid before the first look at it every
# single run.
moved=""
moved_answers() { moved="$(from_client "$vip:8080" || true)"; [ -n "$moved" ]; }
wait_until 10 moved_answers || true
[ "$moved" = "$vm_b" ] || fail "the balancer does not answer on its new port 8080: got '${moved:-nothing}'"
ok "8080 answers, served by $vm_b"

# And the old port is gone. `from_client` returns empty on a refused connection,
# which is what a front port nobody listens on gives.
still="$(from_client "$vip" || true)"
[ -z "$still" ] || fail "the balancer still answers on the port its listener left: $still"
ok "80 no longer answers: the runtime followed the control plane"

echo "- deleting the balancer takes it off the host"
network="$(incus network list -f csv 2>/dev/null | grep '^fnt-' | cut -d, -f1 | while read -r name; do
  addr="$(incus network get "$name" ipv4.address 2>/dev/null || true)"
  case "$addr" in "${SUBBLOCK%.*}."*) echo "$name"; break ;; esac
done)"
[ -n "$network" ] || fail "no host network carries $SUBBLOCK; the delete cannot be verified"
incus network load-balancer list "$network" -f csv 2>/dev/null | grep -q "$vip" \
  || fail "the runtime holds no balancer on $vip, so the probes above measured something else"
osc DeleteLoadBalancer --LoadBalancerName "$lb_name" >/dev/null || fail "DeleteLoadBalancer rejected"
lb_made=""
# A disappearance, which is the one negative a poll may end on: a balancer the
# runtime has deleted does not come back, so the first observation of its
# absence is the verdict the sleep would have reached. The verdict below is
# unchanged and still fails if it never goes.
host_holds_balancer() { incus network load-balancer list "$network" -f csv 2>/dev/null | grep -q "$vip"; }
wait_gone 30 host_holds_balancer || true
if host_holds_balancer; then
  fail "the balancer on $vip outlived the load balancer it belongs to"
fi
ok "the host holds no balancer on $vip"

echo "conformance: outscale load balancer dataplane passed"
