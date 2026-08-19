#!/usr/bin/env bash
# Conformance check: drive the emulator with the real Scaleway CLI.
#
# This is the only proof that matters. Unit tests assert what we believe the API does; scw asserts
# what the official client accepts. If the CLI parses our answers, the wire format is right.
#
# Usage: tools/conformance/scaleway/scw-cli.sh [endpoint]     (default http://127.0.0.1:4599)
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"

# Never let a client reach anything but the local emulator. Without this, a
# missing endpoint does not fail: every official client falls back to the
# operator's stored credentials, and a test creates billable resources on a real
# account. That is not hypothetical — it happened, to this repository.
# shellcheck source=/dev/null
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/guard.sh"
guard_local "$ENDPOINT"
# The assertion spans behind the behaviour and negative evidence axes: each
# lifecycle block and each demanded refusal below is bracketed, and the
# emulator refuses the bracket when its own observation does not support it.
# shellcheck source=/dev/null
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/prove.sh"
ZONE="${ZONE:-fr-par-1}"

# The SDK validates credential FORMAT even though the emulator ignores the values: the access key
# must look like SCWXXXXXXXXXXXXXXXXX and the secret key must be a UUID. Fake but well-formed.
export SCW_ACCESS_KEY="SCWXXXXXXXXXXXXXXXXX"
export SCW_SECRET_KEY="11111111-1111-1111-1111-111111111111"
export SCW_DEFAULT_PROJECT_ID="11111111-1111-1111-1111-111111111111"
export SCW_DEFAULT_ORGANIZATION_ID="11111111-1111-1111-1111-111111111111"
export SCW_DEFAULT_ZONE="$ZONE"
export SCW_API_URL="$ENDPOINT"
export SCW_INSECURE="true"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

echo "conformance: scw CLI against $ENDPOINT"

echo "- create"
span="$(prove_begin behaviour)"
created="$(scw instance server create name=conformance-1 type=DEV1-S zone="$ZONE" -o json 2>&1)" \
  || fail "create rejected by the CLI: $created"
id="$(printf '%s' "$created" | jq -r '.id // empty')"
[ -n "$id" ] || fail "no id in the create response: $created"
ok "server $id"

echo "- list"
list="$(scw instance server list zone="$ZONE" -o json)" || fail "list rejected: $list"
printf '%s' "$list" | jq -e --arg id "$id" 'any(.[]; .id == $id)' >/dev/null \
  || fail "the created server is missing from the list: $list"
ok "server present in the list"

echo "- get"
got="$(scw instance server get "$id" zone="$ZONE" -o json)" || fail "get rejected: $got"
printf '%s' "$got" | jq -e --arg id "$id" '.id == $id' >/dev/null || fail "get returned another server"
ok "read back identical"

# The CLI starts the server right after creating it, and the API refuses to delete a running
# server. Powering off first is what a real user does, so the suite does it too.
echo "- stop"
scw instance server stop "$id" zone="$ZONE" >/dev/null || fail "poweroff rejected"
state="$(scw instance server get "$id" zone="$ZONE" -o json | jq -r '.state')"
[ "$state" = "stopped" ] || fail "expected the server to be stopped, got '$state'"
ok "powered off"

echo "- delete"
scw instance server delete "$id" zone="$ZONE" >/dev/null || fail "delete rejected"
neg="$(prove_begin negative)"
if scw instance server get "$id" zone="$ZONE" -o json >/dev/null 2>&1; then
  fail "the server still exists after delete"
fi
prove_end "$neg"
prove_end "$span"
ok "deleted, and gone"

# The catalogue is a whitelist, not scenery (#279): the CLI reads /products/servers before it
# creates anything and sums local volumes against the type's volumes_constraint, so a type is
# only proven served when a create names it. STARDUST1-S is the entry the kiwinet-infra-cloud
# survey stack died on, and the one whose published constraint is tightest (max_size 10 GB) —
# if the CLI's volume arithmetic ever starts counting the block root volume, this is the type
# that says so first.
echo "- create with a surveyed type outside the old table (STARDUST1-S)"
span="$(prove_begin behaviour)"
sd="$(scw instance server create name=conformance-stardust type=STARDUST1-S zone="$ZONE" -o json 2>&1)" \
  || fail "create type=STARDUST1-S rejected by the CLI: $sd"
sd_id="$(printf '%s' "$sd" | jq -r '.id // empty')"
[ -n "$sd_id" ] || fail "no id in the create response: $sd"
printf '%s' "$sd" | jq -e '.commercial_type == "STARDUST1-S"' >/dev/null \
  || fail "the commercial type did not round-trip: $sd"
scw instance server stop "$sd_id" zone="$ZONE" >/dev/null || fail "poweroff rejected"
scw instance server delete "$sd_id" zone="$ZONE" >/dev/null || fail "delete rejected"
prove_end "$span"
ok "STARDUST1-S created, typed back, deleted"

# The protection flag, whose behaviour was measured against fr-par-1 rather than assumed (#212):
# it closes the action endpoint, not the DELETE verb. Both halves are driven here, because the
# surprising half is the one a future reader will want to "fix".
echo "- protection: the flag closes the actions and not the delete"
span="$(prove_begin behaviour)"
prot="$(scw instance server create name=conformance-protected type=DEV1-S zone="$ZONE" -o json 2>&1)" \
  || fail "create rejected by the CLI: $prot"
prot_id="$(printf '%s' "$prot" | jq -r '.id // empty')"
[ -n "$prot_id" ] || fail "no id in the create response: $prot"

scw instance server update "$prot_id" protected=true zone="$ZONE" -o json \
  | jq -e '.protected == true' >/dev/null || fail "the flag did not come back from the update"

neg="$(prove_begin negative)"
if scw instance server stop "$prot_id" zone="$ZONE" >/dev/null 2>&1; then
  fail "stop accepted on a protected server; fr-par-1 answers precondition_failed"
