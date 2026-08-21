#!/usr/bin/env bash
# Conformance check: an Outscale load balancer distributes real packets (#315).
#
# oapi-cli.sh proves the configuration — listeners, backends, health-check
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
# The gate is the runtime's *declared* capability, never a mode name: a suite
# that compares mode strings has to be edited every time a driver gains one, and
# it lies the day a mode stops delivering it. An undeclared capability reads as
# absent and the suite skips.
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

command -v oapi-cli >/dev/null 2>&1 || { echo "FAIL: oapi-cli is not installed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }
command -v incus >/dev/null 2>&1 || { echo "FAIL: the incus client is not on PATH" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

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

BLOCK="${FEINT_TEST_LB_BLOCK:-10.186.0.0/16}"
SUBBLOCK="${FEINT_TEST_LB_SUBNET_BLOCK:-10.186.7.0/24}"

WORK="$(mktemp -d)"
chmod 700 "$WORK"

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
# shellcheck disable=SC2034 # read by oapi-cli from the environment
OSC_ENDPOINT_API="$ENDPOINT"
set +a
cat >"$WORK/config.json" <<JSON
{"default": {"access_key": "$OSC_ACCESS_KEY", "secret_key": "$OSC_SECRET_KEY",
 "protocol": "http", "region": "eu-west-2", "endpoints": {"api": "$ENDPOINT"}}}
JSON
osc() { oapi-cli --config "$WORK/config.json" "$@"; }

lb_name="feint-lbu-data"
net_id=""; sub_id=""; vm_a=""; vm_b=""; vm_c=""; lb_made=""

cleanup() {
  [ -n "$lb_made" ] && osc DeleteLoadBalancer --LoadBalancerName "$lb_name" >/dev/null 2>&1
  for vm in "$vm_a" "$vm_b" "$vm_c"; do
    [ -n "$vm" ] && osc DeleteVms '--VmIds[]' "$vm" >/dev/null 2>&1
  done
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
for _ in 1 2 3 4 5 6 7 8 9 10 11 12; do
  if incus exec "feint-osc-$vm_a" -- true >/dev/null 2>&1 &&
     incus exec "feint-osc-$vm_b" -- true >/dev/null 2>&1 &&
     incus exec "feint-osc-$vm_c" -- true >/dev/null 2>&1; then booted="yes"; break; fi
  sleep 2
done
[ -n "$booted" ] || fail "the machines never came up; nothing can be measured"
serve "$vm_a"
serve "$vm_b"
sleep 3

# The positive control, before any verdict is drawn from silence: each backend
# answers on its own address. A machine whose responder never started refuses a
# connection exactly as a balancer that forwards nothing does (#219).
from_client() { incus exec "feint-osc-$vm_c" -- wget -q -T 4 -O - "http://$1/" 2>/dev/null; }
[ "$(from_client "$ip_a")" = "$vm_a" ] || fail "the first backend does not answer on its own address"
[ "$(from_client "$ip_b")" = "$vm_b" ] || fail "the second backend does not answer on its own address"
ok "both backends answer directly"

echo "- an internal load balancer, its two machines registered"
lb="$(osc CreateLoadBalancer --LoadBalancerName "$lb_name" --LoadBalancerType internal \
      '--Subnets[]' "$sub_id" \
      --Listeners "[{\"LoadBalancerPort\": 80, \"LoadBalancerProtocol\": \"TCP\", \"BackendPort\": 80, \"BackendProtocol\": \"TCP\"}]")" \
  || fail "CreateLoadBalancer rejected: $lb"
lb_made="yes"
vip="$(printf '%s' "$lb" | jq -r '.LoadBalancer.PrivateIp // empty')"
[ -n "$vip" ] || fail "the load balancer came back with no PrivateIp: $lb"
case "$vip" in
  "${SUBBLOCK%.*}."*) ;;
  *) fail "the balancer's PrivateIp $vip is outside its own Subnet $SUBBLOCK" ;;
esac
osc RegisterVmsInLoadBalancer --LoadBalancerName "$lb_name" '--BackendVmIds[]' "$vm_a" >/dev/null \
  || fail "RegisterVmsInLoadBalancer rejected the first machine"
osc RegisterVmsInLoadBalancer --LoadBalancerName "$lb_name" '--BackendVmIds[]' "$vm_b" >/dev/null \
  || fail "RegisterVmsInLoadBalancer rejected the second machine"
sleep 2
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
sleep 60
hits="$(probe 6)"
case "$hits" in
  *TIMEOUT*) fail "the balancer went dark within a minute: $hits" ;;
esac
ok "6/6 answered again:$hits"

# The replace-not-patch rule, measured rather than asserted in a unit test: a
# machine the API has stopped listing must stop receiving connections.
echo "- an unlinked machine stops receiving connections"
osc UnlinkLoadBalancerBackendMachines --LoadBalancerName "$lb_name" '--BackendVmIds[]' "$vm_a" >/dev/null \
  || fail "UnlinkLoadBalancerBackendMachines rejected"
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
osc DeleteLoadBalancerListeners --LoadBalancerName "$lb_name" '--LoadBalancerPorts[]' 80 >/dev/null \
  || fail "DeleteLoadBalancerListeners rejected"
osc CreateLoadBalancerListeners --LoadBalancerName "$lb_name" \
  --Listeners "[{\"LoadBalancerPort\": 8080, \"LoadBalancerProtocol\": \"TCP\", \"BackendPort\": 80, \"BackendProtocol\": \"TCP\"}]" >/dev/null \
  || fail "CreateLoadBalancerListeners rejected"
sleep 3

# The new port answers, from the same client machine, over the machine still
# registered. Asserted first, because it is the positive half: a suite that only
# checked the old port going quiet would pass on a balancer that was simply gone.
moved=""
for _ in 1 2 3 4 5; do
  moved="$(from_client "$vip:8080" || true)"
  [ -n "$moved" ] && break
  sleep 2
done
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
sleep 2
if incus network load-balancer list "$network" -f csv 2>/dev/null | grep -q "$vip"; then
  fail "the balancer on $vip outlived the load balancer it belongs to"
fi
ok "the host holds no balancer on $vip"

echo "conformance: outscale load balancer dataplane passed"
