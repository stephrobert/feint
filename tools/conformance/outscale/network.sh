#!/usr/bin/env bash
# Conformance check: an Outscale Subnet is a real network, not an answer.
#
# octl.sh proves the arithmetic — masks bounded, containment enforced,
# overlap refused, the address count computed. All of that runs with no machine
# runtime at all, so all of it could be true of a server that stores JSON and
# nothing else. This suite measures the other half: the block a client declares
# is a block that exists on the host, with the range it asked for, and it goes
# away when the Subnet does.
#
# That distinction is the whole reason the addressing is served. Every other
# local cloud emulator stops at the answer.
#
# Requires a machine runtime: `feint serve --vm incus`. With --vm off the suite
# skips itself, so CI stays runtime-free.
#
# Usage: tools/conformance/outscale/network.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Never let a client reach anything but the local emulator. Without this, a
# missing endpoint does not fail: every official client falls back to the
# operator's stored credentials, and a test creates billable resources on a real
# account. That is not hypothetical — it happened, to this repository.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../guard.sh"
guard_local "$ENDPOINT"

command -v octl >/dev/null 2>&1 || { echo "FAIL: octl is not installed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

# Shared assertions about what a machine carries. See the file for why the
# comparison is not written three times.
# shellcheck source=/dev/null
. "$(dirname "$0")/../shared/addresses.sh"
# shellcheck source=/dev/null
. "$(dirname "$0")/../shared/verdicts.sh"
# Waiting for a condition instead of for a duration (#459). The file states the
# one rule that decides where these may stand, and why the waits still written
# `sleep` below cannot become polls.
# shellcheck source=/dev/null
. "$(dirname "$0")/../shared/waiting.sh"

echo "conformance: outscale network against $ENDPOINT"

MACHINES="$(curl -sf "$ENDPOINT/_feint/health" | jq -r '.machines')"
if [ "$MACHINES" = "none" ]; then
  skip "no machine runtime (start with FEINT_VM=incus); nothing to measure"
  exit 0
fi

# The runtime CLI is how the host is inspected. Asking the emulator whether it
# created a network would be asking the accused for the verdict.
#
# It is `incus` for all three modes. The first version derived it from FEINT_VM
# and stripped "-vm", which left "incus-ovn" untouched: the whole suite then
# failed with "the runtime CLI incus-ovn is not on PATH" in the one mode that
# delivers isolation between two VPCs, right after the Scaleway suite had proved
# that isolation in the same run. CLAUDE.md says a mode declares what it can do
# rather than having it deduced from its name; deducing a *tool* from that name
# is the same mistake one level down. The sibling suite gets it right by not
# deriving anything.
command -v incus >/dev/null 2>&1 \
  || fail "the incus client is not on PATH, so nothing can be verified"

# And the host must not already be holding a block this suite is about to ask
# for (#375). Nothing has been created at this point, so refusing here costs
# the host nothing — which is the property the two sibling suites get by
# putting the same call after their EXIT trap.
guard_leftovers "$ENDPOINT"

# Deliberately obscure. A lab bridge or a VPN already holding this range makes
# the create fail, which is the emulator being honest rather than handing back a
# network that exists nowhere.
BLOCK="${FEINT_TEST_NET_BLOCK:-10.182.0.0/16}"
SUBBLOCK="${FEINT_TEST_SUBNET_BLOCK:-10.182.9.0/24}"

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
# The endpoint comes from the argument, not from the credentials file, and it
# carries /api/v1: octl reads that from osc-sdk-go, whose default endpoint is
# https://api.<region>.outscale.com/api/v1, so the path is part of the value.
# The archived oapi-cli this replaced wanted the bare host (#460).
# shellcheck disable=SC2034 # read by octl from the environment, not here
OSC_ENDPOINT_API="$ENDPOINT/api/v1"
set +a

# And it must be set, because an unset one sends octl looking for the operator's
# stored profile. guard_local checked where we intend to go; this checks the
# client cannot go anywhere else.
guard_no_real_profile OSC_ENDPOINT_API octl

WORK="$(mktemp -d)"
cat > "$WORK/config.json" <<EOF
{
  "default": {
    "access_key": "$OSC_ACCESS_KEY",
    "secret_key": "$OSC_SECRET_KEY",
    "region": "$OSC_REGION",
    "protocol": "http",
    "endpoints": { "api": "$ENDPOINT/api/v1" }
  }
}
EOF
# `iaas api <Call>` rather than an alias, `-o raw` so the body is the API's own
# and not the CLI's rearrangement of it, `</dev/null` because octl reads stdin as
# the request body whenever stdin is not a terminal. tools/conformance/outscale/
# octl.sh states all three at length.
osc() { octl --config "$WORK/config.json" --no-upgrade -o raw iaas api "$@" </dev/null; }

net_id=""
sub_id=""
vm_a=""
vm_b=""
born_vm=""
sub_a=""
sub_b=""
born_sub=""
net_a=""
net_b=""
# Delete through the API, which is what removes the backing network. Killing the
# bridge directly would hide a leak in the emulator behind the cleanup.
cleanup() {
  [ -n "$vm_a" ] && osc DeleteVms --VmIds "$vm_a" >/dev/null 2>&1
  [ -n "$vm_b" ] && osc DeleteVms --VmIds "$vm_b" >/dev/null 2>&1
  [ -n "$born_vm" ] && osc DeleteVms --VmIds "$born_vm" >/dev/null 2>&1
  # waits on silence: a subnet cannot be deleted while a machine still sits on
  # it, and this is a trap handler — the ids may be empty, the deletes may have
  # been refused, so there is no single object whose disappearance to wait for.
  sleep 2
  [ -n "$sub_id" ] && osc DeleteSubnet --SubnetId "$sub_id" >/dev/null 2>&1
  [ -n "$sub_a" ] && osc DeleteSubnet --SubnetId "$sub_a" >/dev/null 2>&1
  [ -n "$sub_b" ] && osc DeleteSubnet --SubnetId "$sub_b" >/dev/null 2>&1
  [ -n "$born_sub" ] && osc DeleteSubnet --SubnetId "$born_sub" >/dev/null 2>&1
  [ -n "$net_id" ] && osc DeleteNet --NetId "$net_id" >/dev/null 2>&1
  [ -n "$net_a" ] && osc DeleteNet --NetId "$net_a" >/dev/null 2>&1
  [ -n "$net_b" ] && osc DeleteNet --NetId "$net_b" >/dev/null 2>&1
  rm -rf "$WORK"
}
trap cleanup EXIT

echo "- a Subnet is backed by a network on the host"
net="$(osc CreateNet --IpRange "$BLOCK")" || fail "CreateNet rejected: $net"
net_id="$(printf '%s' "$net" | jq -r '.Net.NetId')"
sub="$(osc CreateSubnet --NetId "$net_id" --IpRange "$SUBBLOCK")" \
  || fail "CreateSubnet rejected: $sub"
sub_id="$(printf '%s' "$sub" | jq -r '.Subnet.SubnetId')"

# The name is derived from the Subnet id by the core, so it is found rather than
# guessed: list what the runtime holds and require exactly one new network whose
# range is the one asked for.
found=""
while IFS= read -r line; do
  name="${line%%,*}"
  addr="$(incus network get "$name" ipv4.address 2>/dev/null || true)"
  if [ "$addr" != "" ] && [ "${addr#*/}" = "${SUBBLOCK#*/}" ]; then
    case "$addr" in
      "${SUBBLOCK%.*}."*) found="$name" ;;
    esac
  fi