fi
prove_end "$neg"

# The client is told before it tries, which is what allowed_actions is for.
scw instance server get "$prot_id" zone="$ZONE" -o json \
  | jq -e '(.allowed_actions | index("poweroff")) == null and (.allowed_actions | index("backup")) != null' \
    >/dev/null || fail "a protected server still advertises poweroff"

scw instance server update "$prot_id" protected=false zone="$ZONE" -o json >/dev/null \
  || fail "clearing the flag rejected"
scw instance server stop "$prot_id" zone="$ZONE" >/dev/null \
  || fail "poweroff rejected once the protection was cleared"

# And the half that reverses the intuition: a protected server deletes. Two runs against
# fr-par-1, each confirming the flag with a fresh GET first, answered 204.
scw instance server update "$prot_id" protected=true zone="$ZONE" -o json >/dev/null \
  || fail "setting the flag back rejected"
scw instance server delete "$prot_id" zone="$ZONE" >/dev/null \
  || fail "delete refused a protected server, which fr-par-1 does not"
prove_end "$span"
ok "stop refused, delete allowed, flag cleared and honoured again"

# Security groups. A fresh project already owns one, so the first list must return it: every
# client reads the existing groups before it creates anything.
echo "- security groups: the project default exists"
groups="$(scw instance security-group list zone="$ZONE" -o json)" || fail "list rejected: $groups"
printf '%s' "$groups" | jq -e 'any(.[]; .project_default == true)' >/dev/null \
  || fail "no default security group in a fresh project: $groups"
ok "default group served"

echo "- security group create, get, update"
span="$(prove_begin behaviour)"
# The CLI unwraps the response envelope for some commands and not for others, so the suite accepts
# both shapes rather than asserting the CLI's own presentation.
sg="$(scw instance security-group create name=conformance-sg description='conformance' \
        inbound-default-policy=drop zone="$ZONE" -o json)" || fail "create rejected: $sg"
sg_id="$(printf '%s' "$sg" | jq -r '(.security_group // .).id // empty')"
[ -n "$sg_id" ] || fail "no id in the create response: $sg"
read_back="$(scw instance security-group get "$sg_id" zone="$ZONE" -o json)" || fail "get rejected: $read_back"
printf '%s' "$read_back" | jq -e '(.security_group // .) | .inbound_default_policy == "drop" and .name == "conformance-sg"' >/dev/null \
  || fail "the group did not round-trip: $read_back"
scw instance security-group update security-group-id="$sg_id" name=conformance-sg-2 zone="$ZONE" -o json >/dev/null \
  || fail "update rejected"
ok "group $sg_id"

# Every rule subcommand takes named arguments only: the CLI has no positional form for them.
echo "- rules: create, list, update, delete"
rule="$(scw instance security-group create-rule security-group-id="$sg_id" protocol=TCP direction=inbound \
          action=accept ip-range=10.0.0.0/8 dest-port-from=22 zone="$ZONE" -o json)" || fail "create-rule rejected: $rule"
rule_id="$(printf '%s' "$rule" | jq -r '(.rule // .).id // empty')"
[ -n "$rule_id" ] || fail "no id in the create-rule response: $rule"
rules="$(scw instance security-group list-rules security-group-id="$sg_id" zone="$ZONE" -o json)" \
  || fail "list-rules rejected: $rules"
printf '%s' "$rules" | jq -e --arg id "$rule_id" 'any(.[]; .id == $id)' >/dev/null \
  || fail "the created rule is missing from the list: $rules"
got_rule="$(scw instance security-group get-rule security-group-id="$sg_id" security-group-rule-id="$rule_id" \
              zone="$ZONE" -o json)" || fail "get-rule rejected: $got_rule"
printf '%s' "$got_rule" | jq -e '(.rule // .) | .ip_range == "10.0.0.0/8" and .dest_port_from == 22' >/dev/null \
  || fail "the rule did not round-trip: $got_rule"
scw instance security-group update-rule security-group-id="$sg_id" security-group-rule-id="$rule_id" \
  action=drop zone="$ZONE" -o json >/dev/null || fail "update-rule rejected"
scw instance security-group delete-rule security-group-id="$sg_id" security-group-rule-id="$rule_id" \
  zone="$ZONE" >/dev/null || fail "delete-rule rejected"
ok "rule $rule_id"

echo "- a server carries its group, and the group refuses to be deleted under it"
sg_server="$(scw instance server create name=conformance-sg-server type=DEV1-S zone="$ZONE" \
               security-group-id="$sg_id" -o json)" || fail "create with a security group rejected: $sg_server"
sg_server_id="$(printf '%s' "$sg_server" | jq -r '.id')"
printf '%s' "$sg_server" | jq -e --arg id "$sg_id" '.security_group.id == $id' >/dev/null \
  || fail "the server did not take the group: $sg_server"
neg="$(prove_begin negative)"
if scw instance security-group delete security-group-id="$sg_id" zone="$ZONE" >/dev/null 2>&1; then
  fail "the group was deleted while a server still used it"
fi
prove_end "$neg"
scw instance server stop "$sg_server_id" zone="$ZONE" >/dev/null || fail "cleanup: poweroff rejected"
scw instance server delete "$sg_server_id" zone="$ZONE" >/dev/null || fail "cleanup: server delete rejected"
scw instance security-group delete security-group-id="$sg_id" zone="$ZONE" >/dev/null || fail "delete rejected once free"
prove_end "$span"
ok "attachment and precondition honoured"

# The CLI reserves an address and names it on the create. The emulator used to
# drop that argument and always answer an empty public_ips, so a server reported
# no public address whatever was attached to it. Found by the unread-field
# report; asserted here so it stays fixed.
echo "- an address reserved before the create comes back on the server"
span="$(prove_begin behaviour)"
ip="$(scw instance ip create zone="$ZONE" -o json)" || fail "ip create rejected: $ip"
ip_id="$(printf '%s' "$ip" | jq -r '(.ip // .).id')"
ip_address="$(printf '%s' "$ip" | jq -r '(.ip // .).address')"
[ -n "$ip_id" ] && [ "$ip_id" != null ] || fail "no id in the ip create response: $ip"

