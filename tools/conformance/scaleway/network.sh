#!/usr/bin/env bash
# Conformance check: the emulated network is a real network.
#
# Every other local cloud emulator answers "here is your subnet, here is your address" and nothing
# carries it: floci computes an address from the subnet CIDR and then overwrites it with the
# container's bridge address, MiniStack starts no machine at all. So the assertions here are not
# about what the API returns, they are about what the machines do with it.
#
# Four things are measured. The block a client asks for is the block it gets. The address the API
# publishes is the address the machine carries. A security group's default policy closes a port,
# for real. And authorising that port afterwards opens it without restarting anything.
#
# Requires a machine runtime that can enforce rules: `feint serve --vm incus` with Incus 6.0.4 or
# later. Older runtimes reject security.acls on a bridged NIC, and the firewall steps are skipped
# rather than silently passing. With --vm off the whole suite is skipped.
#
# Usage: tools/conformance/scaleway/network.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"
ZONE="${ZONE:-fr-par-1}"
REGION="${REGION:-fr-par}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

# Never let a client reach anything but the local emulator. Without this, a
# missing endpoint does not fail: every official client falls back to the
# operator's stored credentials, and a test creates billable resources on a real
# account. That is not hypothetical — it happened, to this repository.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../guard.sh"
guard_local "$ENDPOINT"

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
set +a
export SCW_API_URL="$ENDPOINT"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok()   { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

# Shared assertions about what a machine carries. See the file for why the
# comparison is not written three times.
# shellcheck source=/dev/null
. "$(dirname "$0")/../shared/addresses.sh"
# shellcheck source=/dev/null
. "$(dirname "$0")/../shared/verdicts.sh"

api() { curl -sf -H 'Content-Type: application/json' "$@"; }

# The emulated block. Deliberately obscure: a lab bridge or a VPN holding the same range on the
# operator's host makes the create fail, which is the emulator being honest rather than handing
# back a network that exists nowhere.
BLOCK="${FEINT_TEST_BLOCK:-10.181.0.0/24}"

# The binary under test, which is also what sweeps the runtime. The check is not
# ceremony: this path was one directory too short after the suite moved under
# tools/conformance/scaleway/, every sweep failed, `|| true` swallowed it, and
# the runs went on passing while leaving machines and networks behind. A path
# that no longer resolves must say so rather than turn the cleanup into a no-op.
FEINT_BIN="${FEINT_BIN:-$SCRIPT_DIR/../../../feint}"
[ -x "$FEINT_BIN" ] || fail "no feint binary at $FEINT_BIN (build it: mise run build)"

echo "conformance: network against $ENDPOINT"

# The driver name decides which assertions can be hard: OVN-backed subnets are
# isolated by construction, bridge-backed ones are not (docs/limits.md).
health="$(curl -sf "$ENDPOINT/_feint/health")"
MACHINES="$(printf '%s' "$health" | jq -r '.machines')"
if [ "$MACHINES" = "none" ]; then
  skip "no machine runtime (start with FEINT_VM=incus); nothing to measure"
  exit 0
fi

# What this runtime claims it can deliver, asked rather than inferred from the
# mode name. A suite that compares a mode string in hard code has to be edited
# every time a driver gains a capability; one that reads the declaration does
# not, and it fails honestly when a mode claims something it stops delivering.
ISOLATION="$(printf '%s' "$health" | jq -r '.capabilities.isolation')"

# The list lives in a file rather than in an array, and that is the fix for a
# sweep that swept nothing (#219): boot() ends with `echo "$id"` and every caller
# writes `id="$(boot …)"`, which is a subshell — so `cleanup_ids+=("$id")` inside
# it updated a copy that died with the subshell, and the trap ran on an empty
# list. Every machine the suite created stayed on the operator's host, and the
# suite reported a clean exit.
cleanup_init

# The emulator removes its own leftovers: it labels everything it creates, so the
# sweep is exact where matching names would be a guess. Running it before as well
# as after is not tidiness. A network kept from an interrupted run holds a route
# towards an address the next run allocates again, and an instance whose network
# was swept underneath it lands in the state the runtime calls ERROR. Both were
# observed before this existed.
#
# What it removed is printed rather than discarded. A sweep is the one step whose
# success and whose total failure look identical from the outside, and this suite
# has already shipped both.
sweep_runtime() {
  local out
  if ! out="$("$FEINT_BIN" clean --vm "${FEINT_VM:-incus}" 2>&1)"; then
    echo "FAIL: the runtime sweep failed, resources are left behind: $out" >&2
    return 1
  fi
  printf '  sweep: %s\n' "${out%%$'\n'*}"
}

cleanup() {
  while read -r id; do
    [ -n "$id" ] || continue
    curl -sf -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers/$id/action" \
      -H 'Content-Type: application/json' -d '{"action":"terminate"}' >/dev/null 2>&1 || true
  done < <(cleanup_list)
  cleanup_done
  sweep_runtime || return
  # A sweep that ran and still left work behind is the same problem as one that
  # never ran, so the end state is asserted rather than assumed.
  if ! "$FEINT_BIN" clean --vm "${FEINT_VM:-incus}" 2>&1 | grep -q "nothing was left behind"; then
    echo "FAIL: the runtime is still not clean after the sweep" >&2
  fi
}
trap cleanup EXIT

sweep_runtime

echo "- a private network keeps the block it was given"
pn="$(api -X POST "$ENDPOINT/vpc/v2/regions/$REGION/private-networks" \
       -d "{\"name\":\"conformance\",\"subnets\":[\"$BLOCK\"]}")" \
  || fail "the emulator refused the block $BLOCK (already in use on this host?)"
pn_id="$(echo "$pn" | jq -r '.id')"
[ "$(echo "$pn" | jq -r '.subnets[0].subnet')" = "$BLOCK" ] \
  || fail "the block came back as $(echo "$pn" | jq -r '.subnets[0].subnet'), expected $BLOCK"
ok "private network $pn_id on $BLOCK"

echo "- an overlapping block is refused"
if api -X POST "$ENDPOINT/vpc/v2/regions/$REGION/private-networks" \
     -d "{\"name\":\"overlap\",\"subnets\":[\"$BLOCK\"]}" >/dev/null 2>&1; then
  fail "a second network took the same block"
fi
ok "overlap refused"

echo "- a restrictive security group"
sg_id="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/security_groups" \
          -d '{"name":"conformance-net","inbound_default_policy":"drop","outbound_default_policy":"accept"}' \
        | jq -r '.security_group.id')"
