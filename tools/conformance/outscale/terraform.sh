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

echo "- the volume the provider created is served"
volume_id="$("$TF" output -raw volume_id)"
[ -n "$volume_id" ] || fail "no volume id in the outputs"
volumes="$(curl -sf -X POST "$ENDPOINT/api/v1/ReadVolumes" -H 'Content-Type: application/json' \
            -d "{\"Filters\":{\"VolumeIds\":[\"$volume_id\"]}}")" || fail "ReadVolumes rejected"
printf '%s' "$volumes" | jq -e '.Volumes | length == 1' >/dev/null \
  || fail "the volume Terraform created is not served: $volumes"
ok "volume $volume_id"

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
ok "destroyed, and the API agrees"

echo "conformance: outscale $TF apply/destroy passed"
