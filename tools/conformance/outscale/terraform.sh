#!/usr/bin/env bash
# Conformance check: the real Outscale Terraform provider against the emulator.
#
# This is the pack's first Terraform evidence. Until it existed, everything
# Outscale claimed was proven by `oapi-cli` alone, and the provider walks paths
# no CLI does: it polls after a delete, it addresses a keypair by id while
# creating it by name, and it reads ProductCodes to decide whether a machine is
# Windows.
#
# Each of those was a defect here, and none was visible without this file:
#
#   - no ProductCodes on a Vm sent the provider to ReadAdminPassword, which was
#     not served, and `apply` died on the first machine;
#   - DeleteVms removed the record, so the provider's waiter read an empty list
#     and the plugin crashed outright — "Plugin did not respond", every destroy;
#   - the Subnet guard counted terminated Vms, so the destroy then failed on the
#     Subnet, naming the machine it was waiting for.
#
# The whole cycle runs: init, validate, plan, apply, second plan, destroy.
# `plan` alone would have caught none of the three.
#
# FEINT_TF_APPLY=0 stops at the plan. It exists for bisecting a provider
# problem, not for CI.
#
# Usage: tools/conformance/outscale/terraform.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"

# Never let a client reach anything but the local emulator. Without this, a
# missing endpoint does not fail: every official client falls back to the
# operator's stored credentials, and a test creates billable resources on a real
# account. That is not hypothetical — it happened, to this repository.
# shellcheck source=/dev/null
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/guard.sh"
guard_local "$ENDPOINT"
# The assertion span behind the behaviour evidence axis: the whole
# apply-to-destroy cycle is bracketed, and the emulator only accepts the
# bracket if its own store saw resources created and then destroyed inside it.
# shellcheck source=/dev/null
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/prove.sh"

TF="${FEINT_TF:-}"
if [ -z "$TF" ]; then
  if command -v tofu >/dev/null 2>&1; then TF=tofu; else TF=terraform; fi
