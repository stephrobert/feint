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

# The reboot verb, driven by the client that owns it (#547). It answered `success` on an
# action the emulator did not perform until the sequence moved into the shared layer, and
# the state a client reads afterwards is the whole of what this suite can see from here —
# that the machine really restarted is asserted from the host by
# tools/conformance/functional.sh, which compares the runtime process across the call.
echo "- reboot"
scw instance server reboot "$id" zone="$ZONE" >/dev/null || fail "reboot rejected"
state="$(scw instance server get "$id" zone="$ZONE" -o json | jq -r '.state')"
[ "$state" = "running" ] || fail "expected the server to be running after a reboot, got '$state'"
ok "rebooted, and still running"

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

# `default` is a literal segment of the path the SDK builds, not an identifier,
# and it used to match {id} on the neighbouring route and answer 404 (#432).
# Driven by the real CLI because that is the only thing that proves the segment
# is reachable: a unit test can ask for the path, and only `scw` proves the
# command a user types arrives there.
echo "- the default rule set, at the literal path the CLI asks for"
defaults="$(scw instance security-group list-default-rules zone="$ZONE" -o json)"   || fail "list-default-rules rejected: $defaults"
# `(.rules // .)` because this subcommand hands the envelope through where
# list-rules unwraps it, and the assertion must read the rules either way: a
# jq path that matched the wrapper would grade the number 6, not a rule.
printf '%s' "$defaults" | jq -e '(.rules // .) | length >= 1' >/dev/null || fail "the default rule set came back empty: $defaults"
printf '%s' "$defaults" | jq -e '(.rules // .) | all(.[]; .editable == false)' >/dev/null || fail "a default rule reads as editable, and a client cannot change these: $defaults"
printf '%s' "$defaults" | jq -e '(.rules // .) | all(.[]; .direction == "outbound" and .action == "drop")' >/dev/null || fail "a default rule is not an outbound drop: $defaults"
ok "default rules"

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

# Placement groups (#285): the record is served, the effect is not, and the CLI
# walks all nine v1 operations. The one field that could turn the record into a
# promise is policy_respected, and the assertion that matters here is the
# honest one: a max_availability group whose two members are running must read
# false, because every emulated machine shares the single host that started
# feint (docs/limits.md). The declined era's reason was "any policy would be
# reported satisfied whatever it asked"; this section is that sentence kept
# false.
echo "- placement group: create, get, update, set"
span="$(prove_begin behaviour)"
pg="$(scw instance placement-group create name=conformance-pg policy-mode=enforced \
        policy-type=max_availability zone="$ZONE" -o json)" || fail "placement group create rejected: $pg"
pg_id="$(printf '%s' "$pg" | jq -r '(.placement_group // .).id // empty')"
[ -n "$pg_id" ] || fail "no id in the placement group create response: $pg"
# The CLI's get is a custom command that joins GetPlacementGroup and
# GetPlacementGroupServers, so one call drives both operations.
read_back="$(scw instance placement-group get "$pg_id" zone="$ZONE" -o json)" || fail "get rejected: $read_back"
printf '%s' "$read_back" | jq -e '(.placement_group // .) | .policy_mode == "enforced" and .policy_type == "max_availability" and .policy_respected == true' >/dev/null \
  || fail "the empty group did not round-trip (a group with nothing running violates nothing): $read_back"
scw instance placement-group update placement-group-id="$pg_id" name=conformance-pg-2 zone="$ZONE" -o json >/dev/null \
  || fail "update rejected"
scw instance placement-group set placement-group-id="$pg_id" name=conformance-pg-3 \
  policy-mode=optional policy-type=max_availability zone="$ZONE" -o json >/dev/null || fail "set rejected"
listed="$(scw instance placement-group list zone="$ZONE" -o json)" || fail "list rejected: $listed"
printf '%s' "$listed" | jq -e --arg id "$pg_id" 'any(.[]; .id == $id and .name == "conformance-pg-3" and .policy_mode == "optional")' >/dev/null \
  || fail "the set did not stick in the list: $listed"
ok "group $pg_id"

echo "- placement group: two running members on one host read policy_respected=false"
pg_a="$(scw instance server create name=conformance-pg-a type=DEV1-S zone="$ZONE" \
          placement-group-id="$pg_id" -o json)" || fail "create in the group rejected: $pg_a"
pg_a_id="$(printf '%s' "$pg_a" | jq -r '.id')"
printf '%s' "$pg_a" | jq -e --arg id "$pg_id" '.placement_group.id == $id' >/dev/null \
  || fail "the server did not take the group: $pg_a"
pg_b="$(scw instance server create name=conformance-pg-b type=DEV1-S zone="$ZONE" -o json)" \
  || fail "create for the membership test rejected: $pg_b"
pg_b_id="$(printf '%s' "$pg_b" | jq -r '.id')"
scw instance placement-group set-servers placement-group-id="$pg_id" servers.0="$pg_a_id" servers.1="$pg_b_id" \
  zone="$ZONE" -o json >/dev/null || fail "set-servers rejected"
members="$(scw instance placement-group get-servers placement-group-id="$pg_id" zone="$ZONE" -o json)" \
  || fail "get-servers rejected: $members"
printf '%s' "$members" | jq -e --arg a "$pg_a_id" --arg b "$pg_b_id" \
  '(.servers // .) | any(.[]; .id == $a) and any(.[]; .id == $b)' >/dev/null || fail "set-servers did not stick: $members"
# Both members are running: the CLI boots a server right after creating it,
# which the very first block of this file depends on.
honest="$(scw instance placement-group get "$pg_id" zone="$ZONE" -o json)" || fail "get rejected: $honest"
printf '%s' "$honest" | jq -e '(.placement_group // .).policy_respected == false' >/dev/null \
  || fail "two running members of a spread group on one host read respected — the record became a promise nothing honours: $honest"
