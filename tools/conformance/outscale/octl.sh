#!/usr/bin/env bash
# Conformance check: drive the emulator with octl, the Outscale CLI.
#
# octl is the client this suite measures against, and the reason it is not
# oapi-cli any more is a fact rather than a preference: `outscale/oapi-cli` and
# `outscale/osc-cli` are both `archived: true` on the GitHub API, with
# "Deprecated Outscale CLI" in their own description, while `outscale/octl` is
# live (#460). An archived repository is read-only, so the north star — "the
# official client must not see the difference" — now points here.
#
# WHAT MOVED, AND EACH LINE OF IT COST A MEASUREMENT
#
# 1. The endpoint carries its path. OSC_ENDPOINT_API is
#    http://host:port/api/v1, the exact opposite of oapi-cli, which wanted the
#    bare host and appended /api/v1 itself. This is not a guess: the SDK's
#    default template is "%s://api.%s.outscale.com/api/v1"
#    (osc-sdk-go/pkg/profile/endpoint.go), so the path is part of the value by
#    construction. It is the same shape the Terraform provider >= 1.7 wants,
#    and `feint env outscale --client octl` prints it.
#
# 2. `iaas api <Call>`, never an alias. `octl iaas net list` resolves to
#    `octl iaas api ReadNets`; an alias is a convenience of the CLI and the API
#    is what this project measures. Every call below goes through `iaas api`,
#    which is also what makes the operation names in /_feint/conformance mean
#    what they say.
#
# 3. `-o raw` on every call, which is why it lives in the `osc` wrapper rather
#    than being repeated. The default `-o json` RESHAPES the answer: it unwraps
#    {"Nets":[...],"ResponseContext":{...}} to a bare list, and a suite
#    asserting on a reshaped body measures the CLI instead of the emulator.
#    `-o raw` hands back the API's own bytes, ResponseContext and Errors
#    included. The block "the suite reads raw bodies" below is the witness that
#    would catch a reintroduction of the reshaped form.
#
# 4. The error body arrives on STDERR, not stdout, and in two shapes: a refusal
#    the API composed comes after the line "The server returned an error", and a
#    404 comes inline after "an error occurred: unexpected response status 404
#    Not Found: ". `api_error` below is the one reader for both. oapi-cli put
#    its error JSON on stdout, so every refusal assertion had to move.
#
# 5. Stdin is a request body. octl reads all of stdin whenever stdin is not a
#    terminal and decodes it as the payload (osc-sdk-go runner/stdin.go). A
#    suite that runs `while read ... done < file` around a client call would
#    hand the client its own loop input, or block. Every call here is
#    </dev/null, in the wrapper, so the body can only come from flags.
#
# 6. --no-upgrade. Without it octl asks GitHub for a newer release when stdout
#    is a terminal, which is a network call a conformance run must not make and
#    a difference between running the suite by hand and running it in CI.
#
# 7. It does not retry a 409. Measured on this suite's own call counters, one
#    run each: oapi-cli sent THREE requests for every 409 it met and backed off
#    between them — AcceptNetPeering 7 calls against octl's 3, CreatePublicIp
#    261 against 257, nine operations short by exactly two per refusal — and the
#    issue clocked each of those refusals at 12 s (#459, #460). octl answers a
#    409 in the same ~750 ms as a 200, once.
#
# 8. What it costs instead: ~700 ms of process startup on EVERY invocation,
#    against ~30 ms for the request. Measured 2026-08-25: `--version` 678 ms
#    with no network at all, ReadNets 737 ms, a 409 731 ms. That is why the
#    address-exhaustion block below fills through one process rather than
#    spawning one per address.
#
# THE PROFILE, AND THE KEY THAT COST AN HOUR ONCE
#
# octl reads the same ~/.osc/config.json as oapi-cli and the same key: `region`,
# never `region_name` — the SDK's own struct tag says so
# (osc-sdk-go/pkg/profile/profile.go, `json:"region,omitempty"`). A profile
# written for osc-cli, the Python client, carries region_name and is ignored the
# same way it always was; against a real account that means the default region,
# the wrong signature scope and a 4120 that reads like a broken client.
#
# What DID invert is the precedence. oapi-cli let the environment win over
# --config, which is why fake-credentials.env refuses to pin OSC_ENDPOINT_API.
# octl does the opposite: with --config or --profile given it loads the file
# FIRST and merges the environment into what the file left empty
# (octl cmd/sdk.go, `profile.FromFile(...)` then `MergeWith(FromEnv())`). So the
# endpoint written below is the authoritative one, and the exported variable is
# a second lock rather than the first.
#
# Usage: tools/conformance/outscale/octl.sh [endpoint]   (default http://127.0.0.1:4599)
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
# The assertion spans behind the behaviour and negative evidence axes: each
# lifecycle block and each demanded refusal below is bracketed, and the
# emulator refuses the bracket when its own observation does not support it.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../prove.sh"

command -v octl >/dev/null 2>&1 || { echo "FAIL: octl is not installed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
# The endpoint comes from the argument, not from the credentials file, and it
# carries /api/v1 — see point 1 of the header.
# shellcheck disable=SC2034 # read by octl from the environment, not here
OSC_ENDPOINT_API="$ENDPOINT/api/v1"
set +a

# And it must be set, because an unset one sends octl looking for the operator's
# stored profile. guard_local checked where we intend to go; this checks the
# client cannot go anywhere else.
guard_no_real_profile OSC_ENDPOINT_API octl

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

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

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }
skip() { echo "  SKIP: $*" >&2; }

# One wrapper, three non-negotiables: --config so the endpoint cannot come from
# anywhere else, -o raw so the body is the API's own, and </dev/null so the
# request body cannot come from whatever this script's stdin happens to be.
osc() { octl --config "$WORK/config.json" --no-upgrade -o raw iaas api "$@" </dev/null; }

# api_error prints the API's own error document out of a failed octl run.
#
# Two shapes, one reader: octl prefixes an API refusal with the line "The server
# returned an error" and pretty-prints the body under it, and reports a 404
# inline as "...unexpected response status 404 Not Found: {...}". Everything
# from the first brace onwards is the only thing the two have in common, so that
# is what this takes. A failure that is octl's own — an unknown flag, a bad
# value — carries no brace at all, which makes it print nothing and lets the
# caller say "the client refused this itself" instead of parsing a warning as a
# body.
api_error() {
  awk 'BEGIN { found = 0 }
       { if (!found) { i = index($0, "{"); if (i > 0) { print substr($0, i); found = 1 } }
         else print }' "$1"
}

echo "conformance: octl against $ENDPOINT"

# The witness for point 3 of the header, and it is a control rather than a
# comment: it fails if the raw form ever stops carrying the envelope, and it
# fails if -o json ever stops reshaping — the day that happens this wrapper's
# -o raw stops being load-bearing and somebody should be told, not left with a
# guard nobody can see the point of any more.
echo "- the suite reads raw bodies, and would notice if it stopped"
raw_body="$(osc ReadNets)" || fail "ReadNets rejected: $raw_body"
printf '%s' "$raw_body" | jq -e 'has("Nets") and has("ResponseContext")' >/dev/null \
  || fail "-o raw did not hand back the API's own envelope: $raw_body"
reshaped="$(octl --config "$WORK/config.json" --no-upgrade -o json iaas api ReadNets </dev/null)" \
  || fail "ReadNets rejected under -o json: $reshaped"
# A bare list, so it cannot be carrying the envelope: the type is the whole
# assertion, and it fails in both directions. If octl ever stops reshaping, this
# answer becomes an object, the line below fails, and somebody is told that the
# wrapper's -o raw has stopped being what makes this suite honest — rather than
# being left with a guard whose point nobody can see any more.
printf '%s' "$reshaped" | jq -e 'type == "array"' >/dev/null \
  || fail "-o json no longer reshapes the answer to a bare list: $reshaped"
ok "raw carries Nets and ResponseContext; json flattens both away to a bare list"

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
span="$(prove_begin behaviour)"
net="$(osc CreateNet --IpRange 10.190.0.0/16)" || fail "CreateNet rejected: $net"
net_id="$(printf '%s' "$net" | jq -r '.Net.NetId // empty')"
[ -n "$net_id" ] || fail "no NetId in the create response: $net"
printf '%s' "$net_id" | grep -Eq '^vpc-[0-9a-f]{8}$' || fail "NetId $net_id is not shaped like one"

neg="$(prove_begin negative)"
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
prove_end "$neg"
ok "mask, containment, overlap and the address count all hold"

# What a Net is born with. The shapes were measured against a real account
# through `feint proxy` (X-2, 2026-08-08), not read off the SDK: a pristine Net
# carries a default security group, a main route table whose one route is the
# local one over the Net's own block, and a reference to the account's default
# DHCP options set.
echo "- a Net is born with its defaults"
printf '%s' "$net" | jq -e '.Net.DhcpOptionsSetId | startswith("dopt-")' >/dev/null \
  || fail "the Net does not reference the default DHCP options set: $net"
dhcp="$(osc ReadDhcpOptions)" || fail "ReadDhcpOptions rejected: $dhcp"
printf '%s' "$dhcp" | jq -e '.DhcpOptionsSets[0].Default == true' >/dev/null \
  || fail "no default DHCP options set: $dhcp"