[ -n "$sg_id" ] && [ "$sg_id" != null ] || fail "the group was not created"
ok "group $sg_id, inbound drop"

boot() { # name [security-group-id] -> "id ip"
  local body id ip nic ipam_id
  body="{\"name\":\"$1\",\"commercial_type\":\"DEV1-S\",\"image\":\"alpine\""
  [ -n "${2:-}" ] && body="$body,\"security_group\":\"$2\""
  body="$body}"
  id="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers" -d "$body" | jq -r '.server.id')"
  [ -n "$id" ] && [ "$id" != null ] || fail "server $1 was not created"
  cleanup_add "$id"
  api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers/$id/action" -d '{"action":"poweron"}' >/dev/null
  # The address is resolved the way a client resolves it: instance/v1.PrivateNIC
  # carries no address, only ipam_ip_ids, and ipam/v1 holds the addresses. A
  # suite that read an address off the NIC would be reading a field the SDK does
  # not define, and proving the emulator against itself.
  nic="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers/$id/private_nics" \
          -d "{\"private_network_id\":\"$pn_id\"}")"
  ipam_id="$(echo "$nic" | jq -r '.private_nic.ipam_ip_ids[0] // empty')"
  [ -n "$ipam_id" ] || fail "server $1: the NIC names no IPAM address ($nic)"
  ip="$(api "$ENDPOINT/ipam/v1/regions/$REGION/ips/$ipam_id" | jq -r '.address' | cut -d/ -f1)"
  [ -n "$ip" ] && [ "$ip" != null ] || fail "server $1: IPAM served no address for $ipam_id"
  echo "$id $ip"
}