with_ip="$(scw instance server create name=conformance-ip type=DEV1-S zone="$ZONE" \
             ip="$ip_id" -o json)" || fail "create with a reserved ip rejected: $with_ip"
with_ip_id="$(printf '%s' "$with_ip" | jq -r '.id')"
printf '%s' "$with_ip" | jq -e --arg a "$ip_address" 'any(.public_ips[]; .address == $a)' >/dev/null \
  || fail "the server does not carry the address that was reserved for it: $with_ip"
# Read back rather than trusting the create: the list is computed from the IP
# resources, and a create that answered right could still read wrong.
again="$(scw instance server get "$with_ip_id" zone="$ZONE" -o json)"
printf '%s' "$again" | jq -e --arg a "$ip_address" 'any(.public_ips[]; .address == $a)' >/dev/null \
  || fail "the address is gone on a re-read: $again"
scw instance server stop "$with_ip_id" zone="$ZONE" >/dev/null || fail "cleanup: poweroff rejected"
scw instance server delete "$with_ip_id" zone="$ZONE" >/dev/null || fail "cleanup: delete rejected"
scw instance ip delete "$ip_id" zone="$ZONE" >/dev/null || fail "cleanup: ip delete rejected"
prove_end "$span"
ok "reserved, attached, and reported"

# The volume and address lifecycles, walked by the CLI rather than asserted in a
# unit test. Two whole-pack audits found every one of their defects in the gap
# between this fixture and what the pack claimed: an attached volume answering a
# state no SDK declares (which made `tofu apply` time out for five minutes), a
# volume detached on one path and not the other, and `ip delete <address>`
# answering success while the address survived. Unit tests read JSON, so they
# saw none of it. The fixture is the thing that had to grow.
echo "- a volume is attached, refuses to be deleted under its server, and comes back"
span="$(prove_begin behaviour)"
vol="$(scw instance volume create name=conformance-vol volume-type=b_ssd size=10G zone="$ZONE" -o json)" \
  || fail "volume create rejected: $vol"
vol_id="$(printf '%s' "$vol" | jq -r '(.volume // .).id')"
[ -n "$vol_id" ] && [ "$vol_id" != null ] || fail "no id in the volume create response: $vol"

vol_server="$(scw instance server create name=conformance-vol-host type=DEV1-S zone="$ZONE" -o json)" \
  || fail "create for the volume test rejected: $vol_server"
vol_server_id="$(printf '%s' "$vol_server" | jq -r '.id')"

scw instance server attach-volume server-id="$vol_server_id" volume-id="$vol_id" zone="$ZONE" -o json >/dev/null \
  || fail "attach-volume rejected"
# The state must be one the SDK declares, or every official waiter hangs on it:
# VolumeState has eight values and "in_use" is not one of them.
state="$(scw instance volume get "$vol_id" zone="$ZONE" -o json | jq -r '(.volume // .).state')"
case "$state" in
  available|snapshotting|fetching|saving|attaching|resizing|hotsyncing|error) ;;
  *) fail "an attached volume answers state '$state', which VolumeState does not declare" ;;
esac
scw instance volume get "$vol_id" zone="$ZONE" -o json \
  | jq -e --arg s "$vol_server_id" '(.volume // .).server.id == $s' >/dev/null \
  || fail "the attached volume does not name its server"

# The real API refuses this, and a client destroying in the wrong order depends
# on the refusal to retry.
neg="$(prove_begin negative)"
if scw instance volume delete "$vol_id" zone="$ZONE" >/dev/null 2>&1; then
  fail "the volume deleted while attached to a server"
fi
prove_end "$neg"

scw instance server detach-volume server-id="$vol_server_id" volume-id="$vol_id" zone="$ZONE" -o json >/dev/null \
  || fail "detach-volume rejected"
scw instance volume get "$vol_id" zone="$ZONE" -o json \
  | jq -e '(.volume // .).server == null' >/dev/null \
  || fail "the detached volume still names a server"
scw instance volume delete "$vol_id" zone="$ZONE" >/dev/null || fail "delete rejected once detached"
scw instance server stop "$vol_server_id" zone="$ZONE" >/dev/null || fail "cleanup: poweroff rejected"
scw instance server delete "$vol_server_id" zone="$ZONE" >/dev/null || fail "cleanup: delete rejected"
prove_end "$span"
ok "attached, refused, detached, deleted"

echo "- an address is a valid reference for get and for delete"
span="$(prove_begin behaviour)"
byaddr="$(scw instance ip create zone="$ZONE" -o json)" || fail "ip create rejected: $byaddr"
byaddr_id="$(printf '%s' "$byaddr" | jq -r '(.ip // .).id')"
byaddr_address="$(printf '%s' "$byaddr" | jq -r '(.ip // .).address')"
scw instance ip get "$byaddr_address" zone="$ZONE" -o json \
  | jq -e --arg i "$byaddr_id" '(.ip // .).id == $i' >/dev/null \
  || fail "get by address does not resolve to the same ip"
scw instance ip delete "$byaddr_address" zone="$ZONE" >/dev/null || fail "delete by address rejected"
# The half that was broken: it answered success and kept the address.
neg="$(prove_begin negative)"
if scw instance ip get "$byaddr_id" zone="$ZONE" -o json >/dev/null 2>&1; then
  fail "the address survived its own delete"
fi
prove_end "$neg"
prove_end "$span"
ok "resolved and deleted by address"

# The golden-image path, walked by the CLI: snapshot a volume, cut an image from
# the snapshot, list both, delete in the order the API imposes. Served since
# SW-2; declined before it, which made `scw instance snapshot create` fail with a
# 501 on the first call.
echo "- a volume is snapshotted, an image is cut from it, and both read back"
span="$(prove_begin behaviour)"
img_server="$(scw instance server create name=conformance-golden type=DEV1-S zone="$ZONE" -o json)" \
  || fail "create for the image test rejected: $img_server"
