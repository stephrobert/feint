#!/usr/bin/env bash
# Conformance check: drive the emulator with the real Exoscale CLI.
#
# `exo` is redirected through EXOSCALE_API_ENDPOINT, and that value must carry
# the /v2 suffix: the CLI concatenates it with the route it wants rather than
# adding a version segment of its own. Both facts were measured, the suffix by
# putting a logging proxy between the CLI and the emulator, the variable by
# pointing it at a dead port and reading the error name that port.
#
# It replaces a generated configuration file, which this suite wrote for as long
# as the variable was believed not to exist. `exo -C` refuses a file that is not
# there, so nothing here may pass -C any more.
#
# The order the CLI works in was measured the same way, and it is why this suite
# exists at all. `exo compute instance create` issues, before it posts anything:
# GET /zone, GET /instance-type, POST /ssh-key, GET /template. Every one of those
# was declined by this emulator until the proxy showed them going past. A unit
# test would never have found them.
#
# Usage: tools/conformance/exoscale/exo-cli.sh [endpoint]   (default http://127.0.0.1:4599)
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

command -v exo >/dev/null 2>&1 || { echo "FAIL: exo is not installed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
set +a

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

export EXOSCALE_API_ENDPOINT=${ENDPOINT}/v2

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }
exoc() { exo "$@"; }

echo "conformance: exo CLI against $ENDPOINT"

# The zone list is the first call the CLI makes and the address every call after
# it uses. If this points anywhere but here, the next request leaves for the real
# cloud — which is the one failure an emulator must never have.
echo "- the zone list, which is where every other call gets its address"
zones="$(exoc -O json zone)" || fail "zone rejected: $zones"
printf '%s' "$zones" | jq -e 'length > 0' >/dev/null || fail "no zone on offer: $zones"
# ch-dk-2 is the CLI's own default. Serving only one zone made every command fail
# with "find zone: not found in ListZonesResponse" before it called anything.
printf '%s' "$zones" | jq -e 'any(.[]; .name == "ch-dk-2")' >/dev/null \
  || fail "the CLI default zone ch-dk-2 is missing: every unflagged command fails"
# Exactly one. The CLI queries every zone it is told about and merges the
# answers, so eight zones pointing at one emulator turn one instance into eight
# identical rows — which is what happened, and how this assertion got written.
printf '%s' "$zones" | jq -e 'length == 1' >/dev/null \
  || fail "more than one zone points at this emulator: every resource will be listed once per zone"
ok "one zone, ch-dk-2, which is the CLI's own default"

echo "- the inventory a create walks before it posts anything"
templates="$(exoc -O json compute instance-template list)" || fail "template list rejected: $templates"
template_name="$(printf '%s' "$templates" | jq -r '.[0].name // empty')"
[ -n "$template_name" ] || fail "no template on offer: $templates"
types="$(exoc -O json compute instance-type list)" || fail "instance-type list rejected: $types"
# The CLI renames the field: their API declares `size`, `exo -O json` prints it
# as `name`. The suite reads what the client prints, because that is what a user
# would copy.
type_name="$(printf '%s' "$types" | jq -r '"\(.[0].family).\(.[0].name)"')"
case "$type_name" in
  *null*|.) fail "no instance type on offer: $types" ;;
esac
ok "$template_name, $type_name"

echo "- ssh keys, which the create registers on its own before posting"
span="$(prove_begin behaviour)"
keys_before="$(exoc -O json compute ssh-key list)" || fail "ssh-key list rejected: $keys_before"
printf '%s' "$keys_before" | jq -e 'length == 0' >/dev/null \
  || fail "a fresh account already holds SSH keys: $keys_before"
# A well-formed public key, deliberately public and matching no private key
# anybody holds. The fingerprint must be computed from the blob rather than
# invented: a client comparing it against ssh-keygen -l -E md5 would catch it.
cat > "$WORK/key.pub" <<'KEY'
ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint
KEY
exoc compute ssh-key register conformance "$WORK/key.pub" >/dev/null \
  || fail "ssh-key register rejected"