echo "- two servers, attached to it"
read -r guard_id guard_ip <<<"$(boot conformance-guard "$sg_id")"
read -r probe_id probe_ip <<<"$(boot conformance-probe)"
ok "guard $guard_ip, probe $probe_ip"

case "$guard_ip" in
  "${BLOCK%.*/*}."*) ok "the address belongs to the declared block" ;;
  *) fail "$guard_ip is outside $BLOCK" ;;
esac
[ "$guard_ip" != "$probe_ip" ] || fail "both servers received $guard_ip"

# From here on the assertions need to look inside the machines, which is the whole point: the
# control plane is not a witness for itself.
machine() { echo "feint-scw-$1"; }
if ! command -v incus >/dev/null 2>&1; then
  stop_here "$LINENO" "incus client not available; cannot verify what the machines carry"
fi
sleep 3

echo "- the machine carries the address the API published"
carried="$(incus query "/1.0/instances/$(machine "$guard_id")/state" 2>/dev/null \
           | jq -r '[.network[]?.addresses[]? | select(.family=="inet") | .address] | join(" ")')"
case " $carried " in
  *" $guard_ip "*) ok "guard carries $guard_ip" ;;
  *) fail "guard carries '${carried:-nothing}', the API published $guard_ip" ;;
esac

# Assertions on what the machine carries in full, rather than on what it was
# supposed to carry. A test that only checks the address it expects passes with
# a second, unannounced one right next to it: that happened, and an operator
# found it by reading `incus list` while the suite was green.

echo "- every interface of the machine is covered by the group"
uncovered="$(incus query "/1.0/instances/$(machine "$guard_id")" \
             | jq -r '.expanded_devices | to_entries[]
                      | select(.value.type == "nic")
                      | select((.value["security.acls"] // "") == "")
                      | .key')"
if [ -z "$uncovered" ]; then
  ok "no interface escapes the rules"
else
  fail "interface(s) with no rule set: $(echo "$uncovered" | tr '\n' ' ')"
fi

# private_ip is deliberately not in the published set: the SDK says it is
# "deprecated and always null when routed_ip_enabled is True", so a real client
# reads a private address through ipam/v1 and a public one through public_ips.
echo "- the machine carries no address the API does not publish"
# Scaleway publishes a server's addresses in two places: the routed public ones
# under public_ips, and the private NIC's under IPAM. Both count, and reading
# only the first is what made this a skip for months.
published="$(api "$ENDPOINT/instance/v1/zones/$ZONE/servers/$guard_id" \
             | jq -r '[.server.public_ips[]?.address] | map(select(. != null)) | join(" ")')"
# shellcheck disable=SC2086 # the published list is several arguments on purpose
assert_only_published "$(machine "$guard_id")" $published "$guard_ip"

echo "- the group's drop policy closes a port, for real"
incus exec "$(machine "$guard_id")" -- sh -c \
  'while true; do printf "ok\n" | nc -l -p 80 >/dev/null 2>&1; done' >/dev/null 2>&1 &
sleep 2
reach() { incus exec "$(machine "$probe_id")" -- timeout 3 nc -z -w 2 "$guard_ip" 80 >/dev/null 2>&1; }

if reach; then
  stop_here "$LINENO" "port 80 is open although the group drops by default; the runtime enforces nothing (Incus < 6.0.4?)"
fi
ok "port 80 refused"

echo "- authorising it opens it, with no restart"
rule_id="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/security_groups/$sg_id/rules" \
            -d "{\"protocol\":\"TCP\",\"direction\":\"inbound\",\"action\":\"accept\",\"ip_range\":\"$BLOCK\",\"dest_port_from\":80}" \
          | jq -r '.rule.id')"
sleep 3
reach || fail "port 80 is still refused after the rule was authorised"
ok "port 80 reachable"

echo "- revoking it closes it again"
api -X DELETE "$ENDPOINT/instance/v1/zones/$ZONE/security_groups/$sg_id/rules/$rule_id" >/dev/null
sleep 3
if reach; then fail "port 80 is still open after the rule was revoked"; fi
ok "port 80 refused again"

# ---- Flexible IP ------------------------------------------------------------
# A public address a cloud hands out is routed to the machine holding it. Here it
# comes from 203.0.113.0/24, which RFC 5737 reserves for documentation: it reads
# as a public address and is routed nowhere real, so the forward cannot capture
# traffic meant for somebody's actual server.

echo "- a flexible IP answers once attached"
ip_json="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/ips" -d '{}')"
ip_id="$(echo "$ip_json" | jq -r '.ip.id')"
public="$(echo "$ip_json" | jq -r '.ip.address')"
[ -n "$public" ] && [ "$public" != null ] || fail "no address in the create response"