img_server_id="$(printf '%s' "$img_server" | jq -r '(.server // .).id')"
img_root="$(printf '%s' "$img_server" | jq -r '(.server // .).volumes["0"].id')"
[ -n "$img_root" ] && [ "$img_root" != null ] || fail "the server carries no root volume: $img_server"

snap="$(scw instance snapshot create name=conformance-snap volume-id="$img_root" zone="$ZONE" -o json)" \
  || fail "snapshot create rejected: $snap"
snap_id="$(printf '%s' "$snap" | jq -r '(.snapshot // .).id')"
[ -n "$snap_id" ] && [ "$snap_id" != null ] || fail "no id in the snapshot create response: $snap"
# Immediately usable: the CLI reads state before it lets anything be cut from it.
printf '%s' "$snap" | jq -e '(.snapshot // .).state == "available"' >/dev/null \
  || fail "the snapshot is not available on creation: $snap"
scw instance snapshot get "$snap_id" zone="$ZONE" -o json \
  | jq -e --arg v "$img_root" '(.snapshot // .).base_volume.id == $v' >/dev/null \
  || fail "the snapshot does not name the volume it was taken of"

# arch is required by the CLI, not by the API: `scw instance image create`
# refuses without it before it sends anything. The kind of thing only the real
# client tells you.
img="$(scw instance image create name=conformance-img snapshot-id="$snap_id" arch=x86_64 zone="$ZONE" -o json)" \
  || fail "image create rejected: $img"
img_id="$(printf '%s' "$img" | jq -r '(.image // .).id')"
[ -n "$img_id" ] && [ "$img_id" != null ] || fail "no id in the image create response: $img"
# What the create answered, the read answers: a disagreement here is what
# Terraform reports as "Provider produced inconsistent result after apply".
scw instance image get "$img_id" zone="$ZONE" -o json \
  | jq -e '(.image // .).name == "conformance-img" and (.image // .).public == false' >/dev/null \
  || fail "the image does not read back as it was created"
scw instance image list zone="$ZONE" -o json \
  | jq -e --arg i "$img_id" 'any(.[]; .id == $i)' >/dev/null \
  || fail "the image the client cut is missing from the listing"

# The order the API imposes, and the refusal that makes it retryable.
neg="$(prove_begin negative)"
if scw instance snapshot delete "$snap_id" zone="$ZONE" >/dev/null 2>&1; then
  fail "the snapshot deleted while an image was cut from it"
fi
prove_end "$neg"
scw instance image delete "$img_id" zone="$ZONE" >/dev/null || fail "image delete rejected"
scw instance snapshot delete "$snap_id" zone="$ZONE" >/dev/null \
  || fail "snapshot delete rejected once its image was gone"
scw instance server stop "$img_server_id" zone="$ZONE" >/dev/null || fail "cleanup: poweroff rejected"
scw instance server delete "$img_server_id" zone="$ZONE" >/dev/null || fail "cleanup: delete rejected"
prove_end "$span"
ok "snapshotted, cut, listed, deleted in order"

# Block Storage, the product the Terraform provider falls back to and no client
# drove before SW-3. The CLI is the only thing that exercises the write half:
# the provider only ever reads a volume it created through the instance side.
echo "- a block volume is created, snapshotted, restored and deleted in order"
span="$(prove_begin behaviour)"
# The size carries its unit: the CLI refuses raw bytes here with "size must be
# defined using the G or GB unit", where the instance volume command took them.
# Two commands of one CLI that do not agree, and only the CLI says so.
blk="$(scw block volume create name=conformance-blk perf-iops=5000 from-empty.size=10G \
        zone="$ZONE" -o json)" || fail "block volume create rejected: $blk"
blk_id="$(printf '%s' "$blk" | jq -r '(.volume // .).id')"
[ -n "$blk_id" ] && [ "$blk_id" != null ] || fail "no id in the block volume create response: $blk"
# The bare envelope: block/v1 answers the resource itself where instance/v1 wraps
# it. A wrapper here would have decoded as an empty volume.
printf '%s' "$blk" | jq -e '(.volume // .).specs.class == "sbs"' >/dev/null \
  || fail "the block volume does not report the sbs storage class: $blk"

scw block volume list zone="$ZONE" -o json \
  | jq -e --arg i "$blk_id" 'any(.[]; .id == $i)' >/dev/null \
  || fail "the block volume is missing from the listing"
scw block volume update volume-id="$blk_id" name=conformance-blk-2 zone="$ZONE" -o json >/dev/null \
  || fail "block volume update rejected"

blk_snap="$(scw block snapshot create name=conformance-blk-snap volume-id="$blk_id" \
             zone="$ZONE" -o json)" || fail "block snapshot create rejected: $blk_snap"
blk_snap_id="$(printf '%s' "$blk_snap" | jq -r '(.snapshot // .).id')"
[ -n "$blk_snap_id" ] && [ "$blk_snap_id" != null ] || fail "no id in the block snapshot response: $blk_snap"
scw block snapshot get "$blk_snap_id" zone="$ZONE" -o json \
  | jq -e --arg v "$blk_id" '(.snapshot // .).parent_volume.id == $v' >/dev/null \
  || fail "the block snapshot does not name the volume it came from"
scw block snapshot list zone="$ZONE" -o json \
  | jq -e --arg i "$blk_snap_id" 'any(.[]; .id == $i)' >/dev/null \
  || fail "the block snapshot is missing from the listing"

# Restored from the snapshot, which is the second of the two create branches and
# the one the API says is exclusive with the first.
blk_restored="$(scw block volume create name=conformance-blk-restored perf-iops=5000 \
                 from-snapshot.snapshot-id="$blk_snap_id" zone="$ZONE" -o json)" \
  || fail "restore from a block snapshot rejected: $blk_restored"