keys="$(exoc -O json compute ssh-key list)" || fail "ssh-key list rejected: $keys"
printf '%s' "$keys" | jq -e 'any(.[]; .name == "conformance" and (.fingerprint | length) > 0)' >/dev/null \
  || fail "the registered key came back without a fingerprint: $keys"
exoc -Q compute ssh-key delete conformance --force >/dev/null || fail "ssh-key delete rejected"
prove_end "$span"
ok "registered with a computed fingerprint, and removed"

echo "- the limits a client reads, counted rather than invented"
# The CLI relabels what the API returns — "instance" becomes "Compute
# instances", usage becomes used — so the assertion is on what the client
# prints, which is the only thing a user sees.
limits="$(exoc -O json limits)" || fail "exo limits rejected: $limits"
printf '%s' "$limits" | jq -e 'any(.[]; .resource == "Compute instances")' >/dev/null \
  || fail "no instance quota in the limits: $limits"
used_before="$(printf '%s' "$limits" | jq -r '.[] | select(.resource == "Compute instances") | .used')"
if [ "$used_before" = "0" ]; then
  ok "limits served, no instance in use yet"
else
  fail "a fresh account already uses $used_before instance(s)"
fi

echo "- create, from the catalogue the emulator just published"
span="$(prove_begin behaviour)"
exoc compute instance create conformance \
  --zone "$EXOSCALE_ZONE" --template "$template_name" --instance-type "$type_name" \
  >/dev/null || fail "instance create rejected"
instances="$(exoc -O json compute instance list)" || fail "instance list rejected: $instances"
id="$(printf '%s' "$instances" | jq -r '.[] | select(.name == "conformance") | .id')"
[ -n "$id" ] || fail "the instance is not in the list after create: $instances"
state="$(printf '%s' "$instances" | jq -r '.[] | select(.name == "conformance") | .state')"
[ "$state" = "running" ] \
  || fail "the instance is $state, not running: with no runtime the control plane must still say running"
ok "instance $id, $state"

echo "- the lifecycle: stop, start, reboot (EXO-2's own evidence)"
# `exo compute instance stop` failed against the emulator for as long as the
# lifecycle was untriaged; this is the line issue #5 names as its evidence.
exoc compute instance stop "$id" --force >/dev/null || fail "instance stop rejected"
state="$(exoc -O json compute instance show "$id" | jq -r '.state')"
[ "$state" = "stopped" ] || fail "the instance is $state after stop, not stopped"
exoc compute instance start "$id" --force >/dev/null || fail "instance start rejected"
state="$(exoc -O json compute instance show "$id" | jq -r '.state')"
[ "$state" = "running" ] || fail "the instance is $state after start, not running"
exoc compute instance reboot "$id" --force >/dev/null || fail "instance reboot rejected"
ok "stopped, started, rebooted, and the state followed"

echo "- scale and resize-disk, on a stopped instance as upstream requires"
exoc compute instance stop "$id" --force >/dev/null || fail "second stop rejected"
exoc compute instance scale "$id" standard.small --force >/dev/null || fail "instance scale rejected"
exoc compute instance resize-disk "$id" 11 --force >/dev/null || fail "resize-disk rejected"
# The CLI renames and formats on output: the API's disk-size 11 prints as
# disk_size "11 GiB". The suite reads what the client prints, because that is
# what a user would copy.
shown="$(exoc -O json compute instance show "$id")" || fail "instance show rejected: $shown"
printf '%s' "$shown" | jq -e '.disk_size == "11 GiB"' >/dev/null \
  || fail "the disk did not grow: $shown"
exoc compute instance start "$id" --force >/dev/null || fail "start after scale rejected"
ok "scaled to standard.small, disk at 11 GiB"

echo "- protection: a protected instance refuses its delete"
exoc compute instance update "$id" --protection >/dev/null || fail "protection update rejected"
neg="$(prove_begin negative)"
if exoc -Q compute instance delete "$id" --force >/dev/null 2>&1; then
  fail "a protected instance accepted its delete"
fi
prove_end "$neg"
exoc compute instance update "$id" --protection=false >/dev/null || fail "protection removal rejected"
ok "delete refused while protected, protection removable"