sgs="$(osc ReadSecurityGroups --Filters.NetIds "$net_id")" || fail "ReadSecurityGroups rejected: $sgs"
printf '%s' "$sgs" | jq -e '.SecurityGroups | length == 1 and .[0].SecurityGroupName == "default"' >/dev/null \
  || fail "the Net has no default security group: $sgs"
# Measured conditionality: the pristine inbound rule has SecurityGroupsMembers
# and no IpRanges key; the outbound rule the reverse. An emulator that emits
# empty arrays where the real cloud omits the key fails this.
printf '%s' "$sgs" | jq -e '.SecurityGroups[0].InboundRules[0] | has("IpRanges") | not' >/dev/null \
  || fail "the pristine inbound rule carries an IpRanges key the real cloud omits: $sgs"
rtbs="$(osc ReadRouteTables --Filters.NetIds "$net_id")" || fail "ReadRouteTables rejected: $rtbs"
printf '%s' "$rtbs" | jq -e --arg r 10.190.0.0/16 \
  '.RouteTables[0].Routes[0] | .GatewayId == "local" and .DestinationIpRange == $r' >/dev/null \
  || fail "the main route table does not carry the local route: $rtbs"
printf '%s' "$rtbs" | jq -e '.RouteTables[0].LinkRouteTables[0].Main == true' >/dev/null \
  || fail "the main link does not say Main:true: $rtbs"
ok "default security group, main route table, DHCP set"

# The gateway-and-address algebra Terraform's destroy order depends on: what is
# held refuses to go, what is deleted releases what it held.
echo "- a public IP, a gateway and a NAT service hold and release each other"
eip="$(osc CreatePublicIp)" || fail "CreatePublicIp rejected: $eip"
eip_id="$(printf '%s' "$eip" | jq -r '.PublicIp.PublicIpId // empty')"
[ -n "$eip_id" ] || fail "no PublicIpId in the create response: $eip"
printf '%s' "$eip" | jq -e '.PublicIp | has("NatServiceId") | not' >/dev/null \
  || fail "an unlinked address carries holder keys the real cloud omits: $eip"
gw="$(osc CreateInternetService)" || fail "CreateInternetService rejected: $gw"
gw_id="$(printf '%s' "$gw" | jq -r '.InternetService.InternetServiceId // empty')"
osc LinkInternetService --InternetServiceId "$gw_id" --NetId "$net_id" >/dev/null \
  || fail "LinkInternetService rejected"
if osc DeleteNet --NetId "$net_id" >/dev/null 2>&1; then
  fail "a Net with a linked gateway was deleted"
fi
nat="$(osc CreateNatService --SubnetId "$sub_id" --PublicIpId "$eip_id")" \
  || fail "CreateNatService rejected: $nat"
nat_id="$(printf '%s' "$nat" | jq -r '.NatService.NatServiceId // empty')"

# A default route, then retargeted onto the NAT service. UpdateRoute is the last
# operation OSC-3 named that no client walked: Terraform replaces a route rather
# than updating it, so only a direct call reaches it, and a route whose target
# moves is what a client does when a subnet stops being public.
main_rtb="$(printf '%s' "$rtbs" | jq -r '.RouteTables[0].RouteTableId')"
# CreateRoute is called on a table linked to a Subnet, not on the main one, and
# that is the whole point of the two calls above it.
#
# A LinkRouteTables entry carries SubnetId only when the link names a subnet:
# the main table's link says Main:true and omits the key, measured on a real
# account. So CreateRoute against the main table answers a mapping that can
# never carry the field, and the field gate reported
# `RouteTable.LinkRouteTables[].SubnetId` as one the real cloud returns and no
# answer of the run carried — on main, after the shape fold of #407 handed the
# gate real-cloud data for this operation. The pack was not omitting anything;
# the suite was only ever asking about the poorest object.
#
# Asserted here rather than left to the gate, so a future edit that moves the
# call back to the main table fails on the assertion instead of on a nightly.
linked_rtb="$(osc CreateRouteTable --NetId "$net_id")" \
  || fail "CreateRouteTable rejected: $linked_rtb"
linked_rtb_id="$(printf '%s' "$linked_rtb" | jq -r '.RouteTable.RouteTableId // empty')"
[ -n "$linked_rtb_id" ] || fail "no RouteTableId in the create response: $linked_rtb"
osc LinkRouteTable --RouteTableId "$linked_rtb_id" --SubnetId "$sub_id" >/dev/null \
  || fail "LinkRouteTable rejected"
subnet_route="$(osc CreateRoute --RouteTableId "$linked_rtb_id" --DestinationIpRange 0.0.0.0/0 \
                  --GatewayId "$gw_id")" || fail "CreateRoute on a linked table rejected: $subnet_route"
printf '%s' "$subnet_route" | jq -e --arg s "$sub_id" \
  'any(.RouteTable.LinkRouteTables[]; .SubnetId == $s)' >/dev/null \
  || fail "the linked table's answer does not name the subnet it is linked to: $subnet_route"
# And the main table still answers a link with no SubnetId key, which is the
# other half of the conditional emission.
osc CreateRoute --RouteTableId "$main_rtb" --DestinationIpRange 0.0.0.0/0 \
    --GatewayId "$gw_id" >/dev/null || fail "CreateRoute rejected"
moved="$(osc UpdateRoute --RouteTableId "$main_rtb" --DestinationIpRange 0.0.0.0/0 \
           --NatServiceId "$nat_id")" || fail "UpdateRoute rejected: $moved"
printf '%s' "$moved" | jq -e --arg n "$nat_id" \
  'any(.RouteTable.Routes[]; .DestinationIpRange == "0.0.0.0/0" and .NatServiceId == $n)' >/dev/null \
  || fail "the route still points at the gateway after UpdateRoute: $moved"
# The old target must go with it: a route naming two next hops is one no
# forwarding table could act on, and it reads as success.
printf '%s' "$moved" | jq -e \
  'any(.RouteTable.Routes[]; .DestinationIpRange == "0.0.0.0/0" and (has("GatewayId") | not))' >/dev/null \
  || fail "the route kept its gateway alongside the NAT service: $moved"
osc DeleteRoute --RouteTableId "$main_rtb" --DestinationIpRange 0.0.0.0/0 >/dev/null \
  || fail "DeleteRoute rejected"
neg="$(prove_begin negative)"
if osc DeletePublicIp --PublicIpId "$eip_id" >/dev/null 2>&1; then
  fail "an address held by a NAT service was released"
fi
prove_end "$neg"
held="$(osc ReadPublicIps --Filters.PublicIpIds "$eip_id")" || fail "ReadPublicIps rejected: $held"
printf '%s' "$held" | jq -e --arg n "$nat_id" '.PublicIps[0].NatServiceId == $n' >/dev/null \
  || fail "the held address does not name its holder: $held"
osc DeleteNatService --NatServiceId "$nat_id" >/dev/null || fail "DeleteNatService rejected"
osc DeletePublicIp --PublicIpId "$eip_id" >/dev/null || fail "DeletePublicIp rejected once released"
osc UnlinkInternetService --InternetServiceId "$gw_id" --NetId "$net_id" >/dev/null \
  || fail "UnlinkInternetService rejected"
osc DeleteInternetService --InternetServiceId "$gw_id" >/dev/null || fail "DeleteInternetService rejected"
ok "held refuses, released goes, in the order destroy needs"

# The linked route table goes before its Subnet, and its link before the table:
# a Net holding a table it did not create refuses to be deleted, which is the
# dependency order this very span exists to prove. Read the link back from the
# table rather than remembering an id from the create — the same reason every
# destruction here is proved by a read.
linked_link_id="$(osc ReadRouteTables --Filters.RouteTableIds "$linked_rtb_id" \
  | jq -r '.RouteTables[0].LinkRouteTables[0].LinkRouteTableId // empty')"
[ -n "$linked_link_id" ] || fail "the linked table names no link to unlink"
osc UnlinkRouteTable --LinkRouteTableId "$linked_link_id" >/dev/null \
  || fail "UnlinkRouteTable rejected"
osc DeleteRouteTable --RouteTableId "$linked_rtb_id" >/dev/null \
  || fail "DeleteRouteTable rejected"
osc DeleteSubnet --SubnetId "$sub_id" >/dev/null || fail "DeleteSubnet rejected"
osc DeleteNet --NetId "$net_id" >/dev/null || fail "DeleteNet rejected once empty"
nets="$(osc ReadNets)" || fail "ReadNets rejected: $nets"
printf '%s' "$nets" | jq -e '.Nets | length == 0' >/dev/null \
  || fail "the Net survived its delete: $nets"
prove_end "$span"
ok "deleted in the order the dependency requires"

# The Net peering state machine. The states and their spellings are the SDK's
# (pending-acceptance, active, rejected, failed, deleted); what each operation
# accepts as a starting state is its documentation's. Mono-tenancy makes the
# two owners one account, so the identity rules (who may accept, who may
# delete a pending one) are satisfied by construction and only the states are
# measurable — netpeerings.go says so in the same words.
echo "- a Net peering moves through the states the SDK names"
span="$(prove_begin behaviour)"
net_a_doc="$(osc CreateNet --IpRange 10.191.0.0/16)" || fail "CreateNet rejected: $net_a_doc"
net_b_doc="$(osc CreateNet --IpRange 10.192.0.0/16)" || fail "CreateNet rejected: $net_b_doc"
net_a="$(printf '%s' "$net_a_doc" | jq -r '.Net.NetId // empty')"
net_b="$(printf '%s' "$net_b_doc" | jq -r '.Net.NetId // empty')"
[ -n "$net_a" ] && [ -n "$net_b" ] || fail "the peering Nets were not created"

