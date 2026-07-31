#!/usr/bin/env bash
# Conformance check: drive the emulator with the real Outscale CLI.
#
# oapi-cli is the client this suite measures against. osc-cli exists and is
# deprecated, and it addresses a different path — /api/latest/<Call> rather than
# /api/v1/<Call> — so pointing it here would fail for a reason that says nothing
# about the emulator.
#
# The client is configured through a JSON profile, not flags: it has no
# --endpoint. The endpoint given here is the bare host, because oapi-cli appends
# /api/v1/<Call> itself. Passing http://host/api/v1 makes it request
# /api/v1/api/v1/<Call>, which is a 404 that looks like a missing route.
#
# Usage: tools/conformance/outscale/oapi-cli.sh [endpoint]   (default http://127.0.0.1:4599)
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
trap 'rm -rf "$WORK"' EXIT

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

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }
osc() { oapi-cli --config "$WORK/config.json" "$@"; }

echo "conformance: oapi-cli against $ENDPOINT"

# Every client reads the inventory before it creates anything. The Scaleway pack
# learned this the expensive way: decline the catalogue and the official CLI
# cannot create a server at all, because it resolves the type and the image
# first.
echo "- the inventory a client reads before creating anything"
types="$(osc ReadVmTypes)" || fail "ReadVmTypes rejected: $types"
printf '%s' "$types" | jq -e '.VmTypes | length > 0' >/dev/null \
  || fail "no VM type on offer: a client cannot pick one: $types"
default_type="$(printf '%s' "$types" | jq -r '.VmTypes[0].VmTypeName')"
imgs="$(osc ReadImages)" || fail "ReadImages rejected: $imgs"
image_id="$(printf '%s' "$imgs" | jq -r '.Images[0].ImageId // empty')"
[ -n "$image_id" ] || fail "no image on offer: $imgs"
regions="$(osc ReadRegions)" || fail "ReadRegions rejected: $regions"
# The endpoint a client would call next must be this emulator. Answering
# Outscale's own address would send the following request to the real cloud.
printf '%s' "$regions" | jq -e --arg e "$ENDPOINT" 'any(.Regions[]; .Endpoint == $e)' >/dev/null \
  || fail "ReadRegions points somewhere other than this emulator: $regions"
subregions="$(osc ReadSubregions)" || fail "ReadSubregions rejected: $subregions"
printf '%s' "$subregions" | jq -e '.Subregions | length > 0' >/dev/null \
  || fail "no subregion: $subregions"
ok "$default_type, $image_id, and a region pointing back here"

# The probe checks that these routes answer the shape their document declares.
# What it cannot check is the arithmetic, which is the reason they exist: every
# other local cloud emulator accepts any CIDR, checks no mask, refuses no
# overlap, and reports a fixed address count whatever the prefix.
echo "- the addressing plan is arithmetic, not decoration"
net="$(osc CreateNet --IpRange 10.190.0.0/16)" || fail "CreateNet rejected: $net"
net_id="$(printf '%s' "$net" | jq -r '.Net.NetId // empty')"
[ -n "$net_id" ] || fail "no NetId in the create response: $net"
printf '%s' "$net_id" | grep -Eq '^vpc-[0-9a-f]{8}$' || fail "NetId $net_id is not shaped like one"

if osc CreateNet --IpRange 10.190.128.0/17 >/dev/null 2>&1; then
  fail "a second Net took a range overlapping the first"
fi
if osc CreateNet --IpRange 10.191.0.5/16 >/dev/null 2>&1; then
  fail "a range with host bits set was accepted"
fi
# The mask bounds Outscale publishes: /16 to /28 on a Net, /16 to /29 on a
# Subnet. Neither of these overlaps anything, so only the mask can refuse them.
if osc CreateNet --IpRange 172.0.0.0/8 >/dev/null 2>&1; then
  fail "a /8 Net was accepted, outside the /16 to /28 bounds"
fi

sub="$(osc CreateSubnet --NetId "$net_id" --IpRange 10.190.1.0/24)" \
  || fail "CreateSubnet rejected: $sub"