echo "- security groups: the default one exists, rules round-trip"
sgs="$(exoc -O json compute security-group list)" || fail "security-group list rejected: $sgs"
printf '%s' "$sgs" | jq -e 'any(.[]; .name == "default")' >/dev/null \
  || fail "no default security group: a fresh real account holds one, measured"
exoc compute security-group create conformance-sg --description 'conformance' >/dev/null \
  || fail "security-group create rejected"
exoc compute security-group rule add conformance-sg --flow ingress --protocol tcp --port 22 \
  --network 203.0.113.0/24 >/dev/null || fail "rule add rejected"
# The CLI splits the API's flow-direction into ingress_rules and egress_rules
# on output; the assertion reads what the client prints.
rules="$(exoc -O json compute security-group show conformance-sg | jq '.ingress_rules')" \
  || fail "security-group show rejected"
printf '%s' "$rules" | jq -e 'length == 1 and .[0].network == "203.0.113.0/24"' >/dev/null \
  || fail "the rule did not come back: $rules"

# A second rule, removed again: the deletion path had never been driven, because
# every group this suite made was dropped whole and taking a group away takes its
# rules with it. Two rules rather than one is what makes the assertion mean
# something — a delete that removed the wrong rule, or all of them, would satisfy
# a check that only counted zero afterwards.
exoc compute security-group rule add conformance-sg --flow ingress --protocol tcp --port 8080 \
  --network 198.51.100.0/24 >/dev/null || fail "the second rule was rejected"
doomed="$(exoc -O json compute security-group show conformance-sg \
  | jq -r '.ingress_rules[] | select(.network == "198.51.100.0/24") | .id')"
[ -n "$doomed" ] || fail "the second rule is not readable, so nothing can be deleted"
exoc -Q compute security-group rule delete conformance-sg "$doomed" --force >/dev/null \
  || fail "rule delete rejected"
rules="$(exoc -O json compute security-group show conformance-sg | jq '.ingress_rules')" \
  || fail "security-group show rejected"
printf '%s' "$rules" | jq -e 'length == 1 and .[0].network == "203.0.113.0/24"' >/dev/null \
  || fail "the delete did not take exactly the rule it was given: $rules"
exoc compute instance security-group add "$id" conformance-sg >/dev/null || fail "sg attach rejected"
exoc compute instance security-group remove "$id" conformance-sg >/dev/null || fail "sg detach rejected"
ok "default present, rule round-trips, attach and detach pass"

echo "- anti-affinity groups: membership is computed"
exoc compute anti-affinity-group create conformance-aag --description 'conformance' >/dev/null \
  || fail "anti-affinity-group create rejected"
aag="$(exoc -O json compute anti-affinity-group show conformance-aag)" \
  || fail "anti-affinity-group show rejected"
printf '%s' "$aag" | jq -e '.name == "conformance-aag"' >/dev/null || fail "wrong group: $aag"
ok "created and readable"

echo "- elastic IPs: create, attach, the instance publishes it, detach, delete"
exoc -O json compute elastic-ip create --description 'conformance' >/dev/null \
  || fail "elastic-ip create rejected"
eip_ip="$(exoc -O json compute elastic-ip list | jq -r '.[0].ip_address // .[0].ip // empty')"
[ -n "$eip_ip" ] || fail "no address on the created elastic IP"
exoc compute instance elastic-ip attach "$id" "$eip_ip" >/dev/null || fail "eip attach rejected"
shown="$(exoc -O json compute instance show "$id")" || fail "show after attach rejected"
printf '%s' "$shown" | jq -e --arg ip "$eip_ip" '(.elastic_ips // [])[0] // "" | contains($ip)' >/dev/null \
  || fail "the instance does not publish its elastic IP: $shown"
exoc compute instance elastic-ip detach "$id" "$eip_ip" >/dev/null || fail "eip detach rejected"
exoc -Q compute elastic-ip delete "$eip_ip" --force >/dev/null || fail "eip delete rejected"
ok "$eip_ip attached, published, detached, deleted"