blk_restored_id="$(printf '%s' "$blk_restored" | jq -r '(.volume // .).id')"

# The declared order, asked with the NON-default value — #277's class survived
# every suite that only asked for defaults, because a dropped order_by and an
# honoured one answer the same 200 there. Two volumes exist here
# (conformance-blk-2, conformance-blk-restored); desc must answer the exact
# reverse of asc, whatever else another suite seeded around them.
scw block volume list order-by=name_desc zone="$ZONE" -o json \
  | jq -e '[.[].name | select(startswith("conformance-blk"))] == ["conformance-blk-restored", "conformance-blk-2"]' >/dev/null \
  || fail "order-by=name_desc did not answer the volumes in descending name order"
scw block volume list order-by=name_asc zone="$ZONE" -o json \
  | jq -e '[.[].name | select(startswith("conformance-blk"))] == ["conformance-blk-2", "conformance-blk-restored"]' >/dev/null \
  || fail "order-by=name_asc did not answer the volumes in ascending name order"

# The order the API imposes, and the refusal that makes it retryable.
neg="$(prove_begin negative)"
if scw block snapshot delete "$blk_snap_id" zone="$ZONE" >/dev/null 2>&1; then
  fail "the block snapshot deleted while a volume was restored from it"
fi
prove_end "$neg"
scw block volume delete "$blk_restored_id" zone="$ZONE" >/dev/null || fail "delete of the restored volume rejected"
scw block snapshot delete "$blk_snap_id" zone="$ZONE" >/dev/null \
  || fail "block snapshot delete rejected once nothing came from it"
scw block volume delete "$blk_id" zone="$ZONE" >/dev/null || fail "block volume delete rejected"
prove_end "$span"

# The catalogue this product needs, for the reason the instance one exists.
scw block volume-type list zone="$ZONE" -o json | jq -e 'length > 0' >/dev/null \
  || fail "the block volume-type catalogue answered nothing"
ok "created, snapshotted, restored, refused, deleted in order"

# The IPAM lifecycle, served since SW-4. The CLI's own help states the real
# API's constraint — "Currently IPs can only be reserved from a Private
# Network" — which is exactly the source the emulator books from.
echo "- an address is booked in a private network, read back, and released"
span="$(prove_begin behaviour)"
ipam_pn="$(scw vpc private-network create name=conformance-ipam subnets.0=10.183.0.0/24 \
             region=fr-par -o json)" || fail "private network create rejected: $ipam_pn"
ipam_pn_id="$(printf '%s' "$ipam_pn" | jq -r '.id')"
[ -n "$ipam_pn_id" ] && [ "$ipam_pn_id" != null ] || fail "no id in the private network response: $ipam_pn"

booked="$(scw ipam ip create source.private-network-id="$ipam_pn_id" region=fr-par -o json)" \
  || fail "ipam ip create rejected: $booked"
booked_id="$(printf '%s' "$booked" | jq -r '.id')"
[ -n "$booked_id" ] && [ "$booked_id" != null ] || fail "no id in the book response: $booked"
# The address belongs to the block the network declared, mask included: the SDK
# decodes an scw.IPNet and the CLI prints it in CIDR form.
printf '%s' "$booked" | jq -e '.address | startswith("10.183.0.")' >/dev/null \
  || fail "the booked address does not come from the network's block: $booked"

# A specific address, the one input only the Private Network source accepts.
pinned="$(scw ipam ip create source.private-network-id="$ipam_pn_id" address=10.183.0.42 \
            region=fr-par -o json)" || fail "booking a chosen address rejected: $pinned"
printf '%s' "$pinned" | jq -e '.address == "10.183.0.42/24"' >/dev/null \
  || fail "the chosen address did not come back: $pinned"
pinned_id="$(printf '%s' "$pinned" | jq -r '.id')"
# Booked is booked: the same address a second time must fail.
neg="$(prove_begin negative)"
if scw ipam ip create source.private-network-id="$ipam_pn_id" address=10.183.0.42 \
     region=fr-par -o json >/dev/null 2>&1; then
  fail "the same address was booked twice"
fi
prove_end "$neg"

scw ipam ip list region=fr-par -o json \
  | jq -e --arg i "$booked_id" 'any(.[]; .id == $i)' >/dev/null \
  || fail "the booked address is missing from the list"
scw ipam ip get "$booked_id" region=fr-par -o json \
  | jq -e --arg i "$booked_id" '.id == $i' >/dev/null || fail "get did not read the booked address back"
scw ipam ip update "$booked_id" tags.0=conformance region=fr-par -o json \
  | jq -e '.tags == ["conformance"]' >/dev/null || fail "update did not carry the tag back"

scw ipam ip delete "$booked_id" region=fr-par >/dev/null || fail "release rejected"
scw ipam ip delete "$pinned_id" region=fr-par >/dev/null || fail "release of the pinned address rejected"
if scw ipam ip get "$booked_id" region=fr-par -o json >/dev/null 2>&1; then
  fail "a released address still answers"
fi
scw vpc private-network delete "$ipam_pn_id" region=fr-par >/dev/null \
  || fail "cleanup: private network delete rejected"
prove_end "$span"
ok "booked, pinned, refused twice, listed, updated, released"

# A server dies with its private NICs, and the network it was on becomes
# deletable (#214). Driven by the CLI rather than read from the store, because
# the failure this closes is a destroy that never converges: the NIC survived its
# server, the network refused to go while something referenced it, and no call was
# left that could remove the reference.
echo "- a server dies with its private NICs, and the network goes after it"
span="$(prove_begin behaviour)"
nic_pn="$(scw vpc private-network create name=conformance-nic subnets.0=10.184.0.0/24 \
            region=fr-par -o json)" || fail "private network create rejected: $nic_pn"