done < <(incus network list -f csv 2>/dev/null | grep '^fnt-' || true)

[ -n "$found" ] || fail "no host network carries $SUBBLOCK: the Subnet is an answer, not a network"
ok "$sub_id is backed by $found on ${SUBBLOCK}"

# The gateway must sit inside the block a client declared. A runtime that
# silently renumbers gives a machine an address the API never published, which is
# precisely the failure floci ships: it computes an address from the CIDR, then
# overwrites it with the bridge's own.
gw="$(incus network get "$found" ipv4.address)"
[ "${gw#*/}" = "${SUBBLOCK#*/}" ] \
  || fail "the host network carries mask /${gw#*/}, the Subnet declared /${SUBBLOCK#*/}"
ok "the mask on the host is the mask the client asked for"

# A Subnet nothing can be placed on is a demo. The pack declared SubnetId on
# CreateVms and read nothing, so a client got a 200 and a Vm that was nowhere.
echo "- a Vm placed on the Subnet carries the address the API published"
vm="$(osc CreateVms --ImageId ami-00000001 --VmType tinav6.c1r1p2 --SubnetId "$sub_id")" \
  || fail "CreateVms rejected a SubnetId: $vm"
vm_id="$(printf '%s' "$vm" | jq -r '.Vms[0].VmId')"
private_ip="$(printf '%s' "$vm" | jq -r '.Vms[0].PrivateIp // empty')"
[ -n "$private_ip" ] || fail "the Vm came back without a PrivateIp: $vm"
printf '%s' "$vm" | jq -e --arg s "$sub_id" '.Vms[0].SubnetId == $s' >/dev/null \
  || fail "the Vm does not report the Subnet it was created in: $vm"