scw instance placement-group update-servers placement-group-id="$pg_id" servers.0="$pg_a_id" \
  zone="$ZONE" -o json >/dev/null || fail "update-servers rejected"
members="$(scw instance placement-group get-servers placement-group-id="$pg_id" zone="$ZONE" -o json)" \
  || fail "get-servers rejected after update: $members"
printf '%s' "$members" | jq -e --arg b "$pg_b_id" '(.servers // .) | any(.[]; .id == $b) | not' >/dev/null \
  || fail "update-servers kept an evicted member: $members"
scw instance server stop "$pg_a_id" zone="$ZONE" >/dev/null || fail "cleanup: poweroff rejected"
scw instance server stop "$pg_b_id" zone="$ZONE" >/dev/null || fail "cleanup: poweroff rejected"
scw instance server delete "$pg_a_id" zone="$ZONE" >/dev/null || fail "cleanup: server delete rejected"
scw instance server delete "$pg_b_id" zone="$ZONE" >/dev/null || fail "cleanup: server delete rejected"
scw instance placement-group delete "$pg_id" zone="$ZONE" >/dev/null || fail "placement group delete rejected"
prove_end "$span"
ok "membership walked, and the spread policy told the truth"

# The volume and address lifecycles, walked by the CLI rather than asserted in a
# unit test. Two whole-pack audits found every one of their defects in the gap
# between this fixture and what the pack claimed: an attached volume answering a
# state no SDK declares (which made `tofu apply` time out for five minutes), a
# volume detached on one path and not the other, and `ip delete <address>`
# answering success while the address survived. Unit tests read JSON, so they
# saw none of it. The fixture is the thing that had to grow.
echo "- a volume is attached, refuses to be deleted under its server, and comes back"
span="$(prove_begin behaviour)"
vol="$(scw instance volume create name=conformance-vol volume-type=l_ssd size=10G zone="$ZONE" -o json)" \
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

# The root disk is a BLOCK volume since #365, which is what the cloud gives a
# DEV1-S, so it is snapshotted through the product that owns it. This step is
# new and it is not decoration: it is the whole reason the subject of the
# instance snapshot below had to change.
#
# `scw instance snapshot create volume-id=<a block volume>` cannot be that
# subject, and the refusal comes from the CLI rather than from here: without
# `unified=true` the command calls instance.GetVolume itself before it sends
# anything (scaleway-cli 2.56.3, internal/namespaces/instance/v1/
# custom_snapshot.go) and returns that error. The instance route DOES resolve a
# block volume — TestAnInstanceSnapshotOfABlockVolumeDoesNotPromiseTheBlockProduct
# covers it and `unified=true` reaches it — but a fixture cannot assert it
# through a client that stops one call earlier.
root_snap="$(scw block snapshot create name=conformance-root-snap volume-id="$img_root" \
              zone="$ZONE" -o json)" || fail "block snapshot of the server root rejected: $root_snap"
root_snap_id="$(printf '%s' "$root_snap" | jq -r '(.snapshot // .).id')"
[ -n "$root_snap_id" ] && [ "$root_snap_id" != null ] \
  || fail "no id in the root snapshot response: $root_snap"
scw block snapshot get "$root_snap_id" zone="$ZONE" -o json \
  | jq -e --arg v "$img_root" '(.snapshot // .).parent_volume.id == $v' >/dev/null \
  || fail "the snapshot of the root disk does not name the root disk"
scw block snapshot delete "$root_snap_id" zone="$ZONE" >/dev/null \
  || fail "delete of the root snapshot rejected"

# An instance volume for the instance snapshot, created by the client the way a
# client does. It used to be the server's root disk, which was an instance
# volume until #365 moved it where the cloud keeps it.
img_vol="$(scw instance volume create name=conformance-golden-vol volume-type=l_ssd size=10G \
            zone="$ZONE" -o json)" || fail "volume create for the image test rejected: $img_vol"
img_vol_id="$(printf '%s' "$img_vol" | jq -r '(.volume // .).id')"
[ -n "$img_vol_id" ] && [ "$img_vol_id" != null ] || fail "no id in the volume create response: $img_vol"

snap="$(scw instance snapshot create name=conformance-snap volume-id="$img_vol_id" zone="$ZONE" -o json)" \
  || fail "snapshot create rejected: $snap"
snap_id="$(printf '%s' "$snap" | jq -r '(.snapshot // .).id')"
[ -n "$snap_id" ] && [ "$snap_id" != null ] || fail "no id in the snapshot create response: $snap"
# Immediately usable: the CLI reads state before it lets anything be cut from it.
printf '%s' "$snap" | jq -e '(.snapshot // .).state == "available"' >/dev/null \
  || fail "the snapshot is not available on creation: $snap"
scw instance snapshot get "$snap_id" zone="$ZONE" -o json \
  | jq -e --arg v "$img_vol_id" '(.snapshot // .).base_volume.id == $v' >/dev/null \
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
scw instance volume delete "$img_vol_id" zone="$ZONE" >/dev/null \
  || fail "delete of the snapshotted volume rejected"
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

# The Account product's projects (#372). Not scenery: this is the pair every
# third-party VPC stack walks before it reaches a VPC path, because
# `data "scaleway_account_project"` is evaluated ahead of every resource — the
# module of #372 died here, on a 501, in two exchanges.
#
# The list is filtered by name and the read follows the id it answered, which is
# the sequence terraform-provider-scaleway's DataSourceAccountProjectRead walks.
# NO SPAN, and the emulator is what settled that: a project here is fixed
# inventory, like the catalogue and the default rule set above, so this block
# creates nothing and refuses nothing. A `behaviour` span was tried and the
# emulator refused to close it — "the span declared a lifecycle and the store
# observed no resource created and then destroyed inside it" — and so was a
# `negative` one, refused because an empty list is a 200 rather than a refusal.
# Both refusals were right, and both are the reason this signal is worth
# anything: the axes are raised by what the emulator observes, never by what a
# suite claims. The `driven` axis needs no span; the request itself is what
# raises it.
echo "- the account's project is listed by name and read back by id"
projects="$(scw account project list name=default -o json 2>&1)" \
  || fail "project list rejected: $projects"