nic_pn_id="$(printf '%s' "$nic_pn" | jq -r '.id')"
[ -n "$nic_pn_id" ] && [ "$nic_pn_id" != null ] || fail "no id in the private network response: $nic_pn"

nic_server="$(scw instance server create name=conformance-nic type=DEV1-S zone="$ZONE" -o json 2>&1)" \
  || fail "create rejected by the CLI: $nic_server"
nic_server_id="$(printf '%s' "$nic_server" | jq -r '.id // empty')"
[ -n "$nic_server_id" ] || fail "no id in the create response: $nic_server"

nic="$(scw instance private-nic create server-id="$nic_server_id" \
         private-network-id="$nic_pn_id" zone="$ZONE" -o json 2>&1)" \
  || fail "private nic create rejected: $nic"
nic_id="$(printf '%s' "$nic" | jq -r '.id // .private_nic.id // empty')"
[ -n "$nic_id" ] || fail "no id in the private nic response: $nic"

# Read back by its own id, then removed by it. Both were driven by the Terraform
# provider until 2.81.0 moved its reads and deletes onto instance/v2alpha1 — at
# which point two routes this emulator still serves were driven by nobody, and
# the #174 gate said so on the next run. `scw` reaches them and nothing else
# does, so the suite that keeps them honest is this one.
nic_read="$(scw instance private-nic get private-nic-id="$nic_id" server-id="$nic_server_id" zone="$ZONE" -o json 2>&1)" \
  || fail "private nic get rejected: $nic_read"
printf '%s' "$nic_read" | jq -e --arg id "$nic_id" '(.id // .private_nic.id) == $id' >/dev/null \
  || fail "the NIC read back is not the one created: $nic_read"

# The address is booked before the delete, so the release after it is a change
# and not the absence of anything.
scw ipam ip list private-network-id="$nic_pn_id" region=fr-par -o json \
  | jq -e 'length > 0' >/dev/null || fail "the attach booked no address: nothing to measure"

# Detached through its own door rather than by deleting the server, which is the
# path a client takes when it keeps the machine. releaseNIC runs either way, and
# only this one exercises the v1 delete.
scw instance private-nic delete private-nic-id="$nic_id" server-id="$nic_server_id" zone="$ZONE" >/dev/null 2>&1 \
  || fail "private nic delete rejected"
scw instance private-nic list server-id="$nic_server_id" zone="$ZONE" -o json \
  | jq -e 'length == 0' >/dev/null \
  || fail "the NIC still lists after its own delete"

scw instance server stop "$nic_server_id" zone="$ZONE" >/dev/null || fail "poweroff rejected"
scw instance server delete "$nic_server_id" zone="$ZONE" >/dev/null || fail "delete rejected"

neg="$(prove_begin negative)"
if scw instance private-nic list server-id="$nic_server_id" zone="$ZONE" -o json >/dev/null 2>&1; then
  fail "the NIC listing still answers for a deleted server"
fi
prove_end "$neg"

# The address went back to the pool. Left behind it would be booked for ever:
# the allocator is rebuilt from what IPAM holds.
scw ipam ip list private-network-id="$nic_pn_id" region=fr-par -o json \
  | jq -e 'length == 0' >/dev/null \
  || fail "the network still holds an address after its only server was deleted"

# And the network goes, which is the whole point: this is where a destroy used
# to stop converging.
scw vpc private-network delete "$nic_pn_id" region=fr-par >/dev/null \
  || fail "the private network refuses to be deleted after its only server was removed"
prove_end "$span"
ok "NIC gone with its server, address released, network deleted"

# IAM SSH keys, all five (#174). The only suite that drove them was
# conformance:ssh, which needs a machine runtime and is excluded from
# `mise run conformance` by design — so five operations a client uses on day one
# were mounted, probed, and never driven by a client in CI.
#
# The CRUD needs no runtime at all, which is what makes the gap avoidable rather
# than structural.
echo "- an IAM SSH key is created, listed, read, renamed and removed"
span="$(prove_begin behaviour)"
key="$(scw iam ssh-key create name=conformance-key \
        public-key='ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint' \
        -o json 2>&1)" || fail "ssh-key create rejected: $key"
key_id="$(printf '%s' "$key" | jq -r '.id // empty')"
[ -n "$key_id" ] || fail "no id in the ssh-key response: $key"

scw iam ssh-key list -o json | jq -e --arg i "$key_id" 'any(.[]; .id == $i)' >/dev/null \
  || fail "the key is missing from the list"
scw iam ssh-key get "$key_id" -o json | jq -e --arg i "$key_id" '.id == $i' >/dev/null \
  || fail "get did not read the key back"
scw iam ssh-key update "$key_id" name=conformance-key-2 -o json \
  | jq -e '.name == "conformance-key-2"' >/dev/null || fail "update did not carry the name back"
scw iam ssh-key delete "$key_id" >/dev/null || fail "ssh-key delete rejected"
neg="$(prove_begin negative)"
if scw iam ssh-key get "$key_id" -o json >/dev/null 2>&1; then
  fail "a deleted key still answers"
fi
prove_end "$neg"
prove_end "$span"
ok "created, listed, read, renamed, removed"

# User data, the three operations the YAML-injection work hardened and that no
# client had ever driven (#174). The hardened route is the one a client's
# cloud-init actually takes, so leaving it unproven was the gap that mattered
# most of the three families.
echo "- user data is set, read back and removed on a server"
span="$(prove_begin behaviour)"
ud_server="$(scw instance server create name=conformance-userdata type=DEV1-S zone="$ZONE" -o json 2>&1)" \
  || fail "create rejected by the CLI: $ud_server"
ud_id="$(printf '%s' "$ud_server" | jq -r '.id // empty')"
[ -n "$ud_id" ] || fail "no id in the create response: $ud_server"

ud_file="$(mktemp)"
printf '#cloud-config\npackages:\n  - htop\n' > "$ud_file"
scw instance user-data set server-id="$ud_id" key=cloud-init \
  content=@"$ud_file" zone="$ZONE" >/dev/null \
  || fail "user-data set rejected"