if timeout 2 bash -c "echo > /dev/tcp/$public/80" 2>/dev/null; then
  fail "$public answers before being attached to anything"
fi
ok "$public does not answer while detached"

api -X PATCH "$ENDPOINT/instance/v1/zones/$ZONE/ips/$ip_id" -d "{\"server\":\"$guard_id\"}" >/dev/null \
  || fail "attaching the address was refused"
sleep 2

# The verdict is the exit code, never the probe's text. A connection dropped by
# the firewall is killed by `timeout` without a word, so its output is exactly a
# successful connection's: empty. An earlier version of this file matched on the
# text and reported a dropped SYN as "the address answers", which then read as a
# firewall gap that fifteen exit-code measurements later did not exist.

echo "- the group covers the public address"
if timeout 3 bash -c "echo > /dev/tcp/$public/80" >/dev/null 2>&1; then
  fail "$public answers although the group drops inbound"
fi
ok "$public stays closed by the group"

echo "- authorising the port opens the public address"
# This is what separates "the firewall closed it" from "nothing routes it": the
# same probe, with the port authorised, must complete a real connection.
pub_rule="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/security_groups/$sg_id/rules" \
             -d '{"protocol":"TCP","direction":"inbound","action":"accept","ip_range":"0.0.0.0/0","dest_port_from":80}' \
           | jq -r '.rule.id')"
sleep 3
timeout 3 bash -c "echo > /dev/tcp/$public/80" >/dev/null 2>&1 \
  || fail "$public still closed with port 80 authorised; nothing routes it to the machine"
ok "$public reaches the machine once authorised"
api -X DELETE "$ENDPOINT/instance/v1/zones/$ZONE/security_groups/$sg_id/rules/$pub_rule" >/dev/null

echo "- the machine sees its own public address"
if incus exec "$(machine "$guard_id")" -- ip -4 -o addr show 2>/dev/null | grep -q "$public"; then
  ok "guard carries $public, the way a routed IP works upstream"
else
  skip "guard does not carry $public"
fi

api -X PATCH "$ENDPOINT/instance/v1/zones/$ZONE/ips/$ip_id" -d '{"server":""}' >/dev/null \
  || fail "detaching the address was refused"
ok "address detached"

# ---- Isolation --------------------------------------------------------------
# Two managed bridges on one host are routed to each other by default, so an
# emulated subnet is not separate by construction: the emulator has to make it
# so. And it must separate only what upstream separates, since two private
# networks of one VPC reach each other when the VPC routes.
#
# Judged by exit code throughout: a rejected connection prints nothing on some
# paths, exactly like a success.

echo "- a private network of another VPC is unreachable"
other_vpc="$(api -X POST "$ENDPOINT/vpc/v2/regions/$REGION/vpcs" -d '{"name":"conformance-other"}' | jq -r '.id')"
[ -n "$other_vpc" ] && [ "$other_vpc" != null ] || fail "the second VPC was not created"
far_pn="$(api -X POST "$ENDPOINT/vpc/v2/regions/$REGION/private-networks" \
           -d "{\"name\":\"far\",\"subnets\":[\"10.183.0.0/24\"],\"vpc_id\":\"$other_vpc\"}" | jq -r '.id')"