project_id="$(printf '%s' "$projects" | jq -r '.[0].id // empty')"
[ -n "$project_id" ] || fail "the list carries no project: $projects"
printf '%s' "$projects" | jq -e '.[0].name == "default"' >/dev/null \
  || fail "the project the list names is not the default one: $projects"
scw account project get project-id="$project_id" -o json \
  | jq -e --arg i "$project_id" '.id == $i and (.organization_id | length) > 0' >/dev/null \
  || fail "get did not read back the project the list named"
# NOT a negative span, and the distinction is the axis's whole meaning: an empty
# list is a 200, not a refusal. The negative axis is raised by a client meeting a
# 4xx, and demanding one here made the span fail with "the span demanded a
# refusal and the emulator answered no client with a 4xx inside it" — which was
# the harness being right about a filter that filters. A name this emulator does
# not carry answers nothing, and that is an answer.
if scw account project list name=a-project-this-emulator-never-named -o json | jq -e 'length > 0' >/dev/null; then
  fail "a name nothing carries answered a project"
fi
# The case #572 is about, and the reason the emulator this suite drives is
# started with `--projects default,platform-prod`. Until 2026-08-29 the catalogue
# held one project, so a stack whose `project_name` was its own production
# project died on the provider's FindExact after that truthful empty list — the
# obstacle for exactly the person #372 exists to serve. The operator declares the
# catalogue now, and the declared name resolves the whole way: listed by name,
# read back by the identifier the list answered, under its own name.
# Asked of the emulator rather than assumed, because one leg of the conformance
# matrix starts the shipped container with NO flags on purpose — passing
# `--projects` there would prove a different image. The catalogue is read
# unfiltered: an emulator nobody gave one holds a single project and this block
# skips by name; a broken filter cannot shrink an unfiltered list to one, so the
# skip cannot hide the defect the assertion exists for.
echo "- a declared project that is not the default one resolves by name and by id"
catalogue="$(scw account project list -o json 2>&1)" \
  || fail "listing the account's projects rejected: $catalogue"
if [ "$(printf '%s' "$catalogue" | jq -r 'length')" -lt 2 ]; then
  echo "  SKIP: this emulator declares no project catalogue (serve --projects), so a name that is not the default one has nothing to resolve to (#572)"
else
declared="$(scw account project list name=platform-prod -o json 2>&1)" \
  || fail "listing the declared project rejected: $declared"
printf '%s' "$declared" | jq -e 'length == 1 and .[0].name == "platform-prod"' >/dev/null \
  || fail "the declared project did not list under its own name: $declared"
declared_id="$(printf '%s' "$declared" | jq -r '.[0].id // empty')"
[ -n "$declared_id" ] || fail "the declared project carries no id: $declared"
[ "$declared_id" != "$project_id" ] \
  || fail "the declared project shares the default project's identifier, so the catalogue holds one thing under two names"
scw account project get project-id="$declared_id" -o json \
  | jq -e --arg i "$declared_id" '.id == $i and .name == "platform-prod"' >/dev/null \
  || fail "reading the declared project back answered another project's name"
ok "the declared project resolves by name and reads back as itself"
fi

ok "listed by name, read by id, and a foreign name answers nothing"

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

# The VPC's Network ACL, through the CLI's own subcommands. This is the client
# that measured the refusal: `scw vpc rule get` took a 501 here on 2026-08-21,
# and it is the read a user makes before ever setting anything (#343).
#
# The empty read comes first, and the value it asserts is the one the real cloud
# answered for a VPC nobody had touched — measured the same day against the
# maintainer's own default VPC, creating nothing. An emulator answering the
# SDK's protobuf zero (`unknown_action`) would satisfy "the route is mounted"
# and be wrong about what a client reads.
scw vpc rule get vpc-id="$vpc_id" region=fr-par is-ipv6=false -o json \
  | jq -e '.rules == [] and .default_policy == "accept"' >/dev/null \
  || fail "an unset ACL does not answer what the cloud answers"
scw vpc rule set vpc-id="$vpc_id" region=fr-par is-ipv6=false default-policy=drop \
  rules.0.protocol=TCP rules.0.source=0.0.0.0/0 rules.0.destination=10.187.0.0/24 \
  rules.0.src-port-low=0 rules.0.src-port-high=0 \
  rules.0.dst-port-low=22 rules.0.dst-port-high=22 \
  rules.0.action=accept rules.0.description=ssh -o json \
  | jq -e '.default_policy == "drop" and (.rules | length) == 1' >/dev/null \
  || fail "the ACL the CLI set did not come back"
# Read back through the other door: an endpoint that answers the PUT from the
# request body and stores nothing satisfies the line above and fails this one.
scw vpc rule get vpc-id="$vpc_id" region=fr-par is-ipv6=false -o json \
  | jq -e '.rules[0].dst_port_low == 22 and .rules[0].description == "ssh"' >/dev/null \
  || fail "the ACL the CLI set is not the one the read answers"

scw vpc private-network delete "$vpn_id" region=fr-par >/dev/null || fail "private network delete rejected"
scw vpc vpc delete "$vpc_id" region=fr-par >/dev/null || fail "vpc delete rejected"
prove_end "$span"
ok "VPC and private network created, listed, renamed, switched on, routed, filtered by an ACL read back through both doors, deleted"

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