scw instance user-data get server-id="$ud_id" key=cloud-init zone="$ZONE" 2>/dev/null \
  | grep -q 'cloud-config' || fail "the user data did not come back"
scw instance user-data delete server-id="$ud_id" key=cloud-init zone="$ZONE" >/dev/null \
  || fail "user-data delete rejected"

scw instance server stop "$ud_id" zone="$ZONE" >/dev/null || fail "cleanup: poweroff rejected"
scw instance server delete "$ud_id" zone="$ZONE" >/dev/null || fail "cleanup: delete rejected"
rm -f "$ud_file"
prove_end "$span"
ok "set, read, removed"

# The block/v1 write path (#174). SW-3 closed with v1 proven on GetVolume and
# DeleteVolume alone: scw 2.56.3 drives the alpha, and the Terraform fixture uses
# scaleway_instance_volume, so nine v1 operations were mounted and never walked.
#
# `scw block` drives v1 directly, which makes this the cheapest of the gaps the
# issue lists and the one that had been open longest.
echo "- a block volume and its snapshot go through their whole v1 life"
span="$(prove_begin behaviour)"
scw block volume-type list zone="$ZONE" -o json | jq -e 'length > 0' >/dev/null \
  || fail "no block volume type on offer"

bvol="$(scw block volume create name=conformance-b1 from-empty.size=10GB \
         perf-iops=5000 zone="$ZONE" -o json 2>&1)" || fail "block volume create rejected: $bvol"
bvol_id="$(printf '%s' "$bvol" | jq -r '.id // empty')"
[ -n "$bvol_id" ] || fail "no id in the block volume response: $bvol"

scw block volume list zone="$ZONE" -o json | jq -e --arg i "$bvol_id" 'any(.[]; .id == $i)' >/dev/null \
  || fail "the block volume is missing from the list"
scw block volume update volume-id="$bvol_id" name=conformance-b1-renamed zone="$ZONE" -o json \
  | jq -e '.name == "conformance-b1-renamed"' >/dev/null || fail "block volume update did not carry the name"

bsnap="$(scw block snapshot create volume-id="$bvol_id" name=conformance-bs1 zone="$ZONE" -o json 2>&1)" \
  || fail "block snapshot create rejected: $bsnap"
bsnap_id="$(printf '%s' "$bsnap" | jq -r '.id // empty')"
[ -n "$bsnap_id" ] || fail "no id in the block snapshot response: $bsnap"

scw block snapshot get "$bsnap_id" zone="$ZONE" -o json | jq -e --arg i "$bsnap_id" '.id == $i' >/dev/null \
  || fail "get did not read the block snapshot back"
scw block snapshot list zone="$ZONE" -o json | jq -e --arg i "$bsnap_id" 'any(.[]; .id == $i)' >/dev/null \
  || fail "the block snapshot is missing from the list"
scw block snapshot update snapshot-id="$bsnap_id" name=conformance-bs1-renamed zone="$ZONE" -o json \
  | jq -e '.name == "conformance-bs1-renamed"' >/dev/null || fail "block snapshot update did not carry the name"

scw block snapshot delete snapshot-id="$bsnap_id" zone="$ZONE" >/dev/null || fail "block snapshot delete rejected"
scw block volume delete volume-id="$bvol_id" zone="$ZONE" >/dev/null || fail "block volume delete rejected"
prove_end "$span"
ok "types listed, volume and snapshot created, listed, renamed, deleted"

# The VPC reads and updates SW-4 landed with no scenario (#174). Each is one
# `scw vpc` call, and the lists are what a Terraform data source reads.
echo "- the VPC surface is listed and updated"
span="$(prove_begin behaviour)"
vpc="$(scw vpc vpc create name=conformance-vpc region=fr-par -o json 2>&1)" \
  || fail "vpc create rejected: $vpc"
vpc_id="$(printf '%s' "$vpc" | jq -r '.id // empty')"
[ -n "$vpc_id" ] || fail "no id in the vpc response: $vpc"

scw vpc vpc list region=fr-par -o json | jq -e --arg i "$vpc_id" 'any(.[]; .id == $i)' >/dev/null \
  || fail "the VPC is missing from the list"
scw vpc vpc update "$vpc_id" name=conformance-vpc-2 region=fr-par -o json \
  | jq -e '.name == "conformance-vpc-2"' >/dev/null || fail "vpc update did not carry the name"

vpn="$(scw vpc private-network create name=conformance-vpc-pn vpc-id="$vpc_id" \
        subnets.0=10.185.0.0/24 region=fr-par -o json 2>&1)" \
  || fail "private network create rejected: $vpn"
vpn_id="$(printf '%s' "$vpn" | jq -r '.id // empty')"
[ -n "$vpn_id" ] || fail "no id in the private network response: $vpn"

scw vpc private-network list region=fr-par -o json \
  | jq -e --arg i "$vpn_id" 'any(.[]; .id == $i)' >/dev/null \
  || fail "the private network is missing from the list"
scw vpc private-network update "$vpn_id" name=conformance-vpc-pn-2 region=fr-par -o json \
  | jq -e '.name == "conformance-vpc-pn-2"' >/dev/null || fail "private network update did not carry the name"

# The two switches a VPC carries, and the route table under them. All three are
# CLI subcommands (`enable-dhcp`, `enable-routing`, `route update`) and none had
# ever been driven: SW-4 mounted them and the suite only ever created and
# deleted. The assertion is on the flag the emulator serves back, because an
# endpoint that answers 200 and stores nothing would satisfy the command.
scw vpc private-network enable-dhcp private-network-id="$vpn_id" region=fr-par -o json >/dev/null \
  || fail "enable-dhcp rejected"
scw vpc private-network get "$vpn_id" region=fr-par -o json \
  | jq -e '.dhcp_enabled == true' >/dev/null || fail "DHCP did not stay enabled on the private network"