echo "- private networks: the range round-trips, an attach leases from it (EXO-3's own evidence)"
span_pn="$(prove_begin behaviour)"
exoc compute private-network create conformance-pn \
  --description 'conformance' --start-ip 10.90.0.20 --end-ip 10.90.0.200 --netmask 255.255.255.0 \
  >/dev/null || fail "private-network create rejected"
pn="$(exoc -O json compute private-network show conformance-pn)" || fail "private-network show rejected"
# The CLI relabels the API's kebab-case on output (start-ip prints as start_ip)
# and derives type=managed from the presence of the range; the assertion reads
# what the client prints, because that is what a user would copy.
printf '%s' "$pn" | jq -e '.start_ip == "10.90.0.20" and .end_ip == "10.90.0.200"
  and .netmask == "255.255.255.0" and .type == "managed"' >/dev/null \
  || fail "the declared range did not come back: $pn"
# The network's own edit path, which nothing had walked: this suite created
# networks, attached to them and deleted them, so the one call that changes a
# declared range had never been driven. The description is the field the CLI
# will send on its own, and the read-back is what separates an endpoint that
# stores from one that answers 200.
exoc compute private-network update conformance-pn --description 'conformance renamed' >/dev/null \
  || fail "private-network update rejected"
exoc -O json compute private-network show conformance-pn \
  | jq -e '.description == "conformance renamed"' >/dev/null \
  || fail "the private network update did not survive the write"

exoc compute instance private-network attach "$id" conformance-pn >/dev/null \
  || fail "private-network attach rejected"
pn="$(exoc -O json compute private-network show conformance-pn)" || fail "show after attach rejected"
# The CLI resolves the lease's instance-id to the instance *name* on output;
# the suite reads what the client prints, and the instance here is named
# "conformance".
lease="$(printf '%s' "$pn" | jq -r '.leases[]? | select(.instance == "conformance") | .ip_address')"
case "$lease" in
  10.90.0.*) ok "attached, lease $lease taken from the declared range" ;;
  *) fail "the lease is '$lease', outside the declared range: $pn" ;;
esac
shown="$(exoc -O json compute instance show "$id")" || fail "instance show after attach rejected"
printf '%s' "$shown" | jq -e '(.private_networks // []) | length == 1' >/dev/null \
  || fail "the instance does not publish its private network: $shown"

echo "- update-ip moves the lease, the network publishes the move"
exoc compute instance private-network update-ip "$id" conformance-pn --ip 10.90.0.99 >/dev/null \
  || fail "private-network update-ip rejected"
pn="$(exoc -O json compute private-network show conformance-pn)" || fail "show after update-ip rejected"
printf '%s' "$pn" | jq -e \
  'any(.leases[]?; .instance == "conformance" and .ip_address == "10.90.0.99")' >/dev/null \
  || fail "the lease did not move to 10.90.0.99: $pn"
ok "lease moved to 10.90.0.99"

echo "- a network an instance still sits on refuses its delete"
neg="$(prove_begin negative)"
if exoc -Q compute private-network delete conformance-pn --force >/dev/null 2>&1; then
  fail "an attached private network accepted its delete"
fi
# A static lease outside the declared range is refused too: same span, the two
# refusals this batch's control plane owes.
if exoc compute instance private-network update-ip "$id" conformance-pn --ip 192.0.2.7 >/dev/null 2>&1; then
  fail "a lease outside the declared range was accepted"
fi
prove_end "$neg"
ok "delete refused while attached, out-of-range lease refused"

echo "- detach takes the lease with it, and the delete then passes"
exoc compute instance private-network detach "$id" conformance-pn >/dev/null \
  || fail "private-network detach rejected"
pn="$(exoc -O json compute private-network show conformance-pn)" || fail "show after detach rejected"
printf '%s' "$pn" | jq -e '(.leases // []) | length == 0' >/dev/null \
  || fail "the lease survived its detach: $pn"
exoc -Q compute private-network delete conformance-pn --force >/dev/null \
  || fail "private-network delete rejected"
after_pn="$(exoc -O json compute private-network list)" || fail "private-network list rejected"
printf '%s' "$after_pn" | jq -e 'all(.[]; .name != "conformance-pn")' >/dev/null \
  || fail "the network survived its delete: $after_pn"