pcx="$(osc CreateNetPeering --SourceNetId "$net_a" --AccepterNetId "$net_b")" \
  || fail "CreateNetPeering rejected: $pcx"
pcx_id="$(printf '%s' "$pcx" | jq -r '.NetPeering.NetPeeringId // empty')"
[ -n "$pcx_id" ] || fail "no NetPeeringId in the create response: $pcx"
printf '%s' "$pcx" | jq -e '.NetPeering.State.Name == "pending-acceptance"' >/dev/null \
  || fail "a fresh peering is not pending-acceptance: $pcx"
printf '%s' "$pcx" | jq -e --arg a "$net_a" --arg b "$net_b" \
  '.NetPeering.SourceNet.NetId == $a and .NetPeering.AccepterNet.NetId == $b
   and .NetPeering.SourceNet.IpRange == "10.191.0.0/16"' >/dev/null \
  || fail "the peering does not carry its two ends: $pcx"

read_back="$(osc ReadNetPeerings --Filters.NetPeeringIds "$pcx_id")" \
  || fail "ReadNetPeerings rejected the provider's own filter: $read_back"
printf '%s' "$read_back" | jq -e '.NetPeerings | length == 1' >/dev/null \
  || fail "the peering did not read back: $read_back"

# The reverse request, pending while the forward one is accepted: the SDK
# documents that accepting A-to-B auto-rejects a pending B-to-A as redundant.
rev_doc="$(osc CreateNetPeering --SourceNetId "$net_b" --AccepterNetId "$net_a")" \
  || fail "CreateNetPeering rejected the reverse request: $rev_doc"
rev_id="$(printf '%s' "$rev_doc" | jq -r '.NetPeering.NetPeeringId // empty')"
[ -n "$rev_id" ] || fail "no NetPeeringId in the reverse create response: $rev_doc"
accepted="$(osc AcceptNetPeering --NetPeeringId "$pcx_id")" \
  || fail "AcceptNetPeering rejected a pending peering: $accepted"
printf '%s' "$accepted" | jq -e '.NetPeering.State.Name == "active"' >/dev/null \
  || fail "an accepted peering is not active: $accepted"
osc ReadNetPeerings --Filters.NetPeeringIds "$rev_id" \
  | jq -e '.NetPeerings[0].State.Name == "rejected"' >/dev/null \
  || fail "the reverse pending peering was not auto-rejected on accept"

# The rejection a human makes, as opposed to the automatic one above. Both
# reach the same state and they are not the same call: the auto-rejection is a
# side effect of AcceptNetPeering, and RejectNetPeering is what the owner of the
# accepter Net runs to refuse an offer. Only the first had ever been driven
# (#174), so the endpoint could have answered anything.
third_doc="$(osc CreateNetPeering --SourceNetId "$net_a" --AccepterNetId "$net_b")" \
  || fail "CreateNetPeering rejected a third request: $third_doc"
third_id="$(printf '%s' "$third_doc" | jq -r '.NetPeering.NetPeeringId // empty')"
[ -n "$third_id" ] || fail "no NetPeeringId in the third create response: $third_doc"
osc RejectNetPeering --NetPeeringId "$third_id" >/dev/null \
  || fail "RejectNetPeering rejected a pending peering"
osc ReadNetPeerings --Filters.NetPeeringIds "$third_id" \
  | jq -e '.NetPeerings[0].State.Name == "rejected"' >/dev/null \
  || fail "an explicitly rejected peering did not reach the rejected state"
# And it stays refused: rejecting twice is not a second transition.
neg="$(prove_begin negative)"
if osc RejectNetPeering --NetPeeringId "$third_id" >/dev/null 2>&1; then
  fail "a peering already rejected was rejected again"
fi
prove_end "$neg"

osc DeleteNetPeering --NetPeeringId "$pcx_id" >/dev/null \
  || fail "DeleteNetPeering rejected an active peering"
osc ReadNetPeerings --Filters.NetPeeringIds "$pcx_id" \
  | jq -e '.NetPeerings[0].State.Name == "deleted"' >/dev/null \
  || fail "a deleted peering must stay readable in the deleted state"
# The Nets go inside the span: a deleted peering is a state transition, not a
# store removal — the record stays readable on purpose — so the create-then-
# destroy the behaviour bracket demands is the Nets', and a deleted-state
# peering naming them must not block it.
osc DeleteNet --NetId "$net_a" >/dev/null || fail "DeleteNet rejected $net_a after its peerings ended"
osc DeleteNet --NetId "$net_b" >/dev/null || fail "DeleteNet rejected $net_b after its peerings ended"
prove_end "$span"
ok "pending-acceptance, active, rejected, deleted — by the SDK's spellings"

echo "- the transitions the state machine forbids are refused"
neg="$(prove_begin negative)"
# A rejected peering can be neither accepted nor deleted, per the SDK docs.
if osc AcceptNetPeering --NetPeeringId "$rev_id" >/dev/null 2>&1; then
  fail "a rejected peering was accepted"
fi
if osc DeleteNetPeering --NetPeeringId "$rev_id" >/dev/null 2>&1; then
  fail "a rejected peering was deleted"
fi
# The refusal speaks the API's dialect: 409, typed ResourceConflict. Read
# through api_error rather than grepped out of the raw stream: octl puts its own
# "The server returned an error" line above the body, and a grep that matched
# the two together would keep passing on a body that had stopped saying it.
osc AcceptNetPeering --NetPeeringId "$rev_id" >/dev/null 2>"$WORK/state-refusal.err" || true
api_error "$WORK/state-refusal.err" | jq -e '.Errors[0].Type == "ResourceConflict"' >/dev/null 2>&1 \
  || fail "the state refusal is not typed ResourceConflict:"$'\n'"$(cat "$WORK/state-refusal.err")"
prove_end "$neg"
ok "accept and delete of a rejected peering refused, as ResourceConflict"

# Snapshots as control-plane records, and the conditional key that was measured
# on a real account: a volume with no provenance has NO SnapshotId key — the
# real cloud never sends "".
echo "- a snapshot is a record, and provenance is a conditional key"
span="$(prove_begin behaviour)"
vol="$(osc CreateVolume --SubregionName eu-west-2a --Size 7)" || fail "CreateVolume rejected: $vol"
vol_id="$(printf '%s' "$vol" | jq -r '.Volume.VolumeId // empty')"
printf '%s' "$vol" | jq -e '.Volume | has("SnapshotId") | not' >/dev/null \
  || fail "a plain volume carries a SnapshotId key the real cloud omits: $vol"

# Transitions are immediate here, and that is a decision docs/limits.md carries:
# a fresh volume is available and its snapshot completed at once. The real cloud
# passes through "creating" and refuses a snapshot taken during that window with
# 409 InvalidVolumeState (6007), measured on 2026-08-08 — deliberately not
# reproduced, because making a client wait for information that does not exist
# here is the regression that limit exists to prevent.
printf '%s' "$vol" | jq -e '.Volume.State == "available"' >/dev/null \
  || fail "a fresh volume is not available: $vol"

snap="$(osc CreateSnapshot --VolumeId "$vol_id")" || fail "CreateSnapshot rejected: $snap"
snap_id="$(printf '%s' "$snap" | jq -r '.Snapshot.SnapshotId // empty')"
printf '%s' "$snap" | jq -e '.Snapshot.State == "completed" and .Snapshot.Progress == 100' >/dev/null \
  || fail "a snapshot is not completed at once: $snap"
restored="$(osc CreateVolume --SubregionName eu-west-2a --SnapshotId "$snap_id")" \
  || fail "CreateVolume from a snapshot rejected: $restored"
printf '%s' "$restored" | jq -e --arg s "$snap_id" '.Volume.SnapshotId == $s and .Volume.Size == 7' >/dev/null \
  || fail "the restored volume does not carry its provenance and size: $restored"
restored_id="$(printf '%s' "$restored" | jq -r '.Volume.VolumeId')"
# Read the provenance back through the list, while the restored volume exists.
# This is what makes the field gate (#88) protect Volumes[].SnapshotId: without
# a ReadVolumes in this window, no listed volume ever carries the key, and
# deleting it from volumeView would stay green — the exact example issue #88
# names as what a fix must catch.
listed="$(osc ReadVolumes --Filters.VolumeIds "$restored_id")" || fail "ReadVolumes rejected: $listed"
printf '%s' "$listed" | jq -e --arg s "$snap_id" '.Volumes[0].SnapshotId == $s' >/dev/null \
  || fail "the listed restored volume does not carry its provenance: $listed"