scw vpc route enable-routing "$vpc_id" region=fr-par -o json \
  | jq -e '.routing_enabled == true' >/dev/null || fail "enable-routing did not come back on the VPC"

route="$(scw vpc route create vpc-id="$vpc_id" destination=192.168.77.0/24 \
          nexthop-private-network-id="$vpn_id" region=fr-par -o json 2>&1)" \
  || fail "route create rejected: $route"
route_id="$(printf '%s' "$route" | jq -r '.id // empty')"
[ -n "$route_id" ] || fail "no id in the route response: $route"
scw vpc route update "$route_id" description="conformance route" region=fr-par -o json \
  | jq -e '.description == "conformance route"' >/dev/null || fail "route update did not carry the description"
scw vpc route delete "$route_id" region=fr-par >/dev/null || fail "route delete rejected"

scw vpc private-network delete "$vpn_id" region=fr-par >/dev/null || fail "private network delete rejected"
scw vpc vpc delete "$vpc_id" region=fr-par >/dev/null || fail "vpc delete rejected"
prove_end "$span"
ok "VPC and private network created, listed, renamed, switched on, routed, deleted"

# Releasing a set rather than an address: the one IPAM call `scw ipam ip delete`
# does not make, and the only reason it was never driven. A client reaches it
# through `scw ipam ip-set release`, which is what a user runs when a whole
# network's addresses have to go at once.
echo "- a whole set of addresses is released in one call"
span="$(prove_begin behaviour)"
set_pn="$(scw vpc private-network create name=conformance-ipset subnets.0=10.186.0.0/24 \
           region=fr-par -o json 2>&1)" || fail "private network create rejected: $set_pn"
set_pn_id="$(printf '%s' "$set_pn" | jq -r '.id // empty')"
[ -n "$set_pn_id" ] || fail "no id in the private network response: $set_pn"

set_ip="$(scw ipam ip create source.private-network-id="$set_pn_id" region=fr-par -o json 2>&1)" \
  || fail "ipam ip create rejected: $set_ip"
set_ip_id="$(printf '%s' "$set_ip" | jq -r '.id // empty')"
[ -n "$set_ip_id" ] || fail "no id in the book response: $set_ip"

# ip-ids is what makes this a set: released with no id, the call is a no-op that
# answers 204, and an assertion written without one passes on an emulator that
# releases nothing (measured, on the first version of this block).
scw ipam ip-set release ip-ids.0="$set_ip_id" region=fr-par >/dev/null \
  || fail "ip-set release rejected"
# Released means gone, not merely accepted. The address the set carried must
# stop answering, and the read is what says so.
neg="$(prove_begin negative)"
if scw ipam ip get "$set_ip_id" region=fr-par -o json >/dev/null 2>&1; then
  fail "an address released with its set still answers"
fi
prove_end "$neg"
scw vpc private-network delete "$set_pn_id" region=fr-par >/dev/null \
  || fail "cleanup: private network delete rejected"
prove_end "$span"
ok "the set released, and its address gone"

# The instance/v1 reads and updates left over (#174): the three lists a client
# pages through, and the three updates a rename goes through. One call each, and
# the reason they were missed is that the suite drove the create-and-delete path
# and never the edit one.
echo "- the instance lists and the three renames"
span="$(prove_begin behaviour)"
scw instance ip list zone="$ZONE" -o json >/dev/null || fail "instance ip list rejected"
scw instance volume list zone="$ZONE" -o json >/dev/null || fail "instance volume list rejected"
scw instance snapshot list zone="$ZONE" -o json >/dev/null || fail "instance snapshot list rejected"

ivol="$(scw instance volume create name=conformance-iv size=10GB volume-type=b_ssd \
         zone="$ZONE" -o json 2>&1)" || fail "instance volume create rejected: $ivol"
ivol_id="$(printf '%s' "$ivol" | jq -r '.volume.id // .id // empty')"
[ -n "$ivol_id" ] || fail "no id in the instance volume response: $ivol"
scw instance volume update "$ivol_id" name=conformance-iv-2 zone="$ZONE" -o json \
  | jq -e '(.volume.name // .name) == "conformance-iv-2"' >/dev/null || fail "instance volume update did not carry the name"

isnap="$(scw instance snapshot create name=conformance-is volume-id="$ivol_id" \
          zone="$ZONE" -o json 2>&1)" || fail "instance snapshot create rejected: $isnap"
isnap_id="$(printf '%s' "$isnap" | jq -r '.snapshot.id // .id // empty')"
[ -n "$isnap_id" ] || fail "no id in the instance snapshot response: $isnap"
scw instance snapshot update "$isnap_id" name=conformance-is-2 zone="$ZONE" -o json \
  | jq -e '(.snapshot.name // .name) == "conformance-is-2"' >/dev/null || fail "instance snapshot update did not carry the name"

iimg="$(scw instance image create name=conformance-ii snapshot-id="$isnap_id" arch=x86_64 \
         zone="$ZONE" -o json 2>&1)" || fail "instance image create rejected: $iimg"
iimg_id="$(printf '%s' "$iimg" | jq -r '.image.id // .id // empty')"
[ -n "$iimg_id" ] || fail "no id in the instance image response: $iimg"
scw instance image update "$iimg_id" name=conformance-ii-2 zone="$ZONE" -o json \
  | jq -e '(.image.name // .name) == "conformance-ii-2"' >/dev/null || fail "instance image update did not carry the name"

scw instance image delete "$iimg_id" zone="$ZONE" >/dev/null || fail "instance image delete rejected"
scw instance snapshot delete "$isnap_id" zone="$ZONE" >/dev/null || fail "instance snapshot delete rejected"
scw instance volume delete "$ivol_id" zone="$ZONE" >/dev/null || fail "instance volume delete rejected"
prove_end "$span"
ok "three lists paged, three renames carried back"

echo "conformance: scw CLI passed"