sub_id="$(printf '%s' "$sub" | jq -r '.Subnet.SubnetId // empty')"
[ -n "$sub_id" ] || fail "no SubnetId in the create response: $sub"
# 256 addresses less the five Outscale reserves: the first four and the last.
# Their own API document carries the arithmetic as an example — a /18 published
# with AvailableIpsCount 16379 — which is what this number is checked against. A
# fixed count whatever the mask is what makes an emulated plan meaningless.
printf '%s' "$sub" | jq -e '.Subnet.AvailableIpsCount == 251' >/dev/null \
  || fail "AvailableIpsCount is not computed from the mask: $sub"

# A second mask, because one number proves nothing: 251 alone is satisfied by a
# hardcoded 251. A /26 holds 64 addresses less the same five, so 59, and only a
# computed count answers both.
sub26="$(osc CreateSubnet --NetId "$net_id" --IpRange 10.190.2.0/26)" \
  || fail "CreateSubnet rejected a /26: $sub26"
sub26_id="$(printf '%s' "$sub26" | jq -r '.Subnet.SubnetId // empty')"
printf '%s' "$sub26" | jq -e '.Subnet.AvailableIpsCount == 59' >/dev/null \
  || fail "AvailableIpsCount does not follow the mask: a /26 should offer 59: $sub26"
osc DeleteSubnet --SubnetId "$sub26_id" >/dev/null || fail "DeleteSubnet rejected the /26"

if osc CreateSubnet --NetId "$net_id" --IpRange 10.190.3.0/30 >/dev/null 2>&1; then
  fail "a /30 Subnet was accepted, past the /29 bound"
fi
if osc CreateSubnet --NetId "$net_id" --IpRange 10.191.1.0/24 >/dev/null 2>&1; then
  fail "a subnet outside its Net range was accepted"
fi
if osc CreateSubnet --NetId "$net_id" --IpRange 10.190.1.128/25 >/dev/null 2>&1; then
  fail "a subnet overlapping a sibling was accepted"
fi
if osc DeleteNet --NetId "$net_id" >/dev/null 2>&1; then
  fail "a Net still holding a subnet was deleted"
fi
ok "mask, containment, overlap and the address count all hold"

osc DeleteSubnet --SubnetId "$sub_id" >/dev/null || fail "DeleteSubnet rejected"
osc DeleteNet --NetId "$net_id" >/dev/null || fail "DeleteNet rejected once empty"
nets="$(osc ReadNets)" || fail "ReadNets rejected: $nets"
printf '%s' "$nets" | jq -e '.Nets | length == 0' >/dev/null \
  || fail "the Net survived its delete: $nets"
ok "deleted in the order the dependency requires"

echo "- a keypair, because a machine nobody can log into proves nothing"
keys="$(osc ReadKeypairs)" || fail "ReadKeypairs rejected: $keys"
printf '%s' "$keys" | jq -e '.Keypairs | length == 0' >/dev/null \
  || fail "a fresh account already holds keypairs: $keys"
created_key="$(osc CreateKeypair --KeypairName conformance \
  --PublicKey "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIExampleKeyForConformance conformance@feint")" \
  || fail "CreateKeypair rejected: $created_key"
printf '%s' "$created_key" | jq -e '.Keypair.KeypairFingerprint | length > 0' >/dev/null \
  || fail "the keypair came back without a fingerprint: $created_key"
# Outscale returns a private key only when it generated the pair. This emulator
# never generates one, so it must never claim to have.
printf '%s' "$created_key" | jq -e '.Keypair | has("PrivateKey") | not' >/dev/null \
  || fail "the emulator handed out a private key it did not generate: $created_key"
ok "keypair conformance registered"

echo "- read an empty account"
vms="$(osc ReadVms)" || fail "ReadVms rejected: $vms"
printf '%s' "$vms" | jq -e '.ResponseContext.RequestId | length > 0' >/dev/null \
  || fail "no RequestId in the response envelope: $vms"
printf '%s' "$vms" | jq -e '.Vms | length == 0' >/dev/null \
  || fail "a fresh account already holds machines: $vms"
ok "envelope carries a RequestId, and the account is empty"

echo "- create, from the catalogue the emulator just published"
created="$(osc CreateVms --ImageId "$image_id" --VmType "$default_type" --KeypairName conformance)" \
  || fail "CreateVms rejected: $created"