# THE NUMERIC FILTERS, DRIVEN BY THE CLIENT THAT SENDS THEM AS NUMBERS (#566).
#
# FiltersVolume declares VolumeSizes as a list of integers and FiltersSnapshot
# declares Progresses the same way; this pack read both as lists of strings, so
# the decode failed, the failure was reported as "filter absent", and every
# candidate came back with a 200. That comparison had therefore never
# discriminated for any client, and no unit test and no leg of this suite could
# see it, because nothing here had ever asserted that a filter EXCLUDED
# something.
#
# A volume of a size no other volume here carries, so the assertion names one
# and refuses the rest. octl builds the body from the API description, which is
# what makes this the measurement rather than the curl beside it: it sends 3,
# not "3".
odd="$(osc CreateVolume --SubregionName eu-west-2a --Size 3)" || fail "CreateVolume rejected: $odd"
odd_id="$(printf '%s' "$odd" | jq -r '.Volume.VolumeId // empty')"
sized="$(osc ReadVolumes --Filters.VolumeSizes 3)" || fail "ReadVolumes rejected a VolumeSizes filter: $sized"
printf '%s' "$sized" | jq -e --arg v "$odd_id" \
  '([.Volumes[].VolumeId] | length == 1) and .Volumes[0].VolumeId == $v' >/dev/null \
  || fail "VolumeSizes 3 did not exclude the volumes of another size: $sized"
# And the accepting half, or a filter that refuses everything would pass the
# line above.
kept="$(osc ReadVolumes --Filters.VolumeSizes 7)" || fail "ReadVolumes rejected: $kept"
printf '%s' "$kept" | jq -e --arg v "$odd_id" \
  '(.Volumes | length > 0) and (any(.Volumes[]; .VolumeId == $v) | not)' >/dev/null \
  || fail "VolumeSizes 7 answered nothing, or kept the 3 GiB volume: $kept"
empty="$(osc ReadVolumes --Filters.VolumeSizes 4096)" || fail "ReadVolumes rejected: $empty"
printf '%s' "$empty" | jq -e '.Volumes | length == 0' >/dev/null \
  || fail "a size no volume carries matched something: $empty"
osc DeleteVolume --VolumeId "$odd_id" >/dev/null || fail "DeleteVolume rejected"
progressed="$(osc ReadSnapshots --Filters.Progresses 100)" || fail "ReadSnapshots rejected a Progresses filter: $progressed"
printf '%s' "$progressed" | jq -e --arg s "$snap_id" 'any(.Snapshots[]; .SnapshotId == $s)' >/dev/null \
  || fail "Progresses 100 lost the snapshot that is at 100: $progressed"
unfinished="$(osc ReadSnapshots --Filters.Progresses 7)" || fail "ReadSnapshots rejected: $unfinished"
printf '%s' "$unfinished" | jq -e '.Snapshots | length == 0' >/dev/null \
  || fail "a progress no snapshot carries matched something: $unfinished"
ok "the numeric filters exclude, driven by a client that sends numbers"

osc DeleteSnapshot --SnapshotId "$snap_id" >/dev/null || fail "DeleteSnapshot rejected"
osc DeleteVolume --VolumeId "$restored_id" >/dev/null || fail "DeleteVolume rejected"
ok "record, restore, and the key only when it means something"

# The three updates OSC-3 and OSC-4 promised and no client drove. They were
# served, unit-tested, and unproven: the batches list them among their
# deliverables, and "a unit test alone closes nothing" is condition 3 of what
# makes a batch done. Terraform walks none of them — it replaces rather than
# updates for these — so they belong to this suite.
echo "- the updates a batch promised and nothing had exercised"

# A volume grows and refuses to shrink: a filesystem does not survive its disk
# getting smaller, which is why the refusal matters more than the growth.
grown="$(osc UpdateVolume --VolumeId "$vol_id" --Size 9)" || fail "UpdateVolume rejected: $grown"
printf '%s' "$grown" | jq -e '.Volume.Size == 9' >/dev/null \
  || fail "the volume did not grow: $grown"
neg="$(prove_begin negative)"
if osc UpdateVolume --VolumeId "$vol_id" --Size 3 >/dev/null 2>&1; then
  fail "the volume shrank, which loses a filesystem"
fi
prove_end "$neg"

# An image's description, which is the field of UpdateImage this emulator can
# mean something by. `PermissionsToLaunch` is refused on purpose and the refusal
# is checked below: it grants to another account, and there is only one here.
#
# Cut from a snapshot, because the pack refuses an image with no provenance —
# "an image needs a VmId or BlockDeviceMappings naming a snapshot" — and that
# refusal is the right one: an image of nothing boots nothing.
img_snap="$(osc CreateSnapshot --VolumeId "$vol_id")" || fail "CreateSnapshot rejected: $img_snap"
img_snap_id="$(printf '%s' "$img_snap" | jq -r '.Snapshot.SnapshotId')"
img="$(osc CreateImage --ImageName feint-conformance-update \
         --BlockDeviceMappings.0.Bsu.SnapshotId "$img_snap_id" \
         --BlockDeviceMappings.0.DeviceName /dev/sda1)" || fail "CreateImage rejected: $img"
img_id="$(printf '%s' "$img" | jq -r '.Image.ImageId')"
updated="$(osc UpdateImage --ImageId "$img_id" --Description "renamed by conformance")" \
  || fail "UpdateImage rejected: $updated"
printf '%s' "$updated" | jq -e --arg i "$img_id" \
  '.Image.ImageId == $i and .Image.Description == "renamed by conformance"' >/dev/null \
  || fail "UpdateImage did not apply the description it was given: $updated"
# And it reads back, because an update that answers the new value while storing
# the old one is the failure a single response cannot distinguish.
reread="$(osc ReadImages --Filters.ImageIds "$img_id")" || fail "ReadImages rejected: $reread"
printf '%s' "$reread" | jq -e '.Images[0].Description == "renamed by conformance"' >/dev/null \
  || fail "the description did not survive the write: $reread"

# The half that is refused, and must stay refused.
#
# Additions is an object carrying AccountIds, not a list of objects. Under
# oapi-cli the old spelling — Additions.0.AccountId — was refused by the client
# itself before anything was sent, so the assertion was green while the
# emulator's refusal went unexercised; the negative span is what exposed it, by
# demanding that the refusal really cross the wire. octl spells the same field
# --PermissionsToLaunch.Additions.AccountIds, and the span still holds the line.
neg="$(prove_begin negative)"
if osc UpdateImage --ImageId "$img_id" \
     --PermissionsToLaunch.Additions.AccountIds 123456789012 >/dev/null 2>&1; then
  fail "PermissionsToLaunch was accepted, granting to an account that cannot exist here"
fi
prove_end "$neg"
osc DeleteImage --ImageId "$img_id" >/dev/null || fail "DeleteImage rejected"
osc DeleteSnapshot --SnapshotId "$img_snap_id" >/dev/null || fail "DeleteSnapshot rejected"

osc DeleteVolume --VolumeId "$vol_id" >/dev/null || fail "DeleteVolume rejected"
prove_end "$span"
ok "a volume grows and refuses to shrink, an image's permissions round-trip"

echo "- a keypair, because a machine nobody can log into proves nothing"
# The keypair is created here and deleted at the very end, so this span also
# covers the Vm cycle between the two. The Vm alone could not carry it: a
# terminated Vm stays readable on purpose, so the store never sees it deleted.
span="$(prove_begin behaviour)"
keys="$(osc ReadKeypairs)" || fail "ReadKeypairs rejected: $keys"
printf '%s' "$keys" | jq -e '.Keypairs | length == 0' >/dev/null \
  || fail "a fresh account already holds keypairs: $keys"
created_key="$(osc CreateKeypair --KeypairName conformance \
  --PublicKey "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint")" \
  || fail "CreateKeypair rejected: $created_key"
printf '%s' "$created_key" | jq -e '.Keypair.KeypairFingerprint | length > 0' >/dev/null \
  || fail "the keypair came back without a fingerprint: $created_key"
# Outscale returns a private key only when it generated the pair. This emulator
# never generates one, so it must never claim to have.
printf '%s' "$created_key" | jq -e '.Keypair | has("PrivateKey") | not' >/dev/null \
  || fail "the emulator handed out a private key it did not generate: $created_key"
printf '%s' "$created_key" | jq -e '.Keypair.KeypairType == "ssh-ed25519"' >/dev/null \
  || fail "the keypair type is not the key's own: $created_key"
printf '%s' "$created_key" \
  | jq -e '.Keypair.KeypairFingerprint == "6b:d8:0e:65:b1:58:fd:61:94:3a:b3:42:e6:e1:2c:01"' >/dev/null \
  || fail "the fingerprint is not the one ssh-keygen -l -E md5 prints: $created_key"
ok "keypair conformance registered, with the type and fingerprint of the key itself"

echo "- read an empty account"
vms="$(osc ReadVms)" || fail "ReadVms rejected: $vms"
printf '%s' "$vms" | jq -e '.ResponseContext.RequestId | length > 0' >/dev/null \
  || fail "no RequestId in the response envelope: $vms"
printf '%s' "$vms" | jq -e '.Vms | length == 0' >/dev/null \
  || fail "a fresh account already holds machines: $vms"
ok "envelope carries a RequestId, and the account is empty"

