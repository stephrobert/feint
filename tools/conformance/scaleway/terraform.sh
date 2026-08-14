#!/usr/bin/env bash
# Conformance check: the real Terraform provider against the emulator.
#
# The whole cycle runs: init, validate, plan, apply, destroy. `plan` alone proves only that the
# provider accepts api_url and can read; every defect that has cost this project time was invisible
# until `apply` — a capacity computed as len(volumes)-1, a nil image the provider dereferenced, a
# root volume the CLI waited for and never found. A suite that stops at `plan` would have declared
# all three fine.
#
# FEINT_TF_APPLY=0 stops at the plan. It exists for bisecting a provider problem, not for CI.
#
# The binary is OpenTofu when it is installed, Terraform otherwise; FEINT_TF forces either. The
# fixture uses no provider-specific syntax, so whichever runs it exercises the same emulated API,
# and pinning the tool here would only make the suite fail on a machine that has the other one.
#
# Usage: tools/conformance/scaleway/terraform.sh [endpoint]
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
ZONE="${ZONE:-fr-par-1}"
TF="${FEINT_TF:-}"
if [ -z "$TF" ]; then
  if command -v tofu >/dev/null 2>&1; then TF=tofu; else TF=terraform; fi
fi
command -v "$TF" >/dev/null 2>&1 || { echo "FAIL: neither tofu nor terraform is installed" >&2; exit 1; }
DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")/terraform" && pwd)"
WORK="$(mktemp -d)"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

# An apply that fails leaves machines running on the station, so the destroy has to happen on the
# error path too and not only on the happy one. Without this the first broken run poisons every
# later one, and the operator is left sweeping by hand.
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

echo "conformance: $TF against $ENDPOINT"

"$TF" init -no-color -upgrade
"$TF" validate -no-color
"$TF" plan -no-color -var "endpoint=$ENDPOINT" -out tfplan

if [ "${FEINT_TF_APPLY:-1}" != "1" ]; then
  echo "conformance: plan passed, apply skipped by FEINT_TF_APPLY=0"
  exit 0
fi

echo "- apply"
span="$(prove_begin behaviour)"
"$TF" apply -no-color -auto-approve tfplan
server_id="$("$TF" output -raw server_id)"
[ -n "$server_id" ] || fail "apply produced no server id"
ok "server $server_id"

# The provider qualifies every id with its locality, "fr-par-1/<uuid>", because one configuration
# can span zones. The API path carries the zone separately, so only the uuid goes in the URL.
server_uuid="${server_id##*/}"

# The provider is satisfied, which is the point of the apply. This asks the emulator directly, so a
# state file that agrees with itself cannot pass for a resource that exists.
echo "- the applied server exists in the API"
code="$(curl -s -o /dev/null -w '%{http_code}' "$ENDPOINT/instance/v1/zones/$ZONE/servers/$server_uuid")"
[ "$code" = "200" ] || fail "the server Terraform created answers $code, not 200"
ok "served at /instance/v1/zones/$ZONE/servers/$server_uuid"

# The address the fixture booked answers through IPAM and names the NIC that
# carries it. This is the SW-4 chain: BookIP, then CreatePrivateNIC with
# ipam_ip_ids, then the provider reading the address back through ListIPs. The
# assertion asks the emulator, not the state file.
echo "- the booked address is attached to the NIC, and says so"
ipam_id="$("$TF" output -raw ipam_ip_id)"
ipam_uuid="${ipam_id##*/}"
ipam_body="$(curl -s "$ENDPOINT/ipam/v1/regions/fr-par/ips/$ipam_uuid")"
printf '%s' "$ipam_body" | jq -e '.address == "10.182.0.10/24"' >/dev/null \
  || fail "the booked address did not survive the apply: $ipam_body"
printf '%s' "$ipam_body" | jq -e '.resource.type == "instance_private_nic" and (.resource.id | length) > 0' >/dev/null \
  || fail "the booked address does not name the NIC carrying it: $ipam_body"
ok "10.182.0.10 booked and carried by a NIC"

route_id="$("$TF" output -raw route_id)"
route_uuid="${route_id##*/}"
code="$(curl -s -o /dev/null -w '%{http_code}' "$ENDPOINT/vpc/v2/regions/fr-par/routes/$route_uuid")"
[ "$code" = "200" ] || fail "the route Terraform created answers $code, not 200"
ok "route served at /vpc/v2/regions/fr-par/routes/$route_uuid"

# Terraform plans an empty diff against a state it just wrote only if every attribute the provider
# reads back matches what it sent. This is where an invented field or a dropped one shows up, and
# it is cheaper to catch here than as a permanent diff in somebody's real configuration.
echo "- a second plan is empty"
plan_status=0
"$TF" plan -no-color -detailed-exitcode -var "endpoint=$ENDPOINT" >/dev/null 2>&1 || plan_status=$?
case "$plan_status" in
  0) ok "no drift between what was sent and what is served" ;;
  2) "$TF" plan -no-color -var "endpoint=$ENDPOINT" || true
     fail "the emulator does not read back what the provider sent: the applied state still plans a change" ;;
  *) fail "the second plan errored with status $plan_status" ;;
esac

# Zoned, like every id the provider returns: fr-par-1/<uuid>. The server id
# above is trimmed the same way.
volume_id="$("$TF" output -raw volume_id)"
volume_uuid="${volume_id##*/}"
[ -n "$volume_uuid" ] || fail "no volume id in the outputs"

echo "- destroy"
"$TF" destroy -no-color -auto-approve -var "endpoint=$ENDPOINT"
DESTROYED=1

# Destroy reporting success is not the same as the resource being gone: the provider believes
# whatever the delete answered. Ask the emulator. Each of these is a demanded
# refusal, so the block is a negative span: the emulator must have really said no.
neg="$(prove_begin negative)"
code="$(curl -s -o /dev/null -w '%{http_code}' "$ENDPOINT/instance/v1/zones/$ZONE/servers/$server_uuid")"
[ "$code" = "404" ] || fail "the destroyed server still answers $code"
# And the volume with it. The provider destroys through terminate, which used to
# leave every additional volume naming a server that answered 404: `destroy`
# then failed with "volume is still attached to a server" on every retry, and no
# fixture here carried a volume to show it.
code="$(curl -s -o /dev/null -w '%{http_code}' "$ENDPOINT/instance/v1/zones/$ZONE/volumes/$volume_uuid")"
[ "$code" = "404" ] || fail "the destroyed volume still answers $code"
# The booked address and the route go with their resources. The destroy order
# is the part worth proving: the NIC detaches the booked address first, then
# ReleaseIP frees it — an emulator that deleted it with the NIC would have made
# the destroy fail right here.
code="$(curl -s -o /dev/null -w '%{http_code}' "$ENDPOINT/ipam/v1/regions/fr-par/ips/$ipam_uuid")"
[ "$code" = "404" ] || fail "the released address still answers $code"
code="$(curl -s -o /dev/null -w '%{http_code}' "$ENDPOINT/vpc/v2/regions/fr-par/routes/$route_uuid")"
[ "$code" = "404" ] || fail "the destroyed route still answers $code"
prove_end "$neg"
prove_end "$span"
ok "destroyed, and gone from the API"

echo "conformance: $TF apply/destroy passed"