ivol="$(scw instance volume create name=conformance-iv size=10GB volume-type=l_ssd \
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

# The Load Balancer chain (#282): the shape two surveyed stacks and Scaleway's
# own module reach for — an IP, the balancer, a backend with a health check, a
# frontend with an ACL, the Private Network attachment resolved back through
# IPAM. The emulator records this configuration and forwards nothing, so the
# only honest assertions are round-trips and refusals; stats stay declined.
echo "- the load balancer chain"
span="$(prove_begin behaviour)"
lb_ip="$(scw lb ip create is-ipv6=false zone="$ZONE" -o json 2>&1)" || fail "lb ip create rejected: $lb_ip"
lb_ip_id="$(printf '%s' "$lb_ip" | jq -r '.id // empty')"
[ -n "$lb_ip_id" ] || fail "no id in the lb ip response: $lb_ip"

lb="$(scw lb lb create name=conformance-lb type=LB-S ip-ids.0="$lb_ip_id" zone="$ZONE" -o json 2>&1)" \
  || fail "lb create rejected: $lb"
lb_id="$(printf '%s' "$lb" | jq -r '.id // empty')"
[ -n "$lb_id" ] || fail "no id in the lb response: $lb"
printf '%s' "$lb" | jq -e '.status == "ready"' >/dev/null || fail "the balancer is not ready: $lb"
scw lb lb list zone="$ZONE" -o json | jq -e --arg id "$lb_id" 'any(.[]; .id == $id)' >/dev/null \
  || fail "the balancer is missing from the list"

backend="$(scw lb backend create lb-id="$lb_id" name=conformance-be forward-protocol=tcp \
  forward-port=8080 server-ip.0=172.16.8.10 health-check.port=8080 zone="$ZONE" -o json 2>&1)" \
  || fail "backend create rejected: $backend"
backend_id="$(printf '%s' "$backend" | jq -r '.id // empty')"
[ -n "$backend_id" ] || fail "no id in the backend response: $backend"
scw lb backend get "$backend_id" zone="$ZONE" -o json \
  | jq -e '.pool == ["172.16.8.10"]' >/dev/null || fail "the backend pool did not round-trip"

frontend="$(scw lb frontend create lb-id="$lb_id" backend-id="$backend_id" name=conformance-fe \
  inbound-port=8080 zone="$ZONE" -o json 2>&1)" || fail "frontend create rejected: $frontend"
frontend_id="$(printf '%s' "$frontend" | jq -r '.id // empty')"
[ -n "$frontend_id" ] || fail "no id in the frontend response: $frontend"

acl="$(scw lb acl create frontend-id="$frontend_id" name=conformance-deny index=0 \
  action.type=deny match.ip-subnet.0=0.0.0.0/0 zone="$ZONE" -o json 2>&1)" \
  || fail "acl create rejected: $acl"
acl_id="$(printf '%s' "$acl" | jq -r '.id // empty')"
[ -n "$acl_id" ] || fail "no id in the acl response: $acl"

route="$(scw lb route create frontend-id="$frontend_id" backend-id="$backend_id" \
  match.host-header=app.example.org zone="$ZONE" -o json 2>&1)" || fail "route create rejected: $route"
route_id="$(printf '%s' "$route" | jq -r '.id // empty')"
[ -n "$route_id" ] || fail "no id in the route response: $route"

# The attachment books an address in the network's own pool, and the provider
# reads it back through IPAM filtered by resource_type=lb_server: both halves
# asserted, because an attach whose address IPAM cannot resolve is half a product.
lb_pn="$(scw vpc private-network create name=conformance-lb-pn subnets.0=172.16.8.0/24 region=fr-par -o json)" \
  || fail "private network create rejected: $lb_pn"
lb_pn_id="$(printf '%s' "$lb_pn" | jq -r '.id // empty')"
scw lb private-network attach "$lb_id" private-network-id="$lb_pn_id" zone="$ZONE" -o json >/dev/null \
  || fail "private-network attach rejected"
scw lb private-network list "$lb_id" zone="$ZONE" -o json \
  | jq -e --arg pn "$lb_pn_id" 'any(.[]; .private_network_id == $pn and .status == "ready")' >/dev/null \
  || fail "the attachment is missing or not ready"
scw ipam ip list resource-type=lb_server resource-id="$lb_id" region=fr-par -o json \
  | jq -e 'length == 1' >/dev/null || fail "IPAM does not resolve the balancer's private address"

# The lists a client pages through and the edits a rename goes through, one
# call each — the #174 lesson: the create-and-delete path leaves every edit
# path unproven.
scw lb ip list zone="$ZONE" -o json | jq -e --arg id "$lb_ip_id" 'any(.[]; .id == $id)' >/dev/null \
  || fail "the lb ip is missing from the list"
scw lb ip update "$lb_ip_id" reverse=lb.example.org zone="$ZONE" -o json \
  | jq -e '.reverse == "lb.example.org"' >/dev/null || fail "lb ip update did not carry the reverse"
scw lb backend list lb-id="$lb_id" zone="$ZONE" -o json \
  | jq -e --arg id "$backend_id" 'any(.[]; .id == $id)' >/dev/null || fail "the backend is missing from the list"
scw lb backend update "$backend_id" name=conformance-be-2 forward-protocol=tcp forward-port=8080 \
  forward-port-algorithm=roundrobin sticky-sessions=none on-marked-down-action=on_marked_down_action_none \
  zone="$ZONE" -o json | jq -e '.name == "conformance-be-2"' >/dev/null \
  || fail "backend update did not carry the name"
scw lb backend set-servers "$backend_id" server-ip.0=172.16.8.11 zone="$ZONE" -o json \
  | jq -e '.pool == ["172.16.8.11"]' >/dev/null || fail "set-servers did not replace the pool"
