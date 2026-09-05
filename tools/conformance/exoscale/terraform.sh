#!/usr/bin/env bash
# Conformance check: the real Exoscale Terraform provider against the emulator.
#
# THIS SUITE WAS SUSPENDED FOR TEN DAYS, and how it came back matters more than
# that it did. Until v0.71.0 the published provider honoured
# EXOSCALE_API_ENDPOINT for its egoscale v3 client and built a v2 client with no
# endpoint option at all, so an apply neither failed nor worked: it SPLIT
# between the emulator and a paying account, in one run, with whatever
# credentials the environment held (upstream
# exoscale/terraform-provider-exoscale#573, this project's #525). The decision of
# 2026-08-26 was to refuse Terraform for this pack entirely — fork included, a
# patched client not being the official client.
#
# Upstream fixed it in #576 and published v0.71.0 on 2026-08-31. The refusal came
# off on a measurement rather than on that release note, and the measurement had
# a control. Driven through `feint proxy --forward '*.exoscale.com=<emulator>'`,
# which accepts the CONNECT, terminates the TLS and records every host asked for
# — so a provider that ignores its endpoint is caught rather than obeyed — on
# examples/stacks/exoscale, 2026-09-05:
#
#	provider   apply       second plan           destroy       hosts on the wire
#	v0.70.0    15 created  no resource changes   15 destroyed  57 to api-ch-{dk-2,gva-2}
#	v0.71.0    15 created  no resource changes   15 destroyed  NONE
#
# The v0.70.0 row is what makes the v0.71.0 row mean anything: an empty
# transcript proves nothing until the instrument has been shown able to report a
# full one.
#
# TWO THINGS THIS SUITE THEREFORE DOES THAT ITS SIBLINGS DO NOT.
#
#  1. It pins a floor. A configuration resolving a provider older than v0.71.0
#     walks straight back into the split, so the fixture states `>= 0.71.0` and
#     the emulator refuses an older one by user agent (guardSplitClients).
#  2. It drives the example stack rather than a fixture of its own. That stack
#     is the platform shape this pack asserts, it was the subject of #525, and
#     it is what `stacks.sh` applies beside the Scaleway and Outscale ones now.
#
# The binary is OpenTofu when it is installed, Terraform otherwise; FEINT_TF
# forces either. Both resolve the same provider from the same registry
# namespace, which is why the refusal covered both and the floor does too.
#
# Usage: tools/conformance/exoscale/terraform.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"

# Never let a client reach anything but the local emulator. Without this, a
# missing endpoint does not fail: every official client falls back to the
# operator's stored credentials, and a test creates billable resources on a real
# account. That is not hypothetical — it happened, to this repository, and this
# pack is the one it happened to.
# shellcheck source=/dev/null
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/guard.sh"
guard_local "$ENDPOINT"
# shellcheck source=/dev/null
. "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/prove.sh"

TF="${FEINT_TF:-}"
if [ -z "$TF" ]; then
  if command -v tofu >/dev/null 2>&1; then TF=tofu; else TF=terraform; fi
fi
command -v "$TF" >/dev/null 2>&1 || { echo "FAIL: neither tofu nor terraform is installed" >&2; exit 1; }
command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }

STACK="$(cd "$(dirname "${BASH_SOURCE[0]}")/../../../examples/stacks/exoscale" && pwd)"
# Resolved BEFORE the cd below, or the relative path names nothing: measured on
# the first full conformance pass, which failed with `cd: tools/conformance/exoscale:
# No such file or directory` from inside the work directory.
CREDENTIALS="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)/fake-credentials.env"
WORK="$(mktemp -d)"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

# An apply that fails leaves resources in the emulator's store, so the destroy
# has to happen on the error path too. Without this the first broken run poisons
# every later one.
DESTROYED=0
cleanup() {
  local status=$?
  if [ "$DESTROYED" = "0" ] && [ -f "$WORK/terraform.tfstate" ]; then
    echo "- destroy (cleaning up after a failed run)"
    (cd "$WORK" && "$TF" destroy -no-color -auto-approve) \
      || echo "FAIL: could not destroy; resources may be left behind" >&2
  fi
  rm -rf "$WORK"
  exit "$status"
}
trap cleanup EXIT

