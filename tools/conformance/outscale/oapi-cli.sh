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
# oapi-cli reads `region`, never `region_name` — an hour was paid to learn it.
#
# A ~/.osc/config.json profile written for osc-cli, the Python client, carries
# region_name, host, https and method. oapi-cli ignores region_name, falls back
# to its default region (eu-west-2), and presents the profile's credentials
# there; against a real cloud the server answers InvalidParameterValue 4120 —
# which its own error table files under authentication — and it reads exactly
# like a broken client. Measured on a profile whose region_name was
# cloudgouv-eu-west-1:
#
#   oapi-cli --profile=<p> ReadRegions                       -> 200, but the eu-west-2 list
#   oapi-cli --profile=<p> ReadKeypairs                      -> 4120
#   same profile with "region": "cloudgouv-eu-west-1"        -> 200, real data
#
# ReadRegions passes because it is public and signs nothing, so the wrong
# region does not show. That partial success is what misleads the diagnosis:
# the call that works hides the cause of the one that fails, and two calls
# differing only by authentication are what settles it.
#
# Against the emulator none of this bites — any signature is accepted — but a
# recording session against a real account must keep `region` matching the
# account, or the 4120 comes back looking like an emulator defect.
#
# Roadmap note: Outscale has placed osc-cli and oapi-cli in maintenance mode
# and names octl, written in Go, as its reference CLI; the osc-cli and
# osc-sdk-c repositories are archived, the latter since July 2026. Whether this
# suite migrates is an open decision, not this script's to make.
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
# The assertion spans behind the behaviour and negative evidence axes: each
# lifecycle block and each demanded refusal below is bracketed, and the
# emulator refuses the bracket when its own observation does not support it.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../prove.sh"

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
skip() { echo "  SKIP: $*" >&2; }
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
sgs="$(osc ReadSecurityGroups '--Filters.NetIds[]' "$net_id")" || fail "ReadSecurityGroups rejected: $sgs"
printf '%s' "$sgs" | jq -e '.SecurityGroups | length == 1 and .[0].SecurityGroupName == "default"' >/dev/null \
  || fail "the Net has no default security group: $sgs"
# Measured conditionality: the pristine inbound rule has SecurityGroupsMembers
# and no IpRanges key; the outbound rule the reverse. An emulator that emits
# empty arrays where the real cloud omits the key fails this.
printf '%s' "$sgs" | jq -e '.SecurityGroups[0].InboundRules[0] | has("IpRanges") | not' >/dev/null \
  || fail "the pristine inbound rule carries an IpRanges key the real cloud omits: $sgs"
rtbs="$(osc ReadRouteTables '--Filters.NetIds[]' "$net_id")" || fail "ReadRouteTables rejected: $rtbs"
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
held="$(osc ReadPublicIps '--Filters.PublicIpIds[]' "$eip_id")" || fail "ReadPublicIps rejected: $held"
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
linked_link_id="$(osc ReadRouteTables '--Filters.RouteTableIds[]' "$linked_rtb_id" \
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

read_back="$(osc ReadNetPeerings '--Filters.NetPeeringIds[]' "$pcx_id")" \
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
osc ReadNetPeerings '--Filters.NetPeeringIds[]' "$rev_id" \
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
osc ReadNetPeerings '--Filters.NetPeeringIds[]' "$third_id" \
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
osc ReadNetPeerings '--Filters.NetPeeringIds[]' "$pcx_id" \
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
# The refusal speaks the API's dialect: 409, typed ResourceConflict. The
# non-zero exit is captured, not piped: with pipefail, a pipeline that starts
# with an expected failure reads as the failure it expects.
refusal="$(osc AcceptNetPeering --NetPeeringId "$rev_id" 2>&1 || true)"
printf '%s' "$refusal" | grep -q "ResourceConflict" \
  || fail "the state refusal is not typed ResourceConflict: $refusal"
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
listed="$(osc ReadVolumes --Filters.VolumeIds[] "$restored_id")" || fail "ReadVolumes rejected: $listed"
printf '%s' "$listed" | jq -e --arg s "$snap_id" '.Volumes[0].SnapshotId == $s' >/dev/null \
  || fail "the listed restored volume does not carry its provenance: $listed"
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
reread="$(osc ReadImages --Filters.ImageIds.0 "$img_id")" || fail "ReadImages rejected: $reread"
printf '%s' "$reread" | jq -e '.Images[0].Description == "renamed by conformance"' >/dev/null \
  || fail "the description did not survive the write: $reread"

# The half that is refused, and must stay refused.
#
# Additions is an object carrying AccountIds, not a list of objects. The old
# spelling — Additions.0.AccountId — was refused by oapi-cli itself before
# anything was sent, so this assertion was green while the emulator's refusal
# went unexercised: osc exited non-zero for a reason that said nothing about
# the API. The negative span below is what exposed it, by demanding that the
# refusal really cross the wire.
neg="$(prove_begin negative)"
if osc UpdateImage --ImageId "$img_id" \
     '--PermissionsToLaunch.Additions.AccountIds[]' 123456789012 >/dev/null 2>&1; then
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
plain="$(osc ReadVms --DryRun false)" || fail "ReadVms with DryRun false was rejected: $plain"
absent="$(osc ReadVms --Filters.VmIds[] i-00000000)" || fail "a filtered ReadVms was rejected: $absent"
printf '%s' "$absent" | jq -e '.Vms | length == 0' >/dev/null \
  || fail "a filter on an id that does not exist returned machines: $absent"