scw lb backend update-healthcheck backend-id="$backend_id" port=8081 check-max-retries=3 check-delay=3s check-timeout=1s zone="$ZONE" -o json \
  | jq -e '.port == 8081' >/dev/null || fail "update-healthcheck did not carry the port"
scw lb frontend list lb-id="$lb_id" zone="$ZONE" -o json \
  | jq -e --arg id "$frontend_id" 'any(.[]; .id == $id)' >/dev/null || fail "the frontend is missing from the list"
scw lb frontend update "$frontend_id" name=conformance-fe-2 inbound-port=8080 backend-id="$backend_id" \
  zone="$ZONE" -o json | jq -e '.name == "conformance-fe-2"' >/dev/null \
  || fail "frontend update did not carry the name"
scw lb acl get "$acl_id" zone="$ZONE" -o json | jq -e '.name == "conformance-deny"' >/dev/null \
  || fail "acl get did not answer the acl"
scw lb acl update "$acl_id" name=conformance-deny-2 action.type=deny index=0 zone="$ZONE" -o json \
  | jq -e '.name == "conformance-deny-2"' >/dev/null || fail "acl update did not carry the name"
scw lb route get "$route_id" zone="$ZONE" -o json \
  | jq -e '.match.host_header == "app.example.org"' >/dev/null || fail "route get lost its match"
scw lb route update "$route_id" backend-id="$backend_id" match.host-header=app2.example.org \
  zone="$ZONE" -o json | jq -e '.match.host_header == "app2.example.org"' >/dev/null \
  || fail "route update did not carry the match"
scw lb route list zone="$ZONE" -o json | jq -e --arg id "$route_id" 'any(.[]; .id == $id)' >/dev/null \
  || fail "the route is missing from the list"

# The wrong destroy order gets a refusal, never a silent success: a backend a
# frontend forwards to must not vanish under it.
neg="$(prove_begin negative)"
if scw lb backend delete "$backend_id" zone="$ZONE" >/dev/null 2>&1; then
  fail "deleting a backend still used by a frontend was accepted"
fi
prove_end "$neg"

scw lb route delete "$route_id" zone="$ZONE" >/dev/null || fail "route delete rejected"

# THE ORPHAN LINE IN EVERY CONFORMANCE LOG COMES FROM HERE (#505).
#
# scw 2.56.3 prints "runtime error: invalid memory address or nil pointer
# dereference" on the stderr of this command, and exits 0 having deleted the
# ACL. The fault is upstream and entirely client-side, read rather than guessed
# (scaleway-cli v2.56.3, internal/namespaces/lb/v1/custom_acl.go): the
# interceptor on the four ACL verbs asserts *ZonedAPIDeleteCertificateRequest
# where the argument is *ZonedAPIDeleteACLRequest, so its getACL stays nil, and
# a delete that SUCCEEDS then dereferences getACL.Frontend.LB.Tags. The three
# sibling verbs answer an *lb.ACL rather than a *core.SuccessResult and never
# reach that branch, which is why only this line prints it.
#
# Nothing this emulator answers can avoid it: ZonedAPI.DeleteACL decodes the
# response into nil (lb_sdk.go), so no body reaches the faulty path, and the
# command builds its SuccessResult unconditionally on a nil error. The only
# lever left is to FAIL a delete that worked — measured on 2026-08-28 with a
# fault rule: the panic goes, and the ACL survives. docs/limits.md carries the
# whole measurement.
#
# So the noise is tolerated, and this asserts what makes tolerating it honest:
# the delete did its work. Without this line, "rc=0 and a panic on stderr" is
# taken on trust.
scw lb acl delete "$acl_id" zone="$ZONE" >/dev/null || fail "acl delete rejected"
scw lb acl list frontend-id="$frontend_id" zone="$ZONE" -o json \
  | jq -e --arg id "$acl_id" 'all(.[]; .id != $id)' >/dev/null \
  || fail "the acl survived a delete that answered 0 (#505 is noise, not a failed delete)"
scw lb frontend delete "$frontend_id" zone="$ZONE" >/dev/null || fail "frontend delete rejected"
scw lb backend delete "$backend_id" zone="$ZONE" >/dev/null || fail "backend delete rejected"
scw lb private-network detach "$lb_id" private-network-id="$lb_pn_id" zone="$ZONE" >/dev/null \
  || fail "private-network detach rejected"
scw lb lb delete "$lb_id" zone="$ZONE" >/dev/null || fail "lb delete rejected"
# The address survives its balancer unless released: kubic's whole demand.
scw lb ip get "$lb_ip_id" zone="$ZONE" -o json | jq -e '.lb_id == null' >/dev/null \
  || fail "the address did not survive its balancer detached"
scw lb ip delete "$lb_ip_id" zone="$ZONE" >/dev/null || fail "lb ip delete rejected"
scw vpc private-network delete "$lb_pn_id" region=fr-par >/dev/null \
  || fail "cleanup: private network delete rejected"
prove_end "$span"
ok "the balancer chain round-tripped, its address resolved through IPAM, and the wrong destroy order was refused"

# The Public Gateway chain (#282): IP, gateway, connection with the IPAM
# config — the path terraform-talos and Scaleway's own VPC module walk. Only
# vpc-gw/v2 is served, which is what scw 2.56.3 drives (measured with -D).
echo "- the public gateway chain"
span="$(prove_begin behaviour)"
gw_ip="$(scw vpc-gw ip create zone="$ZONE" -o json 2>&1)" || fail "vpc-gw ip create rejected: $gw_ip"
gw_ip_id="$(printf '%s' "$gw_ip" | jq -r '.id // empty')"
[ -n "$gw_ip_id" ] || fail "no id in the gateway ip response: $gw_ip"

