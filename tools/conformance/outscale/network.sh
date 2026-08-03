#!/usr/bin/env bash
# Conformance check: an Outscale Subnet is a real network, not an answer.
#
# oapi-cli.sh proves the arithmetic — masks bounded, containment enforced,
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

command -v oapi-cli >/dev/null 2>&1 || { echo "FAIL: oapi-cli is not installed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

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

# Deliberately obscure. A lab bridge or a VPN already holding this range makes
# the create fail, which is the emulator being honest rather than handing back a
# network that exists nowhere.
BLOCK="${FEINT_TEST_NET_BLOCK:-10.182.0.0/16}"
SUBBLOCK="${FEINT_TEST_SUBNET_BLOCK:-10.182.9.0/24}"

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
# The endpoint comes from the argument, not from the credentials file: oapi-cli
# lets the environment override --config, so a pinned value there silently wins
# over the port this run was asked to measure.
# shellcheck disable=SC2034 # read by oapi-cli from the environment, not here
OSC_ENDPOINT_API="$ENDPOINT"
set +a

# And it must be set, because an unset one sends oapi-cli looking for the
# operator's stored profile. guard_local checked where we intend to go; this
# checks the client cannot go anywhere else.
guard_no_real_profile OSC_ENDPOINT_API oapi-cli

WORK="$(mktemp -d)"
cat > "$WORK/config.json" <<EOF
{
  "default": {
    "access_key": "$OSC_ACCESS_KEY",
    "secret_key": "$OSC_SECRET_KEY",
    "region": "$OSC_REGION",
    "protocol": "http",
    "endpoints": { "api": "$ENDPOINT" }
  }
}
EOF
osc() { oapi-cli --config "$WORK/config.json" "$@"; }

net_id=""
sub_id=""
# Delete through the API, which is what removes the backing network. Killing the
# bridge directly would hide a leak in the emulator behind the cleanup.
cleanup() {
  [ -n "$sub_id" ] && osc DeleteSubnet --SubnetId "$sub_id" >/dev/null 2>&1
  [ -n "$net_id" ] && osc DeleteNet --NetId "$net_id" >/dev/null 2>&1
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

# The address must be on the machine, not only in the answer. Waiting because a
# container gets its address a few seconds after it starts.
carried=""
for _ in 1 2 3 4 5 6 7 8 9 10; do
  if incus list -f csv -c n4 2>/dev/null | grep -q "$private_ip"; then
    carried="yes"
    break
  fi
  sleep 2
done
[ -n "$carried" ] || fail "no machine on the host carries $private_ip: the address is a number in a store"
ok "$vm_id carries $private_ip, on $found"

osc DeleteVms '--VmIds[]' "$vm_id" >/dev/null || fail "DeleteVms rejected"
sleep 2

echo "- the network goes when the Subnet goes"
osc DeleteSubnet --SubnetId "$sub_id" >/dev/null || fail "DeleteSubnet rejected"
sub_id=""
sleep 1
if incus network list -f csv 2>/dev/null | grep -q "^$found,"; then
  fail "the host network $found outlived its Subnet"
fi
ok "deleted, and the host is as it was"

osc DeleteNet --NetId "$net_id" >/dev/null || fail "DeleteNet rejected"
net_id=""

echo "conformance: outscale network passed"