echo "- a filter the client sends is applied, not ignored"
# The defect this replaces: every filter but VmIds returned the whole inventory
# with a 200, and no conformance script sent one, so score.sh never saw the
# unread fields. A filter that matches nothing must answer nothing.
# DryRun false is a legitimate request, and it used to fail this project's own
# gate: the flag is answered at the mount point, so no handler decodes it and it
# counted as a field nobody read.
#
# `--DryRun=false`, with the equals sign, and not `--DryRun false`: cobra reads a
# boolean flag's value only in the attached form, and octl serialises a flag only
# when it was Changed. Verified with `octl --dry-run`, which prints the body it
# would send: `--DryRun=false` gives {"DryRun": false} and no flag gives {}.
plain="$(osc ReadVms --DryRun=false)" || fail "ReadVms with DryRun false was rejected: $plain"
absent="$(osc ReadVms --Filters.VmIds i-00000000)" || fail "a filtered ReadVms was rejected: $absent"
printf '%s' "$absent" | jq -e '.Vms | length == 0' >/dev/null \
  || fail "a filter on an id that does not exist returned machines: $absent"
# And a filter this pack does not serve is refused rather than silently dropped.
if osc ReadVms --Filters.Architectures x86_64 >/dev/null 2>&1; then
  fail "an unemulated filter was accepted, which is indistinguishable from applying it"
fi
ok "filters apply, and an unserved one is refused"

echo "- create, from the catalogue the emulator just published"
# UserData travels base64, as the API defines it. Sent so the read-back below
# means something: without a Vm that carries one, the field gate (#88) has no
# populated answer to hold ReadVms.Vms[].UserData to, and deleting the field
# from the view would stay green.
user_data="$(printf '#!/bin/sh\necho conformance' | base64 | tr -d '\n')"
created="$(osc CreateVms --ImageId "$image_id" --VmType "$default_type" --KeypairName conformance \
  --UserData "$user_data")" \
  || fail "CreateVms rejected: $created"
vm_id="$(printf '%s' "$created" | jq -r '.Vms[0].VmId // empty')"
[ -n "$vm_id" ] || fail "no VmId in the create response: $created"
printf '%s' "$created" | jq -e --arg u "$user_data" '.Vms[0].UserData == $u' >/dev/null \
  || fail "the machine does not carry the UserData it was created with: $created"
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
health="$(curl -sf "$ENDPOINT/_feint/health")"
MACHINES="$(printf '%s' "$health" | jq -r '.machines')"
if [ "$MACHINES" != "none" ]; then
  echo "- the address the API publishes is the one the machine answers on"
  ip=""
  for _ in $(seq 1 40); do
    ip="$(osc ReadVms | jq -r --arg id "$vm_id" '.Vms[] | select(.VmId == $id) | .PrivateIp // empty')"
    [ -n "$ip" ] && break
    sleep 0.5
  done
  [ -n "$ip" ] || fail "the machine is running and the API publishes no PrivateIp"
  # This probe runs from the host, and whether a subnet-internal address
  # answers the host is a property of the mode, declared rather than deduced
  # from its name: an OVN router SNATs the host away from the inside of its
  # networks (docs/limits.md), and the ssh suite proves reachability there
  # through the routed public plane instead.
  if [ "$(printf '%s' "$health" | jq -r '.capabilities.private_from_host')" = "true" ]; then
    ping -c 2 -W 2 "$ip" >/dev/null 2>&1 \
      || fail "the API publishes $ip and nothing answers there"
    ok "$ip, and it answers"
  else
    skip "$MACHINES declares private_from_host=false; reachability is proven on the public plane (ssh suite)"
  fi
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
stopped="$(osc StopVms --VmIds "$vm_id")" || fail "StopVms rejected: $stopped"
printf '%s' "$stopped" | jq -e --arg id "$vm_id" \
  'any(.Vms[]; .VmId == $id and .PreviousState == "running" and .CurrentState == "stopped")' >/dev/null \
  || fail "the stop did not report the transition: $stopped"
# Retyping a running machine is refused everywhere: the guest would have to be
# rebuilt underneath itself. Stopped, it is allowed.
retyped="$(osc UpdateVm --VmId "$vm_id" --VmType tinav6.c2r2p2)" || fail "UpdateVm rejected: $retyped"
printf '%s' "$retyped" | jq -e '.Vm.VmType == "tinav6.c2r2p2"' >/dev/null \
  || fail "the type did not change: $retyped"
started="$(osc StartVms --VmIds "$vm_id")" || fail "StartVms rejected: $started"
printf '%s' "$started" | jq -e --arg id "$vm_id" \
  'any(.Vms[]; .VmId == $id and .PreviousState == "stopped" and .CurrentState == "running")' >/dev/null \
  || fail "the start did not report the transition: $started"
# RebootVmsResponse carries no Vms field: answering one would be a shape no
# client asked for.
rebooted="$(osc RebootVms --VmIds "$vm_id")" || fail "RebootVms rejected: $rebooted"
printf '%s' "$rebooted" | jq -e 'has("Vms") | not' >/dev/null \
  || fail "RebootVms answered a Vms field the API does not define: $rebooted"
ok "the lifecycle reports every transition"

echo "- retyping a running machine is refused"
neg="$(prove_begin negative)"
if refused="$(osc UpdateVm --VmId "$vm_id" --VmType tinav6.c4r8p2 2>&1)"; then
  fail "a running machine was retyped: $refused"
fi
prove_end "$neg"
ok "refused while running"

# The LBU register path by its 1.1.3 name. The Terraform fixture drives the
# whole family through the current provider, and the current provider attaches
# backends through LinkLoadBalancerBackendMachines — measured on ztiac (#281).
# RegisterVmsInLoadBalancer is the same attach as provider 1.1.3 spells it
# (measured on terraform-outscale-k3s), and without this block it would be the
# one served LBU operation no client of this suite ever drives.
echo "- a load balancer registers a backend under the 1.1.3 spelling"
lbu_net="$(osc CreateNet --IpRange 10.193.0.0/16)" || fail "CreateNet rejected: $lbu_net"
lbu_net_id="$(printf '%s' "$lbu_net" | jq -r '.Net.NetId')"
lbu_sub="$(osc CreateSubnet --NetId "$lbu_net_id" --IpRange 10.193.1.0/24)" || fail "CreateSubnet rejected: $lbu_sub"
lbu_sub_id="$(printf '%s' "$lbu_sub" | jq -r '.Subnet.SubnetId')"
lbu="$(osc CreateLoadBalancer --LoadBalancerName conformance-octl-lb \
  --Listeners.0.BackendPort 80 --Listeners.0.LoadBalancerPort 80 \
  --Listeners.0.LoadBalancerProtocol TCP \
  --Subnets "$lbu_sub_id")" || fail "CreateLoadBalancer rejected: $lbu"
printf '%s' "$lbu" | jq -e '.LoadBalancer.DnsName | test("^conformance-octl-lb-[0-9]+\\..*\\.lbu\\.outscale\\.com$")' >/dev/null \
  || fail "the DnsName does not follow the measured format: $lbu"
registered="$(osc RegisterVmsInLoadBalancer --LoadBalancerName conformance-octl-lb \
  --BackendVmIds "$vm_id")" || fail "RegisterVmsInLoadBalancer rejected: $registered"
osc ReadLoadBalancers --Filters.LoadBalancerNames conformance-octl-lb \
  | jq -e --arg id "$vm_id" '.LoadBalancers[0].BackendVmIds == [$id]' >/dev/null \
  || fail "the registered backend does not read back"
osc DeleteLoadBalancer --LoadBalancerName conformance-octl-lb >/dev/null \
  || fail "DeleteLoadBalancer rejected"
osc DeleteSubnet --SubnetId "$lbu_sub_id" >/dev/null || fail "DeleteSubnet rejected after the balancer went"
osc DeleteNet --NetId "$lbu_net_id" >/dev/null || fail "DeleteNet rejected after the balancer went"
ok "registered by the 1.1.3 name, read back, deleted"

# The four reads and the three writes no scenario reached (#174). They are here
# rather than in the Terraform fixture because no provider resource maps to
# them: they are what a client calls directly, and an operation nothing calls is
# an operation whose answer nobody has ever read.
echo "- the reads and links a client makes outside any resource"
reads_span="$(prove_begin behaviour)"

# The public ranges the cloud routes, which a client reads to size a firewall
# rule. A range that is not a CIDR would be worse than an empty list: it reads
# as data.
ranges="$(osc ReadPublicIpRanges)" || fail "ReadPublicIpRanges rejected: $ranges"
printf '%s' "$ranges" | jq -e '.PublicIps | length > 0 and all(.[]; test("^[0-9.]+/[0-9]+$"))' >/dev/null \
  || fail "ReadPublicIpRanges did not answer routable ranges: $ranges"

# The service names a Net access point can target. The emulator serves none, and
# that is an answer: the shape has to be the one the API declares, so a client
# iterating it finds an empty list rather than a missing key.
services="$(osc ReadNetAccessPointServices)" || fail "ReadNetAccessPointServices rejected: $services"
printf '%s' "$services" | jq -e 'has("ResponseContext")' >/dev/null \
  || fail "ReadNetAccessPointServices answered outside the Outscale envelope: $services"