# And a filter this pack does not serve is refused rather than silently dropped.
if osc ReadVms --Filters.Architectures[] x86_64 >/dev/null 2>&1; then
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
lbu="$(osc CreateLoadBalancer --LoadBalancerName conformance-oapi-lb \
  --Listeners.0.BackendPort 80 --Listeners.0.LoadBalancerPort 80 \
  --Listeners.0.LoadBalancerProtocol TCP \
  '--Subnets[]' "$lbu_sub_id")" || fail "CreateLoadBalancer rejected: $lbu"
printf '%s' "$lbu" | jq -e '.LoadBalancer.DnsName | test("^conformance-oapi-lb-[0-9]+\\..*\\.lbu\\.outscale\\.com$")' >/dev/null \
  || fail "the DnsName does not follow the measured format: $lbu"
registered="$(osc RegisterVmsInLoadBalancer --LoadBalancerName conformance-oapi-lb \
  '--BackendVmIds[]' "$vm_id")" || fail "RegisterVmsInLoadBalancer rejected: $registered"
osc ReadLoadBalancers '--Filters.LoadBalancerNames[]' conformance-oapi-lb \
  | jq -e --arg id "$vm_id" '.LoadBalancers[0].BackendVmIds == [$id]' >/dev/null \
  || fail "the registered backend does not read back"
osc DeleteLoadBalancer --LoadBalancerName conformance-oapi-lb >/dev/null \
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
osc CreateTags --ResourceIds "[\"$vm_id\"]" --Tags '[{"Key":"conformance","Value":"one"}]' >/dev/null \
  || fail "CreateTags rejected"
osc ReadTags '--Filters.ResourceIds[]' "$vm_id" \
  | jq -e 'any(.Tags[]; .Key == "conformance" and .Value == "one")' >/dev/null \
  || fail "the tag was accepted and is not readable"
osc DeleteTags --ResourceIds "[\"$vm_id\"]" --Tags '[{"Key":"conformance","Value":"one"}]' >/dev/null \
  || fail "DeleteTags rejected"
osc ReadTags '--Filters.ResourceIds[]' "$vm_id" \
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
osc LinkPrivateIps --NicId "$nic_id" --PrivateIps '["10.193.1.42"]' >/dev/null \
  || fail "LinkPrivateIps rejected"
osc ReadNics '--Filters.NicIds[]' "$nic_id" \
  | jq -e 'any(.Nics[0].PrivateIps[]; .PrivateIp == "10.193.1.42")' >/dev/null \
  || fail "the secondary address was accepted and is not carried by the NIC"
osc UnlinkPrivateIps --NicId "$nic_id" --PrivateIps '["10.193.1.42"]' >/dev/null \
  || fail "UnlinkPrivateIps rejected"
osc ReadNics '--Filters.NicIds[]' "$nic_id" \
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
  --Rules "[{\"FromPortRange\":22,\"ToPortRange\":22,\"IpProtocol\":\"tcp\",\"SecurityGroupsMembers\":[{\"SecurityGroupId\":\"$peer_sg_id\"}]}]")" \
  || fail "a rule sourced from another group was rejected: $ruled"
printf '%s' "$ruled" | jq -e --arg id "$peer_sg_id" \
  'any(.SecurityGroup.InboundRules[]; any(.SecurityGroupsMembers[]?; .SecurityGroupId == $id))' \
  >/dev/null || fail "the rule came back without the group it points at: $ruled"
in_vm="$(osc CreateVms --ImageId "$image_id" --VmType "$default_type" --SubnetId "$in_sub_id" \
  '--SecurityGroupIds[]' "$in_sg_id")" || fail "CreateVms in a Subnet rejected: $in_vm"
in_vm_id="$(printf '%s' "$in_vm" | jq -r '.Vms[0].VmId // empty')"
[ -n "$in_vm_id" ] || fail "no VmId for the in-Net machine: $in_vm"
osc StopVms '--VmIds[]' "$in_vm_id" >/dev/null || fail "StopVms rejected for the in-Net machine"
in_updated="$(osc UpdateVm --VmId "$in_vm_id" --VmType tinav6.c2r2p2)" \
  || fail "UpdateVm rejected for the in-Net machine: $in_updated"
printf '%s' "$in_updated" | jq -e --arg n "$in_net_id" --arg s "$in_sub_id" \
  '.Vm | .NetId == $n and .SubnetId == $s and (.PrivateIp | length > 0)
        and (.Nics | length >= 1) and (.SecurityGroups | length >= 1)' >/dev/null \
  || fail "an updated machine in a Net answers without its network keys: $in_updated"
osc DeleteVms '--VmIds[]' "$in_vm_id" >/dev/null || fail "DeleteVms rejected for the in-Net machine"
osc DeleteSecurityGroup --SecurityGroupId "$in_sg_id" >/dev/null || fail "DeleteSecurityGroup rejected"
osc DeleteSecurityGroup --SecurityGroupId "$peer_sg_id" >/dev/null || fail "DeleteSecurityGroup rejected"
osc DeleteSubnet --SubnetId "$in_sub_id" >/dev/null || fail "DeleteSubnet rejected"
osc DeleteNet --NetId "$in_net_id" >/dev/null || fail "DeleteNet rejected once empty"
ok "a machine in a Net keeps its network keys through an update, and a rule names a group"

echo "- delete"
deleted="$(osc DeleteVms '--VmIds[]' "$vm_id")" || fail "DeleteVms rejected: $deleted"
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
if bad="$(osc CreateVms --VmType tinav4.c1r1p2 2>&1)"; then
  fail "creating without an ImageId was accepted: $bad"
fi
prove_end "$neg"
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