fi
command -v "$TF" >/dev/null 2>&1 || { echo "FAIL: neither tofu nor terraform is installed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }

DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/terraform" && pwd)"
WORK="$(mktemp -d)"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

# Fail now rather than in six minutes.
#
# The Outscale provider retries with backoff when its endpoint does not answer,
# so an emulator that is not running turns into a `terraform apply` that hangs
# and then reports a timeout — a symptom that reads like a slow emulator and is
# in fact an absent one. Measured: the same mistake cost six minutes twice, once
# from a stopped emulator and once from an endpoint missing its /api/v1 path.
#
# One request, one second, and the message says which of the two it is.
if ! curl -sf -m 2 -o /dev/null "$ENDPOINT/_feint/health"; then
  fail "nothing answers at $ENDPOINT: start the emulator (feint start --addr ${ENDPOINT#http://}) before running this suite"
fi


# An apply that fails leaves resources behind, so the destroy happens on the
# error path too. Without this the first broken run poisons every later one: an
# IpRange that overlaps a leftover Net fails the next apply for a reason that
# says nothing about the change under test.
DESTROYED=0
cleanup() {
  local status=$?
  if [ "$DESTROYED" = "0" ] && [ -f "$WORK/terraform.tfstate" ]; then
    echo "- destroy (cleaning up after a failed run)"
    (cd "$WORK" && "$TF" destroy -no-color -auto-approve -var "endpoint=$ENDPOINT") \
      || echo "FAIL: could not destroy; resources may be left behind" >&2
  fi
  rm -rf "$WORK"
  exit "$status"
}
trap cleanup EXIT

cp "$DIR"/*.tf "$WORK/"
cd "$WORK"

export TF_IN_AUTOMATION=1
export TF_INPUT=0

echo "conformance: outscale $TF against $ENDPOINT"

"$TF" init -no-color -upgrade >/dev/null
"$TF" validate -no-color >/dev/null
"$TF" plan -no-color -var "endpoint=$ENDPOINT" -out tfplan >/dev/null

if [ "${FEINT_TF_APPLY:-1}" != "1" ]; then
  echo "conformance: outscale plan passed, apply skipped by FEINT_TF_APPLY=0"
  exit 0
fi

echo "- apply"
span="$(prove_begin behaviour)"
"$TF" apply -no-color -auto-approve tfplan >/dev/null
vm_id="$("$TF" output -raw vm_id)"
[ -n "$vm_id" ] || fail "apply produced no Vm id"
ok "Vm $vm_id"

# The provider is satisfied, which is what the apply proves. This asks the
# emulator directly, so a state file that agrees with itself cannot pass for a
# resource that exists.
echo "- the applied Vm exists in the API, with the tag it was given"
vms="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadVms" -H 'Content-Type: application/json' \
        -d "{\"Filters\":{\"VmIds\":[\"$vm_id\"]}}")" || fail "ReadVms rejected"
printf '%s' "$vms" | jq -e '.Vms | length == 1' >/dev/null \
  || fail "the Vm Terraform created is not served: $vms"
printf '%s' "$vms" | jq -e 'any(.Vms[0].Tags[]?; .Key == "name" and .Value == "feint-conformance")' >/dev/null \
  || fail "the tag the provider created is not on the Vm: $vms"
ok "served, and carrying its tag"

# The routing family, which the apply proves the provider accepted and this
# proves the emulator actually holds. Each of these was a 400 on a filter the
# provider sends and the pack had not declared — found here, by this fixture,
# after every one of them passed its unit test and the contract.
echo "- the routing the provider built is served, and answers its own filters"
igw_id="$("$TF" output -raw internet_service_id)"
rtb_id="$("$TF" output -raw route_table_id)"
sg_id="$("$TF" output -raw security_group_id)"
public_ip="$("$TF" output -raw public_ip)"

# By destination, which is how the provider reads a route back. This filter
# failing is what killed the apply at resource eleven of thirteen.
routes="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadRouteTables" -H 'Content-Type: application/json' \
           -d '{"Filters":{"RouteDestinationIpRanges":["0.0.0.0/0"]}}')" || fail "ReadRouteTables rejected"
printf '%s' "$routes" | jq -e --arg r "$rtb_id" --arg g "$igw_id" \
  'any(.RouteTables[]; .RouteTableId == $r and any(.Routes[]; .DestinationIpRange == "0.0.0.0/0" and .GatewayId == $g))' >/dev/null \
  || fail "the default route through the gateway is not served: $routes"

# The link, by the subnet it names.
printf '%s' "$routes" | jq -e --arg r "$rtb_id" \
  'any(.RouteTables[]; .RouteTableId == $r and any(.LinkRouteTables[]; .SubnetId != null))' >/dev/null \
  || fail "the route table link to the subnet is not served: $routes"

# The rule the provider added, on the group it named.
groups="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadSecurityGroups" -H 'Content-Type: application/json' \
           -d "{\"Filters\":{\"SecurityGroupIds\":[\"$sg_id\"]}}")" || fail "ReadSecurityGroups rejected"
printf '%s' "$groups" | jq -e \
  'any(.SecurityGroups[0].InboundRules[]; .IpProtocol == "tcp" and .FromPortRange == 22 and (.IpRanges | index("198.51.100.0/24")))' >/dev/null \
  || fail "the inbound rule the provider created is not served: $groups"

# The machine wears the group it was created with, resolved to {id, name}.
printf '%s' "$vms" | jq -e --arg s "$sg_id" \
  'any(.Vms[0].SecurityGroups[]; .SecurityGroupId == $s)' >/dev/null \
  || fail "the Vm does not wear the security group it was created with: $vms"

# The address link, read back by the link id the way the provider does.
addresses="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadPublicIps" -H 'Content-Type: application/json' \
              -d "{\"Filters\":{\"PublicIps\":[\"$public_ip\"]}}")" || fail "ReadPublicIps rejected"
printf '%s' "$addresses" | jq -e --arg v "$vm_id" \
  '.PublicIps[0].VmId == $v and (.PublicIps[0].LinkPublicIpId | startswith("eipassoc-"))' >/dev/null \
  || fail "the public IP is not linked to the Vm: $addresses"
ok "route, link, rule, group and address all served as built"

# The tags the provider set on the internet service and the route table, which
# it applies through CreateTags with the identifier it has just been handed. The
# apply above already fails without this working — that is issue #99, where the
# emulator answered "the resource igw-… does not exist" about a resource it was
# serving — so this reads them back to prove the tag was stored rather than
# merely accepted.
echo "- the tags the provider set on families beyond the first four"
igw_id="$("$TF" output -raw internet_service_id)"
services="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadInternetServices" -H 'Content-Type: application/json' \
             -d "{\"Filters\":{\"InternetServiceIds\":[\"$igw_id\"]}}")" \
  || fail "ReadInternetServices rejected"
printf '%s' "$services" | jq -e \
  'any(.InternetServices[0].Tags[]; .Key == "Name" and .Value == "conformance-igw")' >/dev/null \
  || fail "the tag the provider set on the internet service is not served: $services"

printf '%s' "$routes" | jq -e \
  'any(.RouteTables[0].Tags[]; .Key == "Name" and .Value == "conformance-rtb")' >/dev/null \
  || fail "the tag the provider set on the route table is not served: $routes"

# And the flat view names them with the types Outscale's own SDK declares, not
# with names of our own: "vpc" rather than "net", "instance" rather than "vm".
tags="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadTags" -H 'Content-Type: application/json' \
         -d '{"Filters":{"ResourceTypes":["route-table"]}}')" || fail "ReadTags rejected"
printf '%s' "$tags" | jq -e --arg r "$rtb_id" \
  'any(.Tags[]; .ResourceId == $r and .ResourceType == "route-table")' >/dev/null \
  || fail "ReadTags does not name the route table with the SDK's own type: $tags"
ok "tags set, stored, and named with the types the SDK declares"

echo "- the volume the provider created is served"
volume_id="$("$TF" output -raw volume_id)"
[ -n "$volume_id" ] || fail "no volume id in the outputs"
volumes="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadVolumes" -H 'Content-Type: application/json' \
            -d "{\"Filters\":{\"VolumeIds\":[\"$volume_id\"]}}")" || fail "ReadVolumes rejected"
printf '%s' "$volumes" | jq -e '.Volumes | length == 1' >/dev/null \
  || fail "the volume Terraform created is not served: $volumes"
ok "volume $volume_id"

# The storage chain of OSC-4: the volume attached to the machine, the snapshot
# taken from it, the image cut from the snapshot. The provider polls
# ReadVolumes filtered on LinkVolumeVmIds to wait for the attach — a filter
# this pack refused until the fixture asked for it, which failed the apply and
# then the destroy.
echo "- the storage chain is served, link included"
snapshot_id="$("$TF" output -raw snapshot_id)"
image_id="$("$TF" output -raw image_id)"

linked="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadVolumes" -H 'Content-Type: application/json' \
           -d "{\"Filters\":{\"LinkVolumeVmIds\":[\"$vm_id\"]}}")" || fail "ReadVolumes rejected"
printf '%s' "$linked" | jq -e --arg v "$vm_id" \
  'any(.Volumes[]; any(.LinkedVolumes[]; .VmId == $v and .State == "attached"))' >/dev/null \
  || fail "the volume link the provider made is not served: $linked"

snapshots="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadSnapshots" -H 'Content-Type: application/json' \
              -d "{\"Filters\":{\"SnapshotIds\":[\"$snapshot_id\"]}}")" || fail "ReadSnapshots rejected"
printf '%s' "$snapshots" | jq -e '.Snapshots[0].State == "completed" and .Snapshots[0].Progress == 100' >/dev/null \
  || fail "the snapshot is not readable as completed: $snapshots"

# The image must be listed beside the catalogue, or a client cannot read back
# what it just registered — the shape of bug that plans a change for ever.
image_list="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadImages" -H 'Content-Type: application/json' \
               -d "{\"Filters\":{\"ImageIds\":[\"$image_id\"]}}")" || fail "ReadImages rejected"
printf '%s' "$image_list" | jq -e --arg s "$snapshot_id" \
  '.Images | length == 1 and (.[0].BlockDeviceMappings[0].Bsu.SnapshotId == $s)' >/dev/null \
  || fail "the registered image does not name the snapshot it was cut from: $image_list"
# And the catalogue is still there beside it.
all_images="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadImages" -H 'Content-Type: application/json' -d '{}')" \
  || fail "ReadImages rejected"
printf '%s' "$all_images" | jq -e '[.Images[] | select(.ImageId == "ami-00000001")] | length == 1' >/dev/null \
  || fail "the fixed catalogue vanished once an image was registered: $all_images"
ok "volume linked, snapshot completed, image cut from it and listed"

# Terraform plans an empty diff against a state it just wrote only if every
# attribute the provider reads back matches what it sent. An invented field, a
# dropped one, or a tag order that moves shows up here.
echo "- a second plan is empty"
plan_status=0
"$TF" plan -no-color -detailed-exitcode -var "endpoint=$ENDPOINT" >/dev/null 2>&1 || plan_status=$?
case "$plan_status" in
  0) ok "no drift between what was sent and what is served" ;;
  2) "$TF" plan -no-color -var "endpoint=$ENDPOINT" || true
     fail "the emulator does not read back what the provider sent: the applied state still plans a change" ;;
  *) fail "the second plan errored with status $plan_status" ;;
esac

echo "- destroy"
"$TF" destroy -no-color -auto-approve -var "endpoint=$ENDPOINT" >/dev/null
DESTROYED=1

# Destroy reporting success is not the same as the resources being gone: the
# provider believes whatever the delete answered. Ask the emulator.
#
# A terminated Vm stays readable on purpose — the provider polls for that state
# — so what must be gone is the Net, and the Vm must report terminated rather
# than still running.
nets="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadNets" -H 'Content-Type: application/json' -d '{}')" \
  || fail "ReadNets rejected"
printf '%s' "$nets" | jq -e '.Nets | length == 0' >/dev/null \
  || fail "the destroyed Net still answers: $nets"

vms="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadVms" -H 'Content-Type: application/json' \
        -d "{\"Filters\":{\"VmIds\":[\"$vm_id\"]}}")" || fail "ReadVms rejected"
printf '%s' "$vms" | jq -e '.Vms[0].State == "terminated" or (.Vms | length == 0)' >/dev/null \
  || fail "the destroyed Vm is not terminated: $vms"
prove_end "$span"
ok "destroyed, and the API agrees"

echo "conformance: outscale $TF apply/destroy passed"