# The administrator password of a Linux machine, which is empty on the real
# cloud too: the field exists for Windows images. Answering a 404 here would
# make a client believe the machine is gone.
admin="$(osc ReadAdminPassword --VmId "$vm_id")" || fail "ReadAdminPassword rejected: $admin"
printf '%s' "$admin" | jq -e 'has("AdminPassword") and has("ResponseContext")' >/dev/null \
  || fail "ReadAdminPassword did not answer the shape the API declares: $admin"

# A tag put on and taken off again. CreateTags was driven by the Terraform
# fixture from the first apply; DeleteTags needed a second apply that drops one,
# which no fixture did, so the emulator's removal path had never run.
osc CreateTags --ResourceIds "$vm_id" --Tags.0.Key conformance --Tags.0.Value one >/dev/null \
  || fail "CreateTags rejected"
osc ReadTags --Filters.ResourceIds "$vm_id" \
  | jq -e 'any(.Tags[]; .Key == "conformance" and .Value == "one")' >/dev/null \
  || fail "the tag was accepted and is not readable"
osc DeleteTags --ResourceIds "$vm_id" --Tags.0.Key conformance --Tags.0.Value one >/dev/null \
  || fail "DeleteTags rejected"
osc ReadTags --Filters.ResourceIds "$vm_id" \
  | jq -e 'any(.Tags[]; .Key == "conformance") | not' >/dev/null \
  || fail "the tag survived DeleteTags"

# Secondary addresses on an interface, which is how a client runs two services
# on one machine. The NIC gets a Net and a Subnet of its own: the ones this
# suite opened with are deleted long before here, and the Vm's own interface is
# published inside its answer rather than stored, so LinkPrivateIps against the
# published NicId answers 5063 — the emulator being right, and the first version
# of this block being wrong.
nic_net="$(osc CreateNet --IpRange 10.193.0.0/16)" || fail "CreateNet rejected: $nic_net"
nic_net_id="$(printf '%s' "$nic_net" | jq -r '.Net.NetId // empty')"
[ -n "$nic_net_id" ] || fail "no NetId for the NIC's Net: $nic_net"
nic_sub="$(osc CreateSubnet --NetId "$nic_net_id" --IpRange 10.193.1.0/24)" \
  || fail "CreateSubnet rejected: $nic_sub"
nic_sub_id="$(printf '%s' "$nic_sub" | jq -r '.Subnet.SubnetId // empty')"
[ -n "$nic_sub_id" ] || fail "no SubnetId for the NIC's Subnet: $nic_sub"
nic="$(osc CreateNic --SubnetId "$nic_sub_id")" || fail "CreateNic rejected: $nic"
nic_id="$(printf '%s' "$nic" | jq -r '.Nic.NicId // empty')"
[ -n "$nic_id" ] || fail "no NicId in the create response: $nic"
osc LinkPrivateIps --NicId "$nic_id" --PrivateIps 10.193.1.42 >/dev/null \
  || fail "LinkPrivateIps rejected"
osc ReadNics --Filters.NicIds "$nic_id" \
  | jq -e 'any(.Nics[0].PrivateIps[]; .PrivateIp == "10.193.1.42")' >/dev/null \
  || fail "the secondary address was accepted and is not carried by the NIC"
osc UnlinkPrivateIps --NicId "$nic_id" --PrivateIps 10.193.1.42 >/dev/null \
  || fail "UnlinkPrivateIps rejected"
osc ReadNics --Filters.NicIds "$nic_id" \
  | jq -e 'any(.Nics[0].PrivateIps[]; .PrivateIp == "10.193.1.42") | not' >/dev/null \
  || fail "the secondary address survived its unlink"
osc DeleteNic --NicId "$nic_id" >/dev/null || fail "DeleteNic rejected"
osc DeleteSubnet --SubnetId "$nic_sub_id" >/dev/null || fail "DeleteSubnet rejected"
osc DeleteNet --NetId "$nic_net_id" >/dev/null || fail "DeleteNet rejected once empty"
prove_end "$reads_span"
ok "ranges, services, admin password, a tag removed, a secondary address linked and unlinked"

# A machine inside a Net, updated there. The machine above is created without a
# Subnet, so every UpdateVm answer of a run carried none of the five keys a
# machine in a Net carries — NetId, SubnetId, PrivateIp, Nics, SecurityGroups —
# and the field gate (#88) had no populated answer to hold them to. Verified
# before this block was written: the view emits all five when the machine has
# them, so what was missing was the call and not the view. Found the day the
# committed corpora started feeding shapes/ (#407).
echo "- a machine inside a Net, updated there"
in_net="$(osc CreateNet --IpRange 10.194.0.0/16)" || fail "CreateNet rejected: $in_net"
in_net_id="$(printf '%s' "$in_net" | jq -r '.Net.NetId // empty')"
[ -n "$in_net_id" ] || fail "no NetId for the in-Net machine: $in_net"
in_sub="$(osc CreateSubnet --NetId "$in_net_id" --IpRange 10.194.1.0/24)" \
  || fail "CreateSubnet rejected: $in_sub"
in_sub_id="$(printf '%s' "$in_sub" | jq -r '.Subnet.SubnetId // empty')"
[ -n "$in_sub_id" ] || fail "no SubnetId for the in-Net machine: $in_sub"
in_sg="$(osc CreateSecurityGroup --NetId "$in_net_id" --SecurityGroupName conformance-in-net \
  --Description 'the group the in-Net machine wears')" || fail "CreateSecurityGroup rejected: $in_sg"
in_sg_id="$(printf '%s' "$in_sg" | jq -r '.SecurityGroup.SecurityGroupId // empty')"
[ -n "$in_sg_id" ] || fail "no SecurityGroupId: $in_sg"
# A rule whose source is another group rather than a range. SecurityGroupsMembers
# is what the real cloud answers there and no rule of this run named a group, so
# the key had never been held to anything.
peer_sg="$(osc CreateSecurityGroup --NetId "$in_net_id" --SecurityGroupName conformance-peer \
  --Description 'the group a rule points at')" || fail "CreateSecurityGroup rejected: $peer_sg"
peer_sg_id="$(printf '%s' "$peer_sg" | jq -r '.SecurityGroup.SecurityGroupId // empty')"
ruled="$(osc CreateSecurityGroupRule --SecurityGroupId "$in_sg_id" --Flow Inbound \
  --Rules.0.FromPortRange 22 --Rules.0.ToPortRange 22 --Rules.0.IpProtocol tcp \
  --Rules.0.SecurityGroupsMembers.0.SecurityGroupId "$peer_sg_id")" \
  || fail "a rule sourced from another group was rejected: $ruled"
printf '%s' "$ruled" | jq -e --arg id "$peer_sg_id" \
  'any(.SecurityGroup.InboundRules[]; any(.SecurityGroupsMembers[]?; .SecurityGroupId == $id))' \
  >/dev/null || fail "the rule came back without the group it points at: $ruled"
in_vm="$(osc CreateVms --ImageId "$image_id" --VmType "$default_type" --SubnetId "$in_sub_id" \
  --SecurityGroupIds "$in_sg_id")" || fail "CreateVms in a Subnet rejected: $in_vm"
in_vm_id="$(printf '%s' "$in_vm" | jq -r '.Vms[0].VmId // empty')"
[ -n "$in_vm_id" ] || fail "no VmId for the in-Net machine: $in_vm"
osc StopVms --VmIds "$in_vm_id" >/dev/null || fail "StopVms rejected for the in-Net machine"
in_updated="$(osc UpdateVm --VmId "$in_vm_id" --VmType tinav6.c2r2p2)" \
  || fail "UpdateVm rejected for the in-Net machine: $in_updated"
printf '%s' "$in_updated" | jq -e --arg n "$in_net_id" --arg s "$in_sub_id" \
  '.Vm | .NetId == $n and .SubnetId == $s and (.PrivateIp | length > 0)
        and (.Nics | length >= 1) and (.SecurityGroups | length >= 1)' >/dev/null \
  || fail "an updated machine in a Net answers without its network keys: $in_updated"
osc DeleteVms --VmIds "$in_vm_id" >/dev/null || fail "DeleteVms rejected for the in-Net machine"
osc DeleteSecurityGroup --SecurityGroupId "$in_sg_id" >/dev/null || fail "DeleteSecurityGroup rejected"
osc DeleteSecurityGroup --SecurityGroupId "$peer_sg_id" >/dev/null || fail "DeleteSecurityGroup rejected"
osc DeleteSubnet --SubnetId "$in_sub_id" >/dev/null || fail "DeleteSubnet rejected"
osc DeleteNet --NetId "$in_net_id" >/dev/null || fail "DeleteNet rejected once empty"
ok "a machine in a Net keeps its network keys through an update, and a rule names a group"

echo "- delete"
deleted="$(osc DeleteVms --VmIds "$vm_id")" || fail "DeleteVms rejected: $deleted"
printf '%s' "$deleted" | jq -e --arg id "$vm_id" \
  'any(.Vms[]; .VmId == $id and .CurrentState == "terminated")' >/dev/null \
  || fail "delete did not report the transition: $deleted"
