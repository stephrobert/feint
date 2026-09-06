#!/usr/bin/env bash
# The declared catalogue, driven by the real clients (#126).
#
# An emulator of its own, started with --strict-catalog on a declaration that
# allows one image and one machine type per provider (strict-catalog.json), and
# the three official clients asked to create outside it and inside it. What
# this proves is the issue's own bar: on a typo, `scw`, `octl` and `exo` each
# render their provider's ordinary not-found path, and a declared identifier
# still creates. The shared run never starts with the flag, so nothing here can
# change what every other suite measures: the compatibility mode is theirs.
#
# Its own port, for the reason faults.sh has one: this suite's subject is a
# mode of the emulator, and it would otherwise restart the one every other
# line of the pass is measuring.
#
# Usage: tools/conformance/strict.sh
set -u

PORT="${FEINT_STRICT_PORT:-4595}"
ADDR="127.0.0.1:$PORT"
ENDPOINT="http://$ADDR"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Never let a client reach anything but the local emulator. See guard.sh for the
# incident that wrote this rule.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/guard.sh"
guard_local "$ENDPOINT"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

for tool in jq scw octl exo; do
  command -v "$tool" >/dev/null 2>&1 || fail "$tool is not installed"
done
FEINT_BIN="${FEINT_BIN:-$REPO_DIR/feint}"
[ -x "$FEINT_BIN" ] || fail "no feint binary at $FEINT_BIN (build it: mise run build)"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"; "$FEINT_BIN" stop --addr "$ADDR" >/dev/null 2>&1 || true' EXIT

# refused runs a client that must fail, and requires it to say what a user
# would read: a client that failed for a reason of its own (a bad flag, a
# missing config) is not the refusal this suite is about.
refused() { # label needle command...
  local label="$1" needle="$2"
  shift 2
  local out rc=0
  out="$("$@" 2>&1)" || rc=$?
  [ "$rc" -ne 0 ] || fail "$label: the client was answered success where a refusal was demanded: $out"
  printf '%s' "$out" | grep -qiE -- "$needle" || fail "$label: the client failed without naming it ($needle): $out"
}

echo "conformance: the declared catalogue against $ENDPOINT"

echo "- an emulator of its own, started on the declaration"
"$FEINT_BIN" start --addr "$ADDR" --strict-catalog "$SCRIPT_DIR/strict-catalog.json" --timeout 60s >/dev/null \
  || fail "feint start --strict-catalog refused the declaration"
ok "started with strict-catalog.json"

# ---------------------------------------------------------------- Scaleway ---
set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/scaleway/fake-credentials.env"
# shellcheck disable=SC2034 # read by scw from the environment, not here
SCW_API_URL="$ENDPOINT"
set +a
guard_no_real_profile SCW_API_URL scw

echo "- scw: a typo in the image label meets the CLI's ordinary not-found path"
# Measured on scw 2.56.3: the SDK's ResourceNotFoundError rendered as
# "cannot find resource 'image' with ID 'ubuntu_typo'", resource and
# resource_id beside it.
refused "scw on an undeclared image" "cannot find resource 'image'" \
  scw instance server create name=strict-typo type=DEV1-S image=ubuntu_typo zone=fr-par-1 -o json
refused "scw on an undeclared type" "commercial_type" \
  scw instance server create name=strict-typo type=PLAY2-PICO image=debian_bookworm zone=fr-par-1 -o json
ok "refused by name on the image and on the type"

echo "- scw: the declared image and type still create"
created="$(scw instance server create name=strict-declared type=DEV1-S image=debian_bookworm zone=fr-par-1 -o json 2>&1)" \
  || fail "scw refused the declared image and type: $created"
scw_id="$(printf '%s' "$created" | jq -r '.id // empty')"
[ -n "$scw_id" ] || fail "no server id in the answer: $created"
# Stopped first: a create boots the server, and a delete on a running one is
# refused with transient_state, the refusal Terraform depends on.
scw instance server stop "$scw_id" zone=fr-par-1 >/dev/null || fail "poweroff rejected"
scw instance server delete "$scw_id" zone=fr-par-1 >/dev/null || fail "delete rejected"
ok "created, stopped and deleted"