gw="$(scw vpc-gw gateway create name=conformance-gw type=VPC-GW-S ip-id="$gw_ip_id" zone="$ZONE" -o json 2>&1)" \
  || fail "gateway create rejected: $gw"
gw_id="$(printf '%s' "$gw" | jq -r '.id // empty')"
[ -n "$gw_id" ] || fail "no id in the gateway response: $gw"
printf '%s' "$gw" | jq -e '.status == "running"' >/dev/null || fail "the gateway is not running: $gw"

gw_pn="$(scw vpc private-network create name=conformance-gw-pn subnets.0=172.16.9.0/24 region=fr-par -o json)" \
  || fail "private network create rejected: $gw_pn"
gw_pn_id="$(printf '%s' "$gw_pn" | jq -r '.id // empty')"

gn="$(scw vpc-gw gateway-network create gateway-id="$gw_id" private-network-id="$gw_pn_id" \
  enable-masquerade=true push-default-route=true zone="$ZONE" -o json 2>&1)" \
  || fail "gateway-network create rejected: $gn"
gn_id="$(printf '%s' "$gn" | jq -r '.id // empty')"
[ -n "$gn_id" ] || fail "no id in the gateway-network response: $gn"
printf '%s' "$gn" | jq -e '.status == "ready"' >/dev/null || fail "the connection is not ready: $gn"
# The connection's address is a first-class IPAM citizen, exactly what the
# Terraform provider reads back (resource_type=vpc_gateway_network).
scw ipam ip list resource-type=vpc_gateway_network resource-id="$gn_id" region=fr-par -o json \
  | jq -e 'length == 1' >/dev/null || fail "IPAM does not resolve the connection's address"

# The same #174 discipline for this family: every list paged, every edit
# carried back.
scw vpc-gw ip list zone="$ZONE" -o json | jq -e --arg id "$gw_ip_id" 'any(.[]; .id == $id)' >/dev/null \
  || fail "the gateway ip is missing from the list"
scw vpc-gw ip update "$gw_ip_id" reverse=gw.example.org zone="$ZONE" -o json \
  | jq -e '.reverse == "gw.example.org"' >/dev/null || fail "gateway ip update did not carry the reverse"
scw vpc-gw gateway list zone="$ZONE" -o json | jq -e --arg id "$gw_id" 'any(.[]; .id == $id)' >/dev/null \
  || fail "the gateway is missing from the list"
scw vpc-gw gateway update "$gw_id" name=conformance-gw-2 zone="$ZONE" -o json \
  | jq -e '.name == "conformance-gw-2"' >/dev/null || fail "gateway update did not carry the name"
scw vpc-gw gateway-network list zone="$ZONE" -o json \
  | jq -e --arg id "$gn_id" 'any(.[]; .id == $id)' >/dev/null || fail "the connection is missing from the list"
scw vpc-gw gateway-network get "$gn_id" zone="$ZONE" -o json \
  | jq -e '.push_default_route == true' >/dev/null || fail "the connection lost its default route flag"
scw vpc-gw gateway-network update "$gn_id" enable-masquerade=false zone="$ZONE" -o json \
  | jq -e '.masquerade_enabled == false' >/dev/null || fail "gateway-network update did not carry masquerade"

# A gateway that still carries a connection does not vanish under it.
neg="$(prove_begin negative)"
if scw vpc-gw gateway delete "$gw_id" zone="$ZONE" >/dev/null 2>&1; then
  fail "deleting a connected gateway was accepted"
fi
prove_end "$neg"

scw vpc-gw gateway-network delete "$gn_id" zone="$ZONE" >/dev/null || fail "gateway-network delete rejected"
scw vpc-gw gateway delete "$gw_id" delete-ip=true zone="$ZONE" >/dev/null || fail "gateway delete rejected"
neg="$(prove_begin negative)"
if scw vpc-gw gateway get "$gw_id" zone="$ZONE" -o json >/dev/null 2>&1; then
  fail "the gateway still exists after delete"
fi
prove_end "$neg"
scw vpc private-network delete "$gw_pn_id" region=fr-par >/dev/null \
  || fail "cleanup: private network delete rejected"
prove_end "$span"
ok "the gateway chain round-tripped, its address resolved through IPAM, and the wrong destroy order was refused"

# The three values every refusal below is composed from. They are chosen to be
# unmintable rather than merely unused: knownZones and knownRegions (servers.go,
# vpc.go) are closed lists, and no Scaleway place is named xx-yyy.
UNKNOWN_ID="99999999-9999-4999-8999-999999999999"
NOWHERE_ZONE="xx-yyy-1"
NOWHERE_REGION="xx-yyy"
REGION="${REGION:-fr-par}"

# The refusals a real client can ask for, on the operations nothing ever refused.
#
# `negative` is earned by an operation really answering 4xx to a real client
# inside a span where a suite demanded a refusal (#428). Seventy-six Scaleway
# operations stood at zero, and the reason was never that the emulator lacked
# the behaviour: nobody had ever asked it to say no on those calls. The corpus
# of recorded cloud refusals (refusals.sh, #390) covers the reads and writes a
# recording session drove at bogus identifiers, and stops there.
#
# Nothing below arms a fault. It could not: an injected refusal leaves the
# observed path before the observer records it, and a span whose only 4xx were
# injected is refused outright by the emulator. Every refusal here is this
# emulator's own rule meeting a request the real CLI composed.
#
# TWO SHAPES, AND THE ORDER MATTERS. Where an operation owns a refusal of its
# own — an identifier that names nothing, a resource still in use, a state that
# forbids the call — that is the one driven, because it exercises the handler's
# subject. Where it owns none that `scw` can compose, the request is sent at a
# zone or a region that names no place. That refusal is real and it is this
# emulator's own (`zoneOf`/`regionOf` answer `invalid_arguments` naming the
# segment, and `knownZones` says why), but it is worth stating what it proves
# and what it does not: the route is mounted, its handler runs and refuses a
# scope it does not serve. It says nothing about how that operation treats a
# bad payload. Anything stronger is used where it exists.
#
# WHAT `scw` CANNOT ASK FOR, MEASURED RATHER THAN ASSUMED. The CLI validates
# enums against its own SDK (`state=bogus`, `order-by=nope` never leave the
# machine) and resolves a target before acting, so `server delete <unknown>`,
# `lb delete <unknown>` and `snapshot create volume-id=<unknown>` are answered
# by the GET that precedes them and the write is never sent. That ceiling is
# why refuse_scw fails a case whose refusal never reached the emulator instead
# of passing it: a case that stops reaching the API measures nothing, and it
# would look exactly like this suite working.