prove_end "$span_pn"
ok "detached, deleted, and gone"

# No span of its own: a source add and remove mutate an existing group, and a
# behaviour span demands a lifecycle — the emulator refused the bracket when
# this section claimed one, which is the assertion channel working as designed.
# The group's own create and delete already sit inside the suite's outer span.
echo "- external sources on a security group round-trip"
exoc compute security-group source add conformance-sg 203.0.113.0/24 >/dev/null \
  || fail "security-group source add rejected"
sg="$(exoc -O json compute security-group show conformance-sg)" || fail "sg show rejected"
printf '%s' "$sg" | jq -e '.external_sources == ["203.0.113.0/24"]' >/dev/null \
  || fail "the source did not come back: $sg"
# --force, because the remove prompts for confirmation and a suite has no tty.
exoc compute security-group source remove conformance-sg 203.0.113.0/24 --force >/dev/null \
  || fail "security-group source remove rejected"
sg="$(exoc -O json compute security-group show conformance-sg)" || fail "sg show after remove rejected"
printf '%s' "$sg" | jq -e '(.external_sources // []) | length == 0' >/dev/null \
  || fail "the source survived its removal: $sg"
ok "source added, published, removed"

# The reads and edits this pack served and no client had walked (#174), plus the
# snapshot and template surface #173 added. Each is one `exo` call; the reason
# they were missed is the same in every case — the suite drove creates and
# deletes and never the edit-and-read-back path.
echo "- the reads and edits a client makes after it has created something"
exoc compute instance update "$id" --label conformance=yes >/dev/null \
  || fail "instance update rejected"
exoc -O json compute instance show "$id" | jq -e '.labels.conformance == "yes"' >/dev/null \
  || fail "the label did not come back"

exoc -O json compute deploy-target list >/dev/null || fail "deploy-target list rejected"
exoc -O json compute security-group show conformance-sg >/dev/null || fail "security-group show rejected"

# The account reads #173 served, one of which the pack had refused to decline for
# months without ever answering it.
curl -sf "$ENDPOINT/v2/organization" >/dev/null || fail "the organization is not served"
curl -sf "$ENDPOINT/v2/event" >/dev/null || fail "events are not served"
ok "instance relabelled, deploy targets and group read"

echo "- an elastic IP is read back and edited"
eip="$(exoc -O json compute elastic-ip create --description conformance 2>&1)" \
  || fail "elastic-ip create rejected: $eip"
eip_ip="$(exoc -O json compute elastic-ip list | jq -r '.[0].ip_address // empty')"
[ -n "$eip_ip" ] || fail "no elastic IP in the list"
exoc -O json compute elastic-ip show "$eip_ip" >/dev/null || fail "elastic-ip show rejected"
exoc compute elastic-ip update "$eip_ip" --description conformance-2 >/dev/null \
  || fail "elastic-ip update rejected"
exoc compute elastic-ip delete "$eip_ip" --force >/dev/null || fail "elastic-ip delete rejected"
ok "read back, edited, released"

# Snapshots and the template a snapshot promotes into (#173). Driven by the CLI
# rather than by curl, because the point of the triage was that `exo compute
# instance snapshot create/list/revert` are first-class client paths, and a route
# no client walks is a route this project counts as unproven.
echo "- an instance is snapshotted, reverted to, and the snapshot removed"
exoc -Q compute instance stop "$id" --force >/dev/null || fail "instance stop rejected"
snap="$(exoc -O json compute instance snapshot create "$id" 2>&1)" \
  || fail "snapshot create rejected: $snap"
snaps="$(exoc -O json compute instance snapshot list)" || fail "snapshot list rejected: $snaps"
snap_id="$(printf '%s' "$snaps" | jq -r --arg i "$id" '.[] | select(.instance == $i or .instance_id == $i) | .id' | head -1)"
[ -z "$snap_id" ] && snap_id="$(printf '%s' "$snaps" | jq -r '.[0].id // empty')"
[ -n "$snap_id" ] || fail "the snapshot is not in the list: $snaps"

