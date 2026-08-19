#!/usr/bin/env bash
# Conformance check: the Exoscale zone is a datum, proven with the real CLI (#278).
#
# The emulator serves one zone per process — measured: the CLI queries every
# zone it is told about and merges the answers, so one emulator publishing
# eight zones listed one instance eight times. What #278 changed is *which*
# zone: a constant became a datum, selected by FEINT_EXOSCALE_ZONE. This suite
# is the second half of the two-zone proof: exo-cli.sh drives the default
# deployment (ch-dk-2) and this one starts its own emulator on ch-gva-2 — the
# zone the eu-data-platform stack hardcodes and openshift4-exoscale's DNS
# client resolves (#262) — the way a real client meets a second zone: another
# endpoint.
#
# It owns its emulator (a zone is fixed at construction, so the shared one
# cannot be re-pointed mid-run) and therefore starts and stops it with the
# lifecycle verbs, on its own port.
#
# Usage: tools/conformance/exoscale/zones.sh   (port: FEINT_ZONES_PORT, default 4662)
set -euo pipefail

PORT="${FEINT_ZONES_PORT:-4662}"
ADDR="127.0.0.1:$PORT"
ENDPOINT="http://$ADDR"
ZONE="ch-gva-2"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
REPO_DIR="$(cd "$SCRIPT_DIR/../../.." && pwd)"

# Never let a client reach anything but the local emulator. See guard.sh for
# the incident that wrote this rule.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../guard.sh"
guard_local "$ENDPOINT"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

command -v exo >/dev/null 2>&1 || fail "exo is not installed"
command -v jq >/dev/null 2>&1 || fail "jq is not installed"

FEINT_BIN="${FEINT_BIN:-$REPO_DIR/feint}"
[ -x "$FEINT_BIN" ] || fail "no feint binary at $FEINT_BIN (build it: mise run build)"

set -a
# shellcheck source=/dev/null
. "$SCRIPT_DIR/fake-credentials.env"
set +a
export EXOSCALE_API_ENDPOINT=${ENDPOINT}/v2
export EXOSCALE_ZONE="$ZONE"

echo "conformance: exo CLI against $ENDPOINT, zone $ZONE"

echo "- an emulator of its own, because the zone is fixed at construction"
FEINT_EXOSCALE_ZONE="$ZONE" "$FEINT_BIN" start --addr "$ADDR" \
  --contracts "$REPO_DIR/contracts" --timeout 60s
trap '"$FEINT_BIN" stop --addr "$ADDR" >/dev/null 2>&1 || true' EXIT

# The spans of prove.sh, against this suite's own emulator.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/../prove.sh"

echo "- the zone list names the zone in force, and only it"
zones="$(exo -O json zone)" || fail "zone rejected: $zones"
printf '%s' "$zones" | jq -e 'length == 1' >/dev/null \
  || fail "more than one zone points at this emulator: every resource would be listed once per zone"
printf '%s' "$zones" | jq -e "any(.[]; .name == \"$ZONE\")" >/dev/null \
  || fail "the selected zone $ZONE is missing from: $zones"
# The default must be gone: a list still naming ch-dk-2 would mean the
# selection changed nothing, which is the constant this suite exists to refuse.
printf '%s' "$zones" | jq -e 'any(.[]; .name == "ch-dk-2") | not' >/dev/null \
  || fail "ch-dk-2 is still on offer in a $ZONE deployment: the zone is a constant again"
ok "one zone, $ZONE, selected by FEINT_EXOSCALE_ZONE"

echo "- the catalogue a create walks, in the zone in force"
templates="$(exo -O json compute instance-template list --zone "$ZONE")" || fail "template list rejected: $templates"
template_name="$(printf '%s' "$templates" | jq -r '.[0].name // empty')"
[ -n "$template_name" ] || fail "no template on offer: $templates"
# `exo compute instance-type list` is the one command of this flow that CANNOT
# work off ch-dk-2, and that is the client, measured at the source: exo 1.95.1
# hardcodes its compiled default for the v3 zone switch
# (cmd/compute/instance_type/instance_type_list.go:85 passes
# exocmd.DefaultZone, which cmd/common.go:15 fixes at "ch-dk-2"; no flag,
# config or env reaches it — instance_type_show.go:76 is the same). Invisible
# against the real cloud, whose zone list always names ch-dk-2; fatal against
# any one-zone deployment that is not ch-dk-2, docs/limits.md records it. So
# the suite demands exactly that failure, naming exactly that zone — and lets
# a future exo that fixes it pass, so the day it heals is the day this says so.
if types="$(exo -O json compute instance-type list 2>&1)"; then
  type_name="$(printf '%s' "$types" | jq -r '"\(.[0].family).\(.[0].name)"')"
else
  printf '%s' "$types" | grep -q 'ch-dk-2' \
    || fail "instance-type list failed for a new reason (not its hardcoded ch-dk-2): $types"
  # The type name comes from the deployment's own catalogue instead, the same
  # rows the CLI would have printed.
  type_name="$(curl -sf "$ENDPOINT/v2/instance-type" | jq -r '.["instance-types"][0] | "\(.family).\(.size)"')"
fi
case "$type_name" in
  *null*|.|"") fail "no instance type on offer: $types" ;;
esac
ok "$template_name, $type_name, offered in $ZONE"

echo "- create in $ZONE, from that catalogue, and read it back there"
span="$(prove_begin behaviour)"
exo compute instance create conformance-gva \
  --zone "$ZONE" --template "$template_name" --instance-type "$type_name" \
  >/dev/null || fail "instance create in $ZONE rejected"
instances="$(exo -O json compute instance list --zone "$ZONE")" || fail "instance list rejected: $instances"
id="$(printf '%s' "$instances" | jq -r '.[] | select(.name == "conformance-gva") | .id')"
[ -n "$id" ] || fail "the instance is not in the $ZONE list after create: $instances"
exo -Q compute instance delete "$id" --zone "$ZONE" --force >/dev/null \
  || fail "instance delete rejected"
prove_end "$span"
ok "instance $id lived and died in $ZONE"

echo "PASS: the zone is a datum — $ZONE served, ch-dk-2 gone, the real CLI drove it"