# Allocated from the same allocator the Subnet's own count comes from, so the
# first address on offer is the one after the four reserved ones.
case "$private_ip" in
  "${SUBBLOCK%.*}."*) ;;
  *) fail "PrivateIp $private_ip is outside the Subnet $SUBBLOCK" ;;
esac

# The address must be on the machine, not only in the answer. Asked until it
# holds rather than every two seconds: the same budget, a quarter-second
# granularity, and the verdict below unchanged.
carried=""
if wait_until 20 machine_carries "feint-osc-$vm_id" "$private_ip"; then carried="yes"; fi
[ -n "$carried" ] || fail "feint-osc-$vm_id does not carry $private_ip: the address is a number in a store"
ok "$vm_id carries $private_ip, on $found"

echo "- the machine carries no address the API does not publish"
# Outscale publishes a Vm's addresses as PrivateIp and, when one is linked,
# PublicIp. Nothing else on the machine may exist: an address the runtime handed
# out and no field describes is exactly what #202 removed.
osc_published="$(osc ReadVms --Filters.VmIds "$vm_id" \
  | jq -r '[.Vms[0].PrivateIp, .Vms[0].PublicIp] | map(select(. != null and . != "")) | join(" ")')"
# shellcheck disable=SC2086 # the published list is several arguments on purpose
assert_only_published "feint-osc-$vm_id" $osc_published

osc DeleteVms --VmIds "$vm_id" >/dev/null || fail "DeleteVms rejected"
# The delete of the Subnet below is refused while a machine still sits on it, so
# what the two seconds were for is the machine being gone — an object whose
# disappearance can be asked for rather than waited out.
machine_exists() { incus info "$1" >/dev/null 2>&1; }
wait_gone 30 machine_exists "feint-osc-$vm_id" || true

echo "- the network goes when the Subnet goes"
osc DeleteSubnet --SubnetId "$sub_id" >/dev/null || fail "DeleteSubnet rejected"
sub_id=""
# A disappearance is the one negative a poll may end on: a network the runtime
# has deleted does not come back, so the first observation of its absence is the
# same verdict a fixed sleep would have reached, only sooner. The verdict below
# is unchanged and still fails if it never goes.
host_lists_network() { incus network list -f csv 2>/dev/null | grep -q "^$1,"; }
wait_gone 30 host_lists_network "$found" || true
if host_lists_network "$found"; then
  fail "the host network $found outlived its Subnet"
fi
ok "deleted, and the host is as it was"

osc DeleteNet --NetId "$net_id" >/dev/null || fail "DeleteNet rejected"
net_id=""

# ---- Net peering: the claim, and the only mode that can carry it -------------
#
# docs/roadmap.md's rule for this family: under OVN the claim is asserted,
# elsewhere it is skipped, and no document says "peered" without naming the
# mode. The gate is the runtime's *declared* capability, never the mode's name:
# a suite that compares a mode string has to be edited every time a driver
# gains a capability, and it lies the day a mode stops delivering one.
#
# What is asserted, in order: two Nets are unreachable from each other before
# any peering (the isolation the capability declares), still unreachable while
# the peering is only pending-acceptance (a request grants nothing), reachable
# once accepted, and unreachable again once deleted. The control plane's
# answers are proven by the octl suite with no runtime at all; this is the
# other half — the packets.
ISOLATION="$(curl -sf "$ENDPOINT/_feint/health" | jq -r '.capabilities.isolation')"
if [ "$ISOLATION" != "true" ]; then
  skip "this runtime does not declare isolation; two Nets already reach each other, so an accepted peering would prove nothing (bridge mode, docs/limits.md)"
  echo "conformance: outscale network passed"
  exit 0
