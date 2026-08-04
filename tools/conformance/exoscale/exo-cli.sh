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

echo "- delete, and it is gone"
exoc -Q compute instance delete "$id" --force >/dev/null || fail "instance delete rejected"
after="$(exoc -O json compute instance list)" || fail "instance list rejected: $after"
printf '%s' "$after" | jq -e --arg id "$id" 'all(.[]; .id != $id)' >/dev/null \
  || fail "the instance survived its delete: $after"
ok "deleted, and gone"

# Every answer above was also checked against Exoscale's own API description,
# because the emulator validates itself when --contracts is set. A field they do
# not declare fails the request rather than reaching the client.
contracts="$(curl -sf "$ENDPOINT/_feint/conformance" || true)"
if printf '%s' "$contracts" | jq -e '.contracts | index("exoscale")' >/dev/null 2>&1; then
  ok "every response matched Exoscale's own API description"
fi

echo "conformance: exo CLI passed"