cp "$STACK"/*.tf "$WORK/"
cd "$WORK"

export TF_IN_AUTOMATION=1
export TF_INPUT=0
# The pack's own fake pair, and the endpoint with /v2 inside the value, which is
# how this provider reads it. The credentials go last so they outrank whatever
# the caller's shell holds — the property #525 leaned on and
# TestThePacksOwnCredentialsOutrankTheCallersShell holds.
set -a
# shellcheck source=/dev/null
. "$CREDENTIALS"
set +a
export EXOSCALE_API_ENDPOINT="$ENDPOINT/v2"
export EXOSCALE_ZONE="${EXOSCALE_ZONE:-ch-dk-2}"
export TF_VAR_zone="$EXOSCALE_ZONE"

echo "conformance: $TF against $ENDPOINT"

"$TF" init -no-color -upgrade
"$TF" validate -no-color

# The floor, asserted rather than assumed: a run that resolved an older provider
# would measure the emulator against the client this pack spent ten days
# refusing, and the emulator would refuse it — but the failure would read as a
# broken suite rather than as a pinned-too-low configuration.
# The registry host differs by binary — registry.terraform.io for Terraform,
# registry.opentofu.org for OpenTofu — so the key is matched on its tail rather
# than spelled out. Written with the Terraform host alone, this suite reported
# "no exoscale provider was resolved" under tofu, which reads like a broken
# fixture rather than a lookup that missed.
resolved="$("$TF" version -json | jq -r '.provider_selections | to_entries[] | select(.key | endswith("/exoscale/exoscale")) | .value' | head -1)"
[ -n "$resolved" ] || fail "no exoscale provider was resolved"
lowest="$(printf '%s\n0.71.0\n' "$resolved" | sort -V | head -1)"
[ "$lowest" = "0.71.0" ] || fail "resolved provider $resolved is below the v0.71.0 floor: an apply would split between this emulator and a paying account (#525, upstream #573)"
ok "provider $resolved, at or above the v0.71.0 floor"

if [ "${FEINT_TF_APPLY:-1}" != "1" ]; then
  echo "conformance: plan passed, apply skipped by FEINT_TF_APPLY=0"
  exit 0
fi

echo "- apply"
span="$(prove_begin behaviour)"
"$TF" apply -no-color -auto-approve

# The provider is satisfied, which is what an apply proves. These ask the
# emulator directly, so a state file that agrees with itself cannot pass for a
# platform that exists.
echo "- what the apply built answers in the API"
for kind in private-network instance-pool load-balancer elastic-ip; do
  body="$(curl -s "$ENDPOINT/v2/$kind")"
  count="$(printf '%s' "$body" | jq -r '(."'"$kind"'s" // .[keys_unsorted[0]] // []) | length' 2>/dev/null || echo 0)"
  [ "${count:-0}" -ge 1 ] || fail "the apply left no $kind in the emulator: $body"
  ok "$count $kind(s)"
done

echo "- a second plan changes no resource"
# -detailed-exitcode answers 2 for ANY difference, outputs included, and an
# output recorded after the apply is not drift. So the resource count is what is
# asserted, read out of the plan itself.
"$TF" plan -no-color -out tfplan >/dev/null
changes="$("$TF" show -json tfplan | jq '[.resource_changes[]? | select(.change.actions != ["no-op"])] | length')"
[ "$changes" = "0" ] || fail "the second plan wants to change $changes resource(s): the apply is not idempotent"
ok "no resource changes"

echo "- destroy"
"$TF" destroy -no-color -auto-approve
DESTROYED=1
prove_end "$span"

echo "- the platform is gone from the API"
for kind in private-network instance-pool load-balancer; do
  body="$(curl -s "$ENDPOINT/v2/$kind")"
  count="$(printf '%s' "$body" | jq -r '(."'"$kind"'s" // .[keys_unsorted[0]] // []) | length' 2>/dev/null || echo 0)"
  [ "${count:-0}" = "0" ] || fail "$count $kind(s) survived the destroy: $body"
done
ok "nothing left behind"

echo "conformance: $TF drives the Exoscale pack end to end"