fi

echo "- two Nets, a machine in each"
PEER_BLOCK_A="${FEINT_TEST_PEER_BLOCK_A:-10.183.0.0/16}"
PEER_BLOCK_B="${FEINT_TEST_PEER_BLOCK_B:-10.184.0.0/16}"
net_a="$(osc CreateNet --IpRange "$PEER_BLOCK_A" | jq -r '.Net.NetId')"
net_b="$(osc CreateNet --IpRange "$PEER_BLOCK_B" | jq -r '.Net.NetId')"
[ -n "$net_a" ] && [ -n "$net_b" ] || fail "the two Nets were not created"
sub_a="$(osc CreateSubnet --NetId "$net_a" --IpRange "${PEER_BLOCK_A%.0.0/16}.1.0/24" | jq -r '.Subnet.SubnetId')"
sub_b="$(osc CreateSubnet --NetId "$net_b" --IpRange "${PEER_BLOCK_B%.0.0/16}.1.0/24" | jq -r '.Subnet.SubnetId')"
[ -n "$sub_a" ] && [ -n "$sub_b" ] || fail "the two Subnets were not created"

vm_a_doc="$(osc CreateVms --ImageId ami-00000003 --VmType tinav6.c1r1p2 --SubnetId "$sub_a")" \
  || fail "CreateVms rejected in $sub_a: $vm_a_doc"
vm_b_doc="$(osc CreateVms --ImageId ami-00000003 --VmType tinav6.c1r1p2 --SubnetId "$sub_b")" \
  || fail "CreateVms rejected in $sub_b: $vm_b_doc"
vm_a="$(printf '%s' "$vm_a_doc" | jq -r '.Vms[0].VmId')"
vm_b="$(printf '%s' "$vm_b_doc" | jq -r '.Vms[0].VmId')"
ip_b="$(printf '%s' "$vm_b_doc" | jq -r '.Vms[0].PrivateIp // empty')"
[ -n "$ip_b" ] || fail "the target Vm came back without a PrivateIp"

# The machine must be up and carrying its address before absence of reach can
# mean isolation rather than a machine still booting.
#
# 24 IS NOT A MEASUREMENT, AND RAISING IT WAS THE WRONG FIX (#587). It is the
# `for _ in 1..12; do sleep 2; done` this line replaced, transcribed. This wait
# expired at 24.816 s on the maintainer's station while CI passed the same
# suite, and three measurements said the budget was never the lever: at the end
# of a 90 s wait the guest's DHCP client was gone, a `udhcpc` exchange in that
# same guest was answered in 97–143 ms, and the alpine machine never took its
# address at 45 s, 90 s or 180 s. The defect was in the driver's first-boot
# door, docs/limits.md carries the decomposition, and with it fixed the address
# is on the machine 31–36 ms after the create answers. The number below is left
# where it was because nothing measured asks it to move; WAIT_TRACE in
# shared/waiting.sh is how the next reader re-derives it instead of guessing.
booted=""
if wait_until 24 machine_carries "feint-osc-$vm_b" "$ip_b"; then booted="yes"; fi
[ -n "$booted" ] || fail "feint-osc-$vm_b does not carry $ip_b; cannot measure reachability"

# reach: from the machine in Net A towards the address in Net B. The names are
# the binding's, prefix feint-osc-.
reach() { incus exec "feint-osc-$vm_a" -- ping -c 2 -W 2 "$ip_b" >/dev/null 2>&1; }

# The rule a real user would write, posed BEFORE the negative checks (#499).
# Both Vms carry the default group of their own Net, whose inbound accepts the
# group's own members and nobody else — measured on a real account, and the
# head comment of internal/providers/outscale/securitygroups.go. So without an
# explicit allow, the accepted peering below would still not carry this ping,
# on the real cloud exactly as here; the check was only ever green while no
# group reached the runtime (before #494). Placing the allow first also makes
# the refusals stronger: an explicit allow that still does not cross an
# unpeered or pending Net is the strongest form of those refusals, because the
# isolation rejects at 400 dominate any allow at 300.
sg_b="$(osc ReadSecurityGroups --Filters.NetIds "$net_b" --Filters.SecurityGroupNames default \
        | jq -r '.SecurityGroups[0].SecurityGroupId // empty')"