# refuse_scw names the client; everything else is refuse_client's, in
# tools/conformance/prove.sh, because the rule is the same for all three clouds
# and a control recopied into each suite is a control one suite forgets.
refuse_scw() { # label args...
  local label="$1"; shift
  refuse_client "$label" scw "$@"
}

echo "- the refusals each operation owns"

# An identifier that names nothing, on the operations whose own subject is that
# identifier. `scw` sends these: they are path segments and request fields, not
# enums it can check.
refuse_scw "placement group get"      instance placement-group get "$UNKNOWN_ID" zone="$ZONE" -o json
refuse_scw "attach volume"            instance server attach-volume server-id="$UNKNOWN_ID" volume-id="$UNKNOWN_ID" zone="$ZONE" -o json
refuse_scw "detach volume"            instance server detach-volume server-id="$UNKNOWN_ID" volume-id="$UNKNOWN_ID" zone="$ZONE" -o json
refuse_scw "image from a snapshot"    instance image create name=refused snapshot-id="$UNKNOWN_ID" arch=x86_64 zone="$ZONE" -o json
refuse_scw "placement group servers"  instance placement-group update-servers placement-group-id="$UNKNOWN_ID" servers.0="$UNKNOWN_ID" zone="$ZONE" -o json
refuse_scw "security group rules"     instance security-group set-rules security-group-id="$UNKNOWN_ID" zone="$ZONE" -o json
refuse_scw "user data"                instance user-data set server-id="$UNKNOWN_ID" key=refused content=refused zone="$ZONE"
refuse_scw "load balancer backend"    lb backend create lb-id="$UNKNOWN_ID" name=refused forward-protocol=tcp forward-port=80 \
                                        forward-port-algorithm=roundrobin sticky-sessions=none health-check.port=80 zone="$ZONE" -o json
refuse_scw "load balancer attach"     lb private-network attach lb-id="$UNKNOWN_ID" private-network-id="$UNKNOWN_ID" zone="$ZONE" -o json
refuse_scw "load balancer from an ip" lb lb create name=refused type=LB-S ip-ids.0="$UNKNOWN_ID" zone="$ZONE" -o json
refuse_scw "block volume from a snapshot" block volume create name=refused from-snapshot.snapshot-id="$UNKNOWN_ID" perf-iops=5000 zone="$ZONE" -o json
refuse_scw "block snapshot of a volume"   block snapshot create name=refused volume-id="$UNKNOWN_ID" zone="$ZONE" -o json
refuse_scw "acl of a vpc"             vpc rule set vpc-id="$UNKNOWN_ID" is-ipv6=false default-policy=drop region="$REGION" -o json
ok "an identifier that names nothing is refused where it is the subject"

# A state that forbids the call. DeleteServer is the one write of this pack that
# `scw` reaches at all with a real target: the CLI resolves the server first, so
# the only way to the DELETE is a server that exists and refuses to be deleted.
echo "- a running server does not delete"
doomed="$(scw instance server create name=conformance-refused type=DEV1-S zone="$ZONE" -o json)" \
  || fail "create rejected: $doomed"
doomed_id="$(printf '%s' "$doomed" | jq -r '.id // empty')"
[ -n "$doomed_id" ] || fail "no id in the create response: $doomed"
refuse_scw "delete a running server"  instance server delete "$doomed_id" zone="$ZONE"
scw instance server stop "$doomed_id" zone="$ZONE" >/dev/null || fail "cleanup: poweroff rejected"
scw instance server delete "$doomed_id" zone="$ZONE" >/dev/null || fail "cleanup: delete rejected once stopped"
ok "the running server was kept, and deleted once stopped"