# A deleted machine stays readable, and says it is terminated.
#
# This used to assert that it disappeared, which is what the emulator did — and
# it crashed the Terraform provider outright on every destroy: the provider
# answers DeleteVms by polling ReadVms until the Vm reports "terminated", and an
# empty list is not a state it knows how to wait for. The real API keeps a
# terminated Vm visible for exactly that reason.
vms="$(osc ReadVms)" || fail "ReadVms rejected: $vms"
printf '%s' "$vms" | jq -e --arg id "$vm_id" \
  'any(.Vms[]; .VmId == $id and .State == "terminated")' >/dev/null \
  || fail "the deleted machine is not readable as terminated: $vms"
ok "deleted, and gone"

# The error paths matter as much as the happy one. A client that cannot decode
# an error reports a parsing failure, which sends whoever reads it looking in the
# wrong place entirely.
echo "- a rejected request is a readable API error"
neg="$(prove_begin negative)"
if osc CreateVms --VmType tinav4.c1r1p2 >/dev/null 2>"$WORK/bad.err"; then
  fail "creating without an ImageId was accepted"
fi
prove_end "$neg"
api_error "$WORK/bad.err" | jq -e '.Errors[0] | has("Code") and has("Type") and has("Details")' >/dev/null 2>&1 \
  || fail "the error is not in the SDK's ErrorResponse shape:"$'\n'"$(cat "$WORK/bad.err")"
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
# The five 404 candidates this list used to hold — ReadNics, ReadVolumes,
# ReadSecurityGroups, ReadPublicIps, ReadRouteTables — are all served now
# (#10's read half and #13's snapshots), which is exactly the expiry the
# comment above predicts. These replacements are declined operations, so they
# stay unserved until a decision changes, not until a batch lands.
unserved_action=""
for candidate in ReadLoadBalancers ReadClientGateways ReadFlexibleGpus ReadVmGroups ReadDirectLinks; do
  if ! printf '%s\n' "$mounted" | grep -qx "/api/v1/$candidate"; then
    unserved_action="$candidate"
    break
  fi
done
[ -n "$unserved_action" ] \
  || fail "every candidate is served now: add a still-unserved action to the list above"

if osc "$unserved_action" >/dev/null 2>"$WORK/unserved.err"; then
  fail "$unserved_action is not served but answered successfully"
fi
# This is the 404 shape of api_error's two: octl reports it inline rather than
# under its "The server returned an error" banner, and a reader that only knew
# the banner would report a decodable error as an unreadable one.
api_error "$WORK/unserved.err" | jq -e '.Errors[0].Type == "OperationNotEmulated"' >/dev/null 2>&1 \
  || fail "an unserved operation did not answer a decodable error:"$'\n'"$(cat "$WORK/unserved.err")"
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
  # The machine count is read from the line that reports it, not from the start
  # of the whole output.
  #
  # A prefix match stood here and produced a false verdict the day `clean` gained
  # a second line: it now prints "removed N stale instance record(s)" before its
  # tally when the runtime holds orphaned records, so the tally was no longer
  # first and the suite announced "the delete left a machine behind" while the
  # tally said zero. The subject was clean; the harness was not.
  machines="$(printf '%s\n' "$left" | grep -o 'removed [0-9]* machine(s)' | grep -o '[0-9]*' | head -1)"
  case "$machines" in
    "") fail "the sweep printed no machine tally, so this check cannot decide: $left" ;;
    0) ok "the delete took the machine with it" ;;
    *) fail "the delete left $machines machine(s) behind: $left" ;;
  esac
fi
osc DeleteKeypair --KeypairName conformance >/dev/null || fail "DeleteKeypair rejected"
keys="$(osc ReadKeypairs)" || fail "ReadKeypairs rejected: $keys"
printf '%s' "$keys" | jq -e '.Keypairs | length == 0' >/dev/null \
  || fail "the keypair survived its delete: $keys"
prove_end "$span"
ok "nothing left behind"

# The refusals a client can ask for, on the reads nothing else ever refused.
#
# `negative` is earned by an operation really answering 4xx to a real client
# inside a span where a suite demanded a refusal. For Outscale the only thing
# that had ever earned it was refusals.sh, reissuing what the real cloud refused
# (#390), and that left the whole read surface at zero: a recording session is
# driven at bogus *identifiers*, and a bogus identifier in a Read is an empty
# list, not a refusal (#428).
#
# Nothing below arms a fault. It could not: an injected refusal leaves the
# observed path before the observer records it, and a span whose only 4xx were
# injected is refused outright. Every refusal here is the emulator's own rule
# meeting a request a real client composed.

# refuse_call drives one call that must be refused, and fails the suite when the
# emulator accepts it.
#
# Three outcomes, never two: refused in the Outscale envelope with the code
# asked for, accepted, or unreadable. The middle one is the defect this guards;
# the third is the harness breaking, reported as itself — and here it also
# catches the case that matters most, a refusal octl made on its own before
# sending anything, whose stderr carries no JSON document at all.
refuse_call() { # expected-code operation args...
  local want="$1" op="$2" out rc=0; shift 2
  out="$(osc "$op" "$@" 2>"$WORK/refusal.err")" || rc=$?
  if [ "$rc" -eq 0 ]; then
    fail "$op accepted what it must refuse: $out"
  fi
  api_error "$WORK/refusal.err" | jq -e --arg c "$want" '.Errors[0].Code == $c' >/dev/null 2>&1 \
    || fail "$op did not refuse with code $want"$'\n'"client stderr: $(cat "$WORK/refusal.err")"
}

# Each line sends ONE filter that Outscale's own API description declares on
# that call and this pack does not serve, so the request is valid to the client,
# valid to the API, and refused by the emulator on its own terms — the filter
# guard of filters.go, which answers 400 naming the field rather than returning
# the whole inventory. `--Filters.<x>` is a generated flag: octl builds one per
# field of the SDK's own filter struct, so a name this client accepts is a name
# the API declares.
#
# It is a table rather than sixteen blocks because the assertion is one
# assertion, and sixteen copies of it is sixteen places for one of them to rot.
#
# Two of the sixteen travel as a payload rather than a flag, and that is an octl
# defect rather than a choice. A date filter is a SLICE of iso8601.Time, but
# octl's flag builder registers any field whose element is a string-like with a
# FlagValue as a scalar Var (pkg/builder/build.go, the reflect.String case never
# looks at f.Slice), while its setter asks pflag for a string slice. Every value
# is therefore refused by the client itself:
#
#   octl iaas api ReadVolumes --Filters.CreationDates 2026-01-01T00:00:00.000Z
#   -> invalid Filters.CreationDates value: trying to get stringSlice value of
#      flag of type osctime
#
# That was caught by refuse_call's third outcome rather than by a reader, which
# is what the third outcome is for: the client exited non-zero, and a helper
# with only "refused" and "accepted" would have marked it proven.
#
# `--payload` is still octl composing and signing `iaas api <Call>`, and a
# payload it fails to decode cannot pass silently here: the request would go out
# without the filter, the emulator would answer 200 with the whole inventory,
# and refuse_call fails on "accepted what it must refuse".
echo "- every read refuses the filter it does not emulate"
neg="$(prove_begin negative)"
refuse_call 4001 ReadVms               --Filters.Architectures x86_64
refuse_call 4001 ReadNets              --Filters.TagKeys owner
refuse_call 4001 ReadSubnets           --Filters.TagKeys owner
refuse_call 4001 ReadKeypairs          --Filters.KeypairIds key-feintnone
refuse_call 4001 ReadSecurityGroups    --Filters.InboundRuleAccountIds 000000000001
refuse_call 4001 ReadRouteTables       --Filters.LinkRouteTableLinkRouteTableIds rtbassoc-feintnone
refuse_call 4001 ReadNics              --Filters.Descriptions none
refuse_call 4001 ReadVolumes           --payload '{"Filters":{"CreationDates":["2026-01-01T00:00:00.000Z"]}}'
refuse_call 4001 ReadSnapshots         --Filters.AccountAliases none
refuse_call 4001 ReadPublicIps         --Filters.NicAccountIds 000000000001
refuse_call 4001 ReadNatServices       --Filters.ClientTokens none
refuse_call 4001 ReadInternetServices  --Filters.LinkStates available
refuse_call 4001 ReadDhcpOptions       --Filters.DomainNameServers 192.0.2.53
refuse_call 4001 ReadNetPeerings       --payload '{"Filters":{"ExpirationDates":["2026-01-01T00:00:00.000Z"]}}'
refuse_call 4001 ReadImages            --Filters.AccountAliases none
refuse_call 4001 ReadVmsState          --Filters.MaintenanceEventCodes none
prove_end "$neg"
ok "sixteen reads named the filter they do not apply, instead of answering the whole inventory"

# The two attach spellings, at a balancer that does not exist. LBU is the one
# family where a wrong name is a refusal rather than an empty list, because the
# call names its target instead of filtering for it — so this is the refusal
# these two operations actually own, and the only one they own.
#
# LinkLoadBalancerBackendMachines and RegisterVmsInLoadBalancer share a handler
# and are two routes: each declares its own upstream operation, so the span
# marks the name that was called. Driving one would say nothing about the other.
echo "- attaching a backend to a balancer that does not exist is refused"
neg="$(prove_begin negative)"
refuse_call 5063 LinkLoadBalancerBackendMachines \
  --LoadBalancerName feint-no-such-balancer --BackendVmIds i-feintnone