[ -n "$sg_b" ] || fail "the default group of $net_b was not found"
osc CreateSecurityGroupRule --Flow Inbound --SecurityGroupId "$sg_b" \
    --IpProtocol icmp --IpRange "$PEER_BLOCK_A" >/dev/null \
  || fail "CreateSecurityGroupRule rejected on $sg_b"
# waits on silence: the allow above must have reached the NIC before the four
# refusals below are attributed to isolation. Nothing announces that it has, and
# the only observable consequence of the rule landing is a ping SUCCEEDING
# across the peering — which is precisely what must not happen yet. An allow
# that has not landed makes each of those refusals true for a weaker reason
# than the one this block claims, so this wait stays fixed.
sleep 2

# The positive control, before four negative verdicts in a row: the target answers
# on its own address. A machine whose stack never came up refuses a ping exactly
# as an unpeered Net does, and this suite draws its strongest conclusion from that
# refusal (#219).
assert_answers_itself_within 30 "feint-osc-$vm_b" "$ip_b" "the Vm of the second Net"

echo "- before any peering, the Nets do not reach each other"
if reach; then
  fail "$vm_a reaches $ip_b across two unpeered Nets; the declared isolation does not hold"
fi
ok "unreachable, as the isolation capability declares"

echo "- a pending peering grants nothing"
pcx_id="$(osc CreateNetPeering --SourceNetId "$net_a" --AccepterNetId "$net_b" \
          | jq -r '.NetPeering.NetPeeringId')"
[ -n "$pcx_id" ] || fail "CreateNetPeering answered no id"
if reach; then
  fail "a pending-acceptance peering already carries traffic"
fi
ok "still unreachable while pending-acceptance"

echo "- an accepted peering carries traffic, both ends knowing it"
osc AcceptNetPeering --NetPeeringId "$pcx_id" >/dev/null || fail "AcceptNetPeering rejected"
wait_until 30 reach || fail "the peering is active and $vm_a still cannot reach $ip_b"
ok "$vm_a reaches $ip_b through the active peering"

# The regression #508 measured, held in the same pass as the lifecycle above:
# two reconcilers used to write the runtime's peer state with two different
# truths — "same Net" on every subnet transition, "active peering" on every
# peering transition — and the runtime reconciles rather than appends, so an
# ordinary CreateSubnet in a peered Net severed the active peering, and the
# newborn subnet never joined it. Both halves are asserted here on packets,
# and the deleted-peering verdict below now runs after this create, so it is
# also the negative control: the widening must not leave anything joined once
# the peering is gone, the newborn included.
echo "- a Subnet created while the peering is active does not sever it (#508)"
born_sub="$(osc CreateSubnet --NetId "$net_a" --IpRange "${PEER_BLOCK_A%.0.0/16}.3.0/24" | jq -r '.Subnet.SubnetId')"
[ -n "$born_sub" ] || fail "CreateSubnet was refused while the peering is active"
wait_until 30 reach \
  || fail "an ordinary CreateSubnet in $net_a severed the active peering: $vm_a no longer reaches $ip_b (#508)"
ok "the existing machine still reaches $ip_b after the create"

echo "- a machine born in that Subnet joins the active peering (#508)"
born_doc="$(osc CreateVms --ImageId ami-00000003 --VmType tinav6.c1r1p2 --SubnetId "$born_sub")" \
  || fail "CreateVms rejected in $born_sub: $born_doc"
born_vm="$(printf '%s' "$born_doc" | jq -r '.Vms[0].VmId')"
born_ip="$(printf '%s' "$born_doc" | jq -r '.Vms[0].PrivateIp // empty')"
[ -n "$born_ip" ] || fail "the newborn Vm came back without a PrivateIp"
born_up=""
if wait_until 24 machine_carries "feint-osc-$born_vm" "$born_ip"; then born_up="yes"; fi
[ -n "$born_up" ] || fail "feint-osc-$born_vm does not carry $born_ip; cannot measure the newborn's reachability"
born_reach() { incus exec "feint-osc-$born_vm" -- ping -c 2 -W 3 "$ip_b" >/dev/null 2>&1; }
wait_until 30 born_reach \
  || fail "the newborn Subnet's machine never joined the active peering: $born_vm cannot reach $ip_b (#508)"