# A zone and a region that name no place, for the operations that own no other
# refusal a client can compose. See the note above on what this proves.
echo "- a zone and a region that name no place"
refuse_scw "instance ip list"          instance ip list zone="$NOWHERE_ZONE" -o json
refuse_scw "instance image list"       instance image list zone="$NOWHERE_ZONE" -o json
refuse_scw "instance volume list"      instance volume list zone="$NOWHERE_ZONE" -o json
refuse_scw "instance snapshot list"    instance snapshot list zone="$NOWHERE_ZONE" -o json
refuse_scw "security group list"       instance security-group list zone="$NOWHERE_ZONE" -o json
refuse_scw "placement group list"      instance placement-group list zone="$NOWHERE_ZONE" -o json
refuse_scw "instance server list"      instance server list zone="$NOWHERE_ZONE" -o json
refuse_scw "instance ip create"        instance ip create zone="$NOWHERE_ZONE" -o json
refuse_scw "security group create"     instance security-group create name=refused zone="$NOWHERE_ZONE" -o json
refuse_scw "placement group create"    instance placement-group create name=refused zone="$NOWHERE_ZONE" -o json
refuse_scw "instance volume create"    instance volume create name=refused size=10GB volume-type=l_ssd zone="$NOWHERE_ZONE" -o json
# The create the CLI walks in four calls: the type catalogue, the default image,
# the address, then the server. It is refused on the first three, which is where
# `GetImage` and `ListServersTypes` earn theirs — `GetImage` owns no refusal of
# its own, an identifier it never minted being served on purpose (docs/limits.md,
# and the divergence that decision costs is corpus/accepted.json's #392).
refuse_scw "instance server create"    instance server create name=refused type=DEV1-S zone="$NOWHERE_ZONE" -o json
refuse_scw "block volume list"         block volume list zone="$NOWHERE_ZONE" -o json
refuse_scw "block snapshot list"       block snapshot list zone="$NOWHERE_ZONE" -o json
refuse_scw "block volume type list"    block volume-type list zone="$NOWHERE_ZONE" -o json
refuse_scw "load balancer list"        lb lb list zone="$NOWHERE_ZONE" -o json
refuse_scw "load balancer ip list"     lb ip list zone="$NOWHERE_ZONE" -o json
refuse_scw "load balancer ip create"   lb ip create zone="$NOWHERE_ZONE" -o json
refuse_scw "gateway list"              vpc-gw gateway list zone="$NOWHERE_ZONE" -o json
refuse_scw "gateway network list"      vpc-gw gateway-network list zone="$NOWHERE_ZONE" -o json
refuse_scw "gateway ip list"           vpc-gw ip list zone="$NOWHERE_ZONE" -o json
refuse_scw "gateway ip create"         vpc-gw ip create zone="$NOWHERE_ZONE" -o json
refuse_scw "vpc list"                  vpc vpc list region="$NOWHERE_REGION" -o json
refuse_scw "private network list"      vpc private-network list region="$NOWHERE_REGION" -o json
refuse_scw "vpc create"                vpc vpc create name=refused region="$NOWHERE_REGION" -o json
refuse_scw "private network dhcp"      vpc private-network enable-dhcp private-network-id="$UNKNOWN_ID" region="$NOWHERE_REGION" -o json
refuse_scw "ipam ip list"              ipam ip list region="$NOWHERE_REGION" -o json
refuse_scw "ipam ip set release"       ipam ip-set release region="$NOWHERE_REGION" -o json
ok "every one of them refused by name"

# UpdatePrivateNIC, served since #624 because the decline's reason had stopped
# describing the code. Driven by the CLI rather than by curl, because the whole
# argument for serving it was that a client reaches it: `scw instance private-nic
# update` is that client, and a route no client drives is a route this project
# counts as unproven.
echo "- a private NIC is retagged, and a second identical update changes nothing"
span="$(prove_begin behaviour)"
nic_pn2="$(scw vpc private-network create name=conformance-nic-tags subnets.0=10.186.0.0/24 \
             region=fr-par -o json)" || fail "private network create rejected: $nic_pn2"
nic_pn2_id="$(printf '%s' "$nic_pn2" | jq -r '.id')"
tag_server="$(scw instance server create name=conformance-nic-tags type=DEV1-S zone="$ZONE" -o json 2>&1)" \
  || fail "create rejected by the CLI: $tag_server"
tag_server_id="$(printf '%s' "$tag_server" | jq -r '.id // empty')"
[ -n "$tag_server_id" ] || fail "no id in the create response: $tag_server"

tag_nic="$(scw instance private-nic create server-id="$tag_server_id" \
             private-network-id="$nic_pn2_id" tags.0=before zone="$ZONE" -o json 2>&1)" \
  || fail "private nic create rejected: $tag_nic"
tag_nic_id="$(printf '%s' "$tag_nic" | jq -r '.id // .private_nic.id // empty')"
[ -n "$tag_nic_id" ] || fail "no id in the private nic response: $tag_nic"

# The update is driven by the CLI and verified by the read, deliberately.
#
# `scw instance private-nic update` cannot decode this answer — and it cannot
# decode it against the real Scaleway either. The Go SDK reads the body into
# PrivateNIC directly while the cloud answers a {"private_nic": …} envelope
# (corpus/scaleway/scw-billed-shapes.jsonl seq 24), so the CLI prints an empty
# object in both places. The emulator reproduces the cloud rather than repairing
# the client, so the assertion goes through `get`, which both agree on.
scw instance private-nic update server-id="$tag_server_id" private-nic-id="$tag_nic_id" \
  tags.0=after zone="$ZONE" -o json >/dev/null \
  || fail "the update was refused"
scw instance private-nic get server-id="$tag_server_id" private-nic-id="$tag_nic_id" \
  zone="$ZONE" -o json | jq -e '(.tags // .private_nic.tags) == ["after"]' >/dev/null \
  || fail "the update did not carry the tag back"

# The property a Day-2 module is built on: read, compare, write only on a
# difference, and a second identical run reports no change. Here that means the
# same request twice leaves the same tags rather than accumulating or clearing.
scw instance private-nic update server-id="$tag_server_id" private-nic-id="$tag_nic_id" \
  tags.0=after zone="$ZONE" -o json >/dev/null || fail "the second update was refused"
scw instance private-nic get server-id="$tag_server_id" private-nic-id="$tag_nic_id" \
  zone="$ZONE" -o json | jq -e '(.tags // .private_nic.tags) == ["after"]' >/dev/null \
  || fail "a second identical update changed the tags"

scw instance server stop "$tag_server_id" zone="$ZONE" >/dev/null || fail "cleanup: poweroff rejected"
scw instance server delete "$tag_server_id" zone="$ZONE" >/dev/null || fail "cleanup: delete rejected"
scw vpc private-network delete "$nic_pn2_id" region=fr-par >/dev/null \
  || fail "cleanup: private network delete rejected"
prove_end "$span"
ok "retagged, and idempotent on a second run"

echo "conformance: scw CLI passed"