vm_id="$(printf '%s' "$created" | jq -r '.Vms[0].VmId // empty')"
[ -n "$vm_id" ] || fail "no VmId in the create response: $created"
# Outscale identifiers are a prefix and eight hexadecimal characters, not UUIDs.
# Their own API description says so in sixty-three places, and a client
# filtering on the shape would drop what the emulator returns.
printf '%s' "$vm_id" | grep -Eq '^i-[0-9a-f]{8}$' \
  || fail "VmId $vm_id is not shaped like an Outscale id"
# BootOnCreation defaults to true upstream, so a create with nothing said gives
# a running machine, which is what every client waits for.
printf '%s' "$created" | jq -e '.Vms[0].State == "running"' >/dev/null \
  || fail "the machine did not come up running: $created"
ok "vm $vm_id, running"

# With a machine runtime configured, "running" has to mean something. Without
# one the emulator tracks state only, which is the default and stays untested
# here so CI needs no runtime.
MACHINES="$(curl -sf "$ENDPOINT/_feint/health" | jq -r '.machines')"
if [ "$MACHINES" != "none" ]; then
  echo "- the address the API publishes is the one the machine answers on"
  ip=""
  for _ in $(seq 1 40); do
    ip="$(osc ReadVms | jq -r --arg id "$vm_id" '.Vms[] | select(.VmId == $id) | .PrivateIp // empty')"
    [ -n "$ip" ] && break
    sleep 0.5
  done
  [ -n "$ip" ] || fail "the machine is running and the API publishes no PrivateIp"
  ping -c 2 -W 2 "$ip" >/dev/null 2>&1 \
    || fail "the API publishes $ip and nothing answers there"
  ok "$ip, and it answers"
fi

echo "- read it back"
vms="$(osc ReadVms)" || fail "ReadVms rejected: $vms"
printf '%s' "$vms" | jq -e --arg id "$vm_id" 'any(.Vms[]; .VmId == $id)' >/dev/null \
  || fail "the created machine is missing from the list: $vms"
printf '%s' "$vms" | jq -e --arg id "$vm_id" --arg t "$default_type" --arg i "$image_id" \
  'any(.Vms[]; .VmId == $id and .VmType == $t and .ImageId == $i and .KeypairName == "conformance")' >/dev/null \
  || fail "the machine did not round-trip what it was created with: $vms"
ok "read back identical"

echo "- stop, retype, start, reboot"
stopped="$(osc StopVms '--VmIds[]' "$vm_id")" || fail "StopVms rejected: $stopped"
printf '%s' "$stopped" | jq -e --arg id "$vm_id" \
  'any(.Vms[]; .VmId == $id and .PreviousState == "running" and .CurrentState == "stopped")' >/dev/null \
  || fail "the stop did not report the transition: $stopped"
# Retyping a running machine is refused everywhere: the guest would have to be
# rebuilt underneath itself. Stopped, it is allowed.
retyped="$(osc UpdateVm --VmId "$vm_id" --VmType tinav6.c2r2p2)" || fail "UpdateVm rejected: $retyped"
printf '%s' "$retyped" | jq -e '.Vm.VmType == "tinav6.c2r2p2"' >/dev/null \
  || fail "the type did not change: $retyped"
started="$(osc StartVms '--VmIds[]' "$vm_id")" || fail "StartVms rejected: $started"
printf '%s' "$started" | jq -e --arg id "$vm_id" \
  'any(.Vms[]; .VmId == $id and .PreviousState == "stopped" and .CurrentState == "running")' >/dev/null \
  || fail "the start did not report the transition: $started"
# RebootVmsResponse carries no Vms field: answering one would be a shape no
# client asked for.
rebooted="$(osc RebootVms '--VmIds[]' "$vm_id")" || fail "RebootVms rejected: $rebooted"
printf '%s' "$rebooted" | jq -e 'has("Vms") | not' >/dev/null \
  || fail "RebootVms answered a Vms field the API does not define: $rebooted"
ok "the lifecycle reports every transition"

echo "- retyping a running machine is refused"
if refused="$(osc UpdateVm --VmId "$vm_id" --VmType tinav6.c4r8p2 2>&1)"; then
  fail "a running machine was retyped: $refused"
fi
ok "refused while running"

echo "- delete"
deleted="$(osc DeleteVms '--VmIds[]' "$vm_id")" || fail "DeleteVms rejected: $deleted"
printf '%s' "$deleted" | jq -e --arg id "$vm_id" \
  'any(.Vms[]; .VmId == $id and .CurrentState == "terminated")' >/dev/null \
  || fail "delete did not report the transition: $deleted"