ok "$born_vm reaches $ip_b through the peering it was born into"

echo "- a deleted peering separates them again"
osc DeleteNetPeering --NetPeeringId "$pcx_id" >/dev/null || fail "DeleteNetPeering rejected"
# waits on silence: both verdicts below are drawn from a ping that does NOT come
# back, and nothing announces a withdrawn peering reaching the runtime. A poll
# ending at the first failed ping would end on the ping that failed because the
# route was still being torn down, which is a pass drawn from a race (#559).
sleep 2
if reach; then
  fail "the peering is deleted and the Nets still reach each other"
fi
if incus exec "feint-osc-$born_vm" -- ping -c 2 -W 2 "$ip_b" >/dev/null 2>&1; then
  fail "the peering is deleted and the newborn Subnet's machine still reaches $ip_b"
fi
ok "unreachable again, the newborn included"

# The id is kept before the variable is cleared: the wait is on THIS machine
# going, and `feint-osc-` with nothing after it is a name incus never held, so
# the wait would end at once having asked about nothing.
gone_vm="$born_vm"
osc DeleteVms --VmIds "$born_vm" >/dev/null 2>&1 && born_vm=""
wait_gone 30 machine_exists "feint-osc-$gone_vm" || true
osc DeleteSubnet --SubnetId "$born_sub" >/dev/null 2>&1 && born_sub=""

# And the accepting half, which the peering lifecycle above does not cover: a
# rule set that kept everything out would pass every check so far and separate
# two Subnets of one Net, which the real cloud routes.
echo "- a second Subnet of the same Net stays reachable"
same_sub="$(osc CreateSubnet --NetId "$net_a" --IpRange "${PEER_BLOCK_A%.0.0/16}.2.0/24" | jq -r '.Subnet.SubnetId')"
if [ -z "$same_sub" ]; then
  skip "a second Subnet of the same Net was refused; the accepting half is not measured"
else
  same_doc="$(osc CreateVms --ImageId ami-00000003 --VmType tinav6.c1r1p2 --SubnetId "$same_sub")"
  same_vm="$(printf '%s' "$same_doc" | jq -r '.Vms[0].VmId')"
  same_ip="$(printf '%s' "$same_doc" | jq -r '.Vms[0].PrivateIp // empty')"
  wait_until 60 machine_carries "feint-osc-$same_vm" "$same_ip" || true
  # The target answers its own address before anything concludes about the Net.
  # Without this, a Vm whose stack was not up produced the same silence as a Net
  # that separates too much, and the verdict below accused the second — the
  # measurement that cost an `evidence:update` pass on 2026-08-27, in the
  # Exoscale suite, where this line was also missing. `assert_answers_itself_within`
  # is already used twenty lines above for the Vm of the *other* Net; the lesson
  # had been learned once in this very file and not carried to the second verdict.
  assert_answers_itself_within 30 "feint-osc-$same_vm" "$same_ip" "the Vm of the same Net"
  same_reach() { incus exec "feint-osc-$vm_a" -- ping -c 2 -W 3 "$same_ip" >/dev/null 2>&1; }
  if wait_until 30 same_reach; then
    ok "a machine of the same Net is reachable ($same_ip)"
  else
    fail "$same_ip is unreachable inside one Net; the isolation separates too much"
  fi
  osc DeleteVms --VmIds "$same_vm" >/dev/null 2>&1
  wait_gone 30 machine_exists "feint-osc-$same_vm" || true
  osc DeleteSubnet --SubnetId "$same_sub" >/dev/null 2>&1
fi

gone_a="$vm_a"
gone_b="$vm_b"
osc DeleteVms --VmIds "$vm_a" >/dev/null && vm_a=""
osc DeleteVms --VmIds "$vm_b" >/dev/null && vm_b=""
wait_gone 30 machine_exists "feint-osc-$gone_a" || true
wait_gone 30 machine_exists "feint-osc-$gone_b" || true
osc DeleteSubnet --SubnetId "$sub_a" >/dev/null && sub_a=""
osc DeleteSubnet --SubnetId "$sub_b" >/dev/null && sub_b=""
osc DeleteNet --NetId "$net_a" >/dev/null && net_a=""
osc DeleteNet --NetId "$net_b" >/dev/null && net_b=""



echo "conformance: outscale network passed"