# Revert is the one lifecycle verb the served instance surface was missing, and
# the API refuses it on a running machine because the disk is replaced underneath.
exoc -Q compute instance snapshot revert "$id" "$snap_id" --force >/dev/null \
  || fail "revert rejected on a stopped instance"

exoc -Q compute instance snapshot delete "$snap_id" --force >/dev/null \
  || fail "snapshot delete rejected"
after_snap="$(exoc -O json compute instance snapshot list)" || fail "snapshot list rejected"
printf '%s' "$after_snap" | jq -e --arg s "$snap_id" 'all(.[]; .id != $s)' >/dev/null \
  || fail "the snapshot survived its delete: $after_snap"
# Two verbs the API only accepts on a stopped instance, which is why they sit
# here rather than anywhere else: a TPM is attached hardware, and a reset
# reinstalls the disk from the template. Both were mounted by #173's triage and
# neither had ever been driven, so their refusal on a running instance was the
# only thing anybody had seen.
exoc -Q compute instance enable-tpm "$id" --force >/dev/null \
  || fail "enable-tpm rejected on a stopped instance"
exoc -Q compute instance reset "$id" --force >/dev/null \
  || fail "reset rejected on a stopped instance"
# A reset keeps the instance and its identifier: an emulator that recreated it
# would answer every assertion above and hand the client a machine it never
# asked for.
exoc -O json compute instance show "$id" | jq -e --arg i "$id" '.id == $i' >/dev/null \
  || fail "the reset instance is not the one that was reset"

exoc -Q compute instance start "$id" --force >/dev/null || fail "instance start rejected"
ok "snapshotted, reverted, removed, TPM enabled and reset while stopped"

# A template of one's own, which is the other half of the template surface: the
# catalogue this emulator publishes is fixed, and registering one is what a
# client does when it builds its own image. Neither call had been driven — the
# suite only ever listed the catalogue.
echo "- a template is registered and removed"
span_tmpl="$(prove_begin behaviour)"
exoc -Q compute instance-template register conformance-tmpl \
  https://example.invalid/conformance.qcow2 \
  0000000000000000000000000000000000000000000000000000000000000000 \
  --description 'conformance' --disable-password --disable-ssh-key --username conformance \
  >/dev/null || fail "instance-template register rejected"
# Registered means listed: the CLI resolves a template by name against the
# catalogue, so a registration nothing lists is a registration no client can use.
exoc -O json compute instance-template list --family custom \
  | jq -e 'any(.[]; .name == "conformance-tmpl")' >/dev/null \
  || fail "the registered template is not in the custom list"
exoc -Q compute instance-template delete conformance-tmpl --force >/dev/null \
  || fail "instance-template delete rejected"
exoc -O json compute instance-template list --family custom \
  | jq -e 'all(.[]; .name != "conformance-tmpl")' >/dev/null \
  || fail "the template survived its delete"
prove_end "$span_tmpl"
ok "registered, listed, removed"

echo "- delete, and it is gone"
exoc -Q compute instance delete "$id" --force >/dev/null || fail "instance delete rejected"
after="$(exoc -O json compute instance list)" || fail "instance list rejected: $after"
printf '%s' "$after" | jq -e --arg id "$id" 'all(.[]; .id != $id)' >/dev/null \
  || fail "the instance survived its delete: $after"
ok "deleted, and gone"

echo "- the groups go too"
exoc -Q compute security-group delete conformance-sg --force >/dev/null \
  || fail "security-group delete rejected"
exoc -Q compute anti-affinity-group delete conformance-aag --force >/dev/null \
  || fail "anti-affinity-group delete rejected"
prove_end "$span"
ok "security group and anti-affinity group deleted"

# Every answer above was also checked against Exoscale's own API description,
# because the emulator validates itself when --contracts is set. A field they do
# not declare fails the request rather than reaching the client.
contracts="$(curl -sf "$ENDPOINT/_feint/conformance" || true)"
if printf '%s' "$contracts" | jq -e '.contracts | index("exoscale")' >/dev/null 2>&1; then
  ok "every response matched Exoscale's own API description"
fi


echo "conformance: exo CLI passed"