vms="$(osc ReadVms)" || fail "ReadVms rejected: $vms"
printf '%s' "$vms" | jq -e --arg id "$vm_id" 'all(.Vms[]; .VmId != $id)' >/dev/null \
  || fail "the machine is still listed after delete: $vms"
ok "deleted, and gone"

# The error paths matter as much as the happy one. A client that cannot decode
# an error reports a parsing failure, which sends whoever reads it looking in the
# wrong place entirely.
echo "- a rejected request is a readable API error"
if bad="$(osc CreateVms --VmType tinav4.c1r1p2 2>&1)"; then
  fail "creating without an ImageId was accepted: $bad"
fi
printf '%s' "$bad" | jq -e '.Errors[0] | has("Code") and has("Type") and has("Details")' >/dev/null \
  || fail "the error is not in the SDK's ErrorResponse shape: $bad"
ok "Errors carries Code, Type and Details"

# With most of the surface still unserved, this is the likeliest answer of all.
# It used to be net/http's "404 page not found" in text/plain, which no client
# can decode.
echo "- an unserved operation answers in the API's own dialect"
# The operation is derived from the route table, not written here. Naming one in
# the script makes the check expire the day it gets implemented, which is exactly
# what happened to ReadNets: the suite failed on a route being served. Asking the
# emulator what it mounts keeps the check true as the surface grows.
mounted="$(curl -sf "$ENDPOINT/_feint/routes" | jq -r '.[].path')" \
  || fail "could not read the route table to pick an unserved operation"
unserved_action=""
for candidate in ReadNics ReadVolumes ReadSecurityGroups ReadPublicIps ReadRouteTables; do
  if ! printf '%s\n' "$mounted" | grep -qx "/api/v1/$candidate"; then
    unserved_action="$candidate"
    break
  fi
done
[ -n "$unserved_action" ] \
  || fail "every candidate is served now: add a still-unserved action to the list above"

if unserved="$(osc "$unserved_action" 2>&1)"; then
  fail "$unserved_action is not served but answered successfully: $unserved"
fi
printf '%s' "$unserved" | jq -e '.Errors[0].Type == "OperationNotEmulated"' >/dev/null \
  || fail "an unserved operation did not answer a decodable error: $unserved"
ok "decodable, and says what it is"

echo "- clean up"
if [ "$MACHINES" != "none" ]; then
  # The delete above must have taken the machine with it. A leftover container
  # holds a name and an address the next run wants.
  #
  # Machines only, deliberately. A sweep removes everything the emulator created,
  # and the Scaleway suites run before this one in `mise run conformance`, so
  # demanding a clean runtime here would fail this suite for somebody else's
  # residue. The end state of the whole run is network.sh's assertion, which is
  # the last thing to run and owns it.
  left="$("$SCRIPT_DIR/../../../feint" clean --vm "${FEINT_VM:-incus}" 2>&1)" \
    || fail "the runtime sweep failed: $left"
  case "$left" in
    "removed 0 machine(s)"*) ok "the delete took the machine with it" ;;
    *) fail "the delete left a machine behind: $left" ;;
  esac
fi
osc DeleteKeypair --KeypairName conformance >/dev/null || fail "DeleteKeypair rejected"
keys="$(osc ReadKeypairs)" || fail "ReadKeypairs rejected: $keys"
printf '%s' "$keys" | jq -e '.Keypairs | length == 0' >/dev/null \
  || fail "the keypair survived its delete: $keys"
ok "nothing left behind"

# Started with --contracts, the emulator has been validating every response
# above against Outscale's own OpenAPI document. Reading the verdict here is
# what makes the check part of the run rather than a report nobody opens.
contracts="$(curl -sf "$ENDPOINT/_feint/conformance" || true)"
if [ -n "$contracts" ] && printf '%s' "$contracts" | jq -e '.contracts | index("outscale")' >/dev/null 2>&1; then
  violations="$(printf '%s' "$contracts" | jq -r '.violations | to_entries[] | "\(.key): \(.value | join("; "))"')"
  [ -z "$violations" ] || fail "responses that do not match the API description:"$'\n'"$violations"
  ok "every response matched Outscale's own API description"
fi

echo "conformance: oapi-cli passed"