[ -n "$far_pn" ] && [ "$far_pn" != null ] || fail "the far private network was not created"

far_id="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers" \
           -d '{"name":"conformance-far","commercial_type":"DEV1-S","image":"alpine"}' | jq -r '.server.id')"
cleanup_add "$far_id"
far_nic="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers/$far_id/private_nics" \
            -d "{\"private_network_id\":\"$far_pn\"}")"
far_ipam="$(echo "$far_nic" | jq -r '.private_nic.ipam_ip_ids[0] // empty')"
[ -n "$far_ipam" ] || fail "the far NIC names no IPAM address"
far_ip="$(api "$ENDPOINT/ipam/v1/regions/$REGION/ips/$far_ipam" | jq -r '.address' | cut -d/ -f1)"
api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers/$far_id/action" -d '{"action":"poweron"}' >/dev/null
sleep 5

incus exec "$(machine "$far_id")" -- sh -c \
  'while true; do printf "ok\n" | nc -l -p 80 >/dev/null 2>&1; done' >/dev/null 2>&1 &
sleep 2

# On OVN the assertion is hard: two OVN networks reach each other only when
# peered, and another VPC's network must not be. On bridges it stays a skip,
# not silence: two managed bridges of one host are routed together, the
# emulator tries to separate them, and the separation does not hold once the
# NICs carry a security group. docs/limits.md records both.
# The positive control, before the negative verdict: the listener on the far
# machine answers on its own loopback. Without it a machine that never booted and
# a machine correctly isolated are the same observation, and the suite read the
# first as a pass — on the assertion that carries the product's strongest claim
# (#219).
assert_listening "$(machine "$far_id")" 80 "the server of the other VPC"

if incus exec "$(machine "$probe_id")" -- timeout 3 nc -z -w 2 "$far_ip" 80 >/dev/null 2>&1; then
  # A runtime that declares isolation and does not deliver it is a hard failure:
  # that claim is the product's strongest, and an unproven claim is worse than
  # an absent one. A runtime that declares none is skipped, not silently passed.
  if [ "$ISOLATION" = "true" ]; then
    fail "$far_ip is reachable from another VPC, but $MACHINES declares isolation"
  fi
  skip "$far_ip is reachable from another VPC: $MACHINES does not isolate subnets (see docs/limits.md)"
else
  ok "a server of another VPC is unreachable ($far_ip)"
fi

echo "- a private network of the same VPC stays reachable"
near_pn="$(api -X POST "$ENDPOINT/vpc/v2/regions/$REGION/private-networks" \
            -d '{"name":"near","subnets":["10.184.0.0/24"]}' | jq -r '.id')"
near_id="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers" \
            -d '{"name":"conformance-near","commercial_type":"DEV1-S","image":"alpine"}' | jq -r '.server.id')"
cleanup_add "$near_id"
near_nic="$(api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers/$near_id/private_nics" \
             -d "{\"private_network_id\":\"$near_pn\"}")"
near_ipam="$(echo "$near_nic" | jq -r '.private_nic.ipam_ip_ids[0] // empty')"
near_ip="$(api "$ENDPOINT/ipam/v1/regions/$REGION/ips/$near_ipam" | jq -r '.address' | cut -d/ -f1)"
api -X POST "$ENDPOINT/instance/v1/zones/$ZONE/servers/$near_id/action" -d '{"action":"poweron"}' >/dev/null
sleep 5

incus exec "$(machine "$near_id")" -- sh -c \
  'while true; do printf "ok\n" | nc -l -p 80 >/dev/null 2>&1; done' >/dev/null 2>&1 &
sleep 2

if incus exec "$(machine "$probe_id")" -- timeout 3 nc -z -w 2 "$near_ip" 80 >/dev/null 2>&1; then
  ok "a server of the same VPC is reachable ($near_ip)"
else
  fail "$near_ip is unreachable within one VPC; isolation is separating too much"
fi

echo "conformance: network passed"