# ---------------------------------------------------------------- Outscale ---
set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/outscale/fake-credentials.env"
# shellcheck disable=SC2034 # read by the guard below and by octl through config.json
OSC_ENDPOINT_API="$ENDPOINT/api/v1"
set +a
guard_no_real_profile OSC_ENDPOINT_API octl

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
osc() { octl --config "$WORK/config.json" --no-upgrade -o raw iaas api "$@" </dev/null; }

echo "- octl: an ImageId that names nothing is the recorded 400, code 5023"
refused "octl on an undeclared image" "5023" osc CreateVms --ImageId ami-1234567a --VmType tinav6.c1r1p2
refused "octl on an undeclared type" "declared catalogue" osc CreateVms --ImageId ami-00000001 --VmType tinav6.c2r4p2
ok "refused with the recorded code on the image, and on the type"

echo "- octl: the catalogue a client reads is the one the create accepts"
images="$(osc ReadImages)" || fail "ReadImages rejected: $images"
printf '%s' "$images" | jq -e '[.Images[].ImageId] == ["ami-00000001"]' >/dev/null \
  || fail "ReadImages lists more than the declared image: $images"
types="$(osc ReadVmTypes)" || fail "ReadVmTypes rejected: $types"
printf '%s' "$types" | jq -e '[.VmTypes[].VmTypeName] == ["tinav6.c1r1p2"]' >/dev/null \
  || fail "ReadVmTypes lists more than the declared type: $types"
ok "one image and one type on offer, the declared ones"

echo "- octl: the declared image and type still create"
vm="$(osc CreateVms --ImageId ami-00000001 --VmType tinav6.c1r1p2)" || fail "CreateVms refused the declared pair: $vm"
vm_id="$(printf '%s' "$vm" | jq -r '.Vms[0].VmId // empty')"
[ -n "$vm_id" ] || fail "no VmId in the answer: $vm"
osc DeleteVms --VmIds "$vm_id" >/dev/null || fail "DeleteVms rejected"
ok "created and deleted"

# ---------------------------------------------------------------- Exoscale ---
set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/exoscale/fake-credentials.env"
set +a
export EXOSCALE_API_ENDPOINT="$ENDPOINT/v2"
exoc() { exo "$@"; }

echo "- exo: the lists a create walks first offer the declared entries only"
templates="$(exoc -O json compute instance-template list)" || fail "template list rejected: $templates"
printf '%s' "$templates" | jq -e 'length == 1 and .[0].name == "Linux Ubuntu 24.04 LTS 64-bit"' >/dev/null \
  || fail "the template list is not the declared one: $templates"
template_name="$(printf '%s' "$templates" | jq -r '.[0].name')"
types="$(exoc -O json compute instance-type list)" || fail "instance-type list rejected: $types"
printf '%s' "$types" | jq -e 'length == 1' >/dev/null || fail "the instance-type list is not the declared one: $types"
type_name="$(printf '%s' "$types" | jq -r '"\(.[0].family).\(.[0].name)"')"
ok "one template and one type on offer"

echo "- exo: a template outside the declaration meets the CLI's ordinary not-found path"
refused "exo on an undeclared template" "found|template" \
  exoc compute instance create strict-typo --zone "$EXOSCALE_ZONE" --template "Linux Debian 12 64-bit" --instance-type "$type_name"
ok "refused by name"

echo "- exo: the declared template and type still create"
exoc compute instance create strict-declared \
  --zone "$EXOSCALE_ZONE" --template "$template_name" --instance-type "$type_name" >/dev/null \
  || fail "exo refused the declared template and type"
instances="$(exoc -O json compute instance list)" || fail "instance list rejected: $instances"
exo_id="$(printf '%s' "$instances" | jq -r '.[] | select(.name == "strict-declared") | .id')"
[ -n "$exo_id" ] || fail "the instance is not in the list: $instances"
exoc -Q compute instance delete "$exo_id" --force >/dev/null || fail "instance delete rejected"
ok "created and deleted"

echo "conformance: the declared catalogue passed"