refuse_call 5063 UnlinkLoadBalancerBackendMachines \
  --LoadBalancerName feint-no-such-balancer --BackendVmIds i-feintnone
prove_end "$neg"
ok "both spellings answered 5063, which osc.IsNotFound reports true on"

# The five reads whose only refusable argument is the page size.
#
# Four of these serve every filter their API declares, and the fifth
# (ReadPublicIpRanges) declares no filters at all, so the filter guard above has
# nothing to bite on. What they do declare is ResultsPerPage, and Outscale's own
# API description bounds it in the same words on all twenty-one schemas that
# carry it: "between `1` and `1000`, both included". A value outside that is a
# request the real API refuses, and until paging.go it was a request this
# emulator answered with the whole inventory.
#
# 1001 rather than 0 because only one of the two proves the bound is read from
# the value: 0 is also the Go zero value, so a handler refusing 0 could be
# refusing "absent" and nobody would know. The unit tests hold both ends
# (TestAnAbsentPageSizeIsNotAZeroPageSize).
echo "- a page size outside the published bound is refused"
neg="$(prove_begin negative)"
for paged in ReadTags ReadSubregions ReadNetAccessPointServices ReadVmTypes ReadPublicIpRanges; do
  refuse_call 4001 "$paged" --ResultsPerPage 1001
done
prove_end "$neg"
ok "five reads bounded their page size instead of ignoring it"

# The type catalogue applies the filter a client sends it, and names the ones it
# cannot. FiltersVmType declares nine and this handler read none of them, so a
# client resolving its type by name was handed the whole table with a 200 — the
# defect filters.go removed from every other read of this pack, still standing
# on the one call every client makes before it creates anything.
echo "- the type catalogue filters, and refuses what it cannot filter on"
selected="$(osc ReadVmTypes --Filters.VmTypeNames "$default_type" | jq -r '.VmTypes | length')" \
  || fail "ReadVmTypes refused the filter it serves"
[ "$selected" = "1" ] || fail "VmTypeNames selected $selected rows; the filter is not applied"
neg="$(prove_begin negative)"
# MemorySizes travels as a payload rather than a flag, and that is octl's gap
# rather than a choice: its flag builder has a case for bool, int, int32, int64,
# string and map and none for float (pkg/builder/build.go), and FiltersVmType
# spells MemorySizes as an array of numbers — so no --Filters.MemorySizes flag
# is generated at all. `--payload` is still octl composing and signing the
# request through `iaas api ReadVmTypes`, not a hand-rolled curl, and the
# refusal below is what proves the field arrived: the emulator names MemorySizes
# back. A silently dropped payload would answer 200 with the whole table, which
# is exactly the defect this line exists to catch.
refuse_call 4001 ReadVmTypes --payload '{"Filters":{"MemorySizes":[8]}}'
prove_end "$neg"
ok "one type selected by name, and the arithmetic filters refused by name"

# The address block is finite, and running it out is the one refusal
# CreatePublicIp owns. Its request declares nothing but DryRun — there is no
# argument to malform — so the only thing that can make this call fail is the
# state of the emulator's own /24, which is exactly what makes it evidence: no
# fixture, no fault, no account.
#
# The inventory is read before and after and compared BY IDENTIFIER, never by
# count: this suite shares its emulator with the ones that run after it, and an
# address left behind is an address the Terraform fixture cannot have.
echo "- the public block runs out, and says so"
osc ReadPublicIps | jq -r '.PublicIps[].PublicIpId' | sort >"$WORK/ips.before"

# The fill is ONE process, and that is what makes this block affordable under
# octl: ~700 ms of startup on every invocation against ~30 ms for the request
# (#460), so two hundred and fifty separate processes would cost three minutes
# on their own. `--waitfor` keeps calling the same operation inside one process
# until its jq expression holds or the API refuses; `false` never holds, so the
# loop ends on the refusal and nothing else. Measured 2026-08-25: 255 calls in
# 51 s, against 186 s as separate processes.
#
# The timeout is a bound on a defect, the way the old `seq 1 300` was: at the
# measured ~200 ms per iteration two minutes is roughly six hundred addresses,
# far past a /24, so an allocator that never refuses ends this block instead of
# the run.
fill_rc=0
osc CreatePublicIp --waitfor 'false' --interval 10ms --waitfor-timeout 2m \
  >/dev/null 2>"$WORK/fill.err" || fill_rc=$?
[ "$fill_rc" -ne 0 ] \
  || fail "the fill never ended: a /24 handed out addresses without limit"
# Three outcomes, never two: the loop stopped because the block ran out, it
# stopped for another reason, or it timed out. Only the first means anything,
# and a timeout reads exactly like the block being bounded if nobody asks.
api_error "$WORK/fill.err" | jq -e '.Errors[0].Code == "9029"' >/dev/null 2>&1 \
  || fail "the fill stopped on something other than an exhausted block:"$'\n'"$(cat "$WORK/fill.err")"

osc ReadPublicIps | jq -r '.PublicIps[].PublicIpId' | sort >"$WORK/ips.filled"
comm -13 "$WORK/ips.before" "$WORK/ips.filled" >"$WORK/ips.mine"
[ -s "$WORK/ips.mine" ] || fail "the fill exhausted the block without taking a single address"

# The span opens only now: the fill above is setup, and a span bracketing two
# hundred and fifty successful creates would be claiming a refusal it had not
# asked for yet.
neg="$(prove_begin negative)"
refuse_call 9029 CreatePublicIp
prove_end "$neg"

# The `osc` wrapper's </dev/null is what makes this loop safe: octl reads all of
# stdin as its request body whenever stdin is not a terminal, so a client call
# inside a `while read` would eat the list it is iterating.
while read -r id; do
  [ -n "$id" ] || continue
  osc DeletePublicIp --PublicIpId "$id" >/dev/null \
    || fail "DeletePublicIp rejected $id, and the address is now stranded"
done <"$WORK/ips.mine"
osc ReadPublicIps | jq -r '.PublicIps[].PublicIpId' | sort >"$WORK/ips.after"
diff "$WORK/ips.before" "$WORK/ips.after" >"$WORK/ips.diff" \
  || fail "the address inventory did not return to what it was, by identifier"$'\n'"$(cat "$WORK/ips.diff")"
ok "the block refused past its last address, and every address taken was given back"

# A tag put on and taken off a resource that then dies, which is what DeleteTags
# needed to earn `behaviour`.
#
# The axis marks an operation whose store touches fall on a resource created and
# destroyed inside the span. Tags are stored ON the resource they name rather
# than in a table of their own (tags.go), so DeleteTags touches whatever it
# untags — and the suite had only ever untagged a Vm, which this emulator marks
# terminated and keeps readable rather than removing. A Net is removed, so the
# same call becomes attributable.
#
# Measured before it was written: with the Net dying inside the span the store
# reports CreateNet, CreateTags, DeleteTags and DeleteNet; with a Vm it reports
# neither tag call. That is why this block exists rather than a Route.Unearnable
# declaration beside its neighbours — twelve of the thirteen candidates resisted
# this attempt, and this one did not.
echo "- a tag outlives nothing: the resource it named is gone"
span="$(prove_begin behaviour)"
tag_net="$(osc CreateNet --IpRange 10.195.0.0/16)" || fail "CreateNet rejected: $tag_net"
tag_net_id="$(printf '%s' "$tag_net" | jq -r '.Net.NetId // empty')"
[ -n "$tag_net_id" ] || fail "no NetId for the tagged Net: $tag_net"
osc CreateTags --ResourceIds "$tag_net_id" --Tags.0.Key conformance-net --Tags.0.Value one >/dev/null \
  || fail "CreateTags rejected on a Net"
osc ReadTags --Filters.ResourceIds "$tag_net_id" \
  | jq -e 'any(.Tags[]; .Key == "conformance-net" and .ResourceType == "vpc")' >/dev/null \
  || fail "the Net tag is not readable, or does not carry the SDK's own ResourceType"
osc DeleteTags --ResourceIds "$tag_net_id" --Tags.0.Key conformance-net --Tags.0.Value one >/dev/null \
  || fail "DeleteTags rejected on a Net"
osc ReadTags --Filters.ResourceIds "$tag_net_id" \
  | jq -e 'any(.Tags[]; .Key == "conformance-net") | not' >/dev/null \
  || fail "the Net tag survived DeleteTags"
osc DeleteNet --NetId "$tag_net_id" >/dev/null || fail "DeleteNet rejected once its tag was gone"
prove_end "$span"
ok "the tag went, and so did the Net that carried it"

# Started with --contracts, the emulator has been validating every response
# above against Outscale's own OpenAPI document. Reading the verdict here is
# what makes the check part of the run rather than a report nobody opens.
contracts="$(curl -sf "$ENDPOINT/_feint/conformance" || true)"
if [ -n "$contracts" ] && printf '%s' "$contracts" | jq -e '.contracts | index("outscale")' >/dev/null 2>&1; then
  violations="$(printf '%s' "$contracts" | jq -r '.violations | to_entries[] | "\(.key): \(.value | join("; "))"')"
  [ -z "$violations" ] || fail "responses that do not match the API description:"$'\n'"$violations"
  ok "every response matched Outscale's own API description"
fi

echo "conformance: octl passed"
