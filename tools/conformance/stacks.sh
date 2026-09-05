#!/usr/bin/env bash
# The example stacks, applied against a running emulator.
#
# These are not fixtures. `tools/conformance/*/terraform/` holds configurations
# written to exercise what somebody thought to assert; `examples/stacks/` holds
# configurations written the way a platform team writes them, and the difference
# is not cosmetic. Within an hour of the first one being applied, two defects
# surfaced that every existing gate was blind to:
#
#   #249 — a route could not point at a Net peering, so two peered Nets stayed
#          unreachable. The suite creates peerings and asserts their state
#          machine; it never routes through one.
#   #250 — a tagged NIC read back without its tags, so a Terraform plan never
#          converged. The suite tagged Nets, Vms and volumes, never an interface.
#
# So they run here, on every pull request, for the same reason the examples are
# in this repository rather than in one of their own: an example nobody runs is
# an example that rots, and this one is also a test.
#
# What each stack must do, and the third is the one that finds things:
#
#   1. apply
#   2. destroy cleanly
#   3. **plan empty in between** — where an emulator that answers 200 and stores
#      something else shows up
#
# Usage: tools/conformance/stacks.sh [endpoint]
set -euo pipefail

ENDPOINT="${1:-http://127.0.0.1:4599}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"

# Never let a client reach anything but the local emulator. Without this, a
# missing endpoint does not fail: every official client falls back to the
# operator's stored credentials, and a test creates billable resources on a real
# account. That is not hypothetical — it happened, to this repository.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/guard.sh"
guard_local "$ENDPOINT"

TF="${FEINT_TF:-}"
if [ -z "$TF" ]; then
  if command -v tofu >/dev/null 2>&1; then TF=tofu; else TF=terraform; fi
fi
command -v "$TF" >/dev/null 2>&1 || { echo "FAIL: neither tofu nor terraform is installed" >&2; exit 1; }

command -v jq >/dev/null 2>&1 || { echo "FAIL: jq is not installed" >&2; exit 1; }

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

echo "conformance: the example stacks against $ENDPOINT, with $TF"

# The destroy has to happen on the error path too. Without it the first broken
# run poisons every later one: the emulator keeps the half-applied resources,
# and the rerun fails on "subnet 10.30.1.0/24 overlaps 10.30.1.0/24" — its own
# leftovers — instead of the defect that stopped the first run. CI never sees
# this, because every leg gets a fresh emulator; the operator's station does.
#
# An EXIT trap, deliberately not RETURN. The first version of this file hung
# the cleanup on `trap cleanup_stack RETURN`, and that trap never fired where
# it mattered: fail() exits the script, and bash runs a RETURN trap only when
# the function returns, never on exit. Falsified on 2026-08-17 against a fresh
# container, by duplicating the app subnet so the apply fails after networks
# exist: with RETURN, the rerun reported three overlaps — admin among them,
# which nothing in the plan duplicates, so it collided with its own leftover;
# with EXIT, the rerun reports exactly the one overlap the sabotage put in
# the plan.
WORK=""
STACK=""
DESTROYED=1
# How this stack is told where the emulator is. Two of the three take a
# variable; the Exoscale provider has no endpoint attribute and reads
# EXOSCALE_API_ENDPOINT, with the /v2 path inside the value. Held here rather
# than at each of the four call sites below, which is what made adding a third
# stack a rewrite instead of a line.
VARS=()
cleanup_stack() {
  [ -n "$WORK" ] || return 0
  if [ "$DESTROYED" = "0" ] && [ -f "$WORK/terraform.tfstate" ]; then
    (cd "$WORK" && "$TF" destroy -no-color -auto-approve ${VARS[@]+"${VARS[@]}"} >/dev/null 2>&1) \
      || echo "FAIL: could not destroy $STACK; resources may be left behind" >&2
  fi
  rm -rf "$WORK"
  WORK=""
}
trap cleanup_stack EXIT

run_stack() { # name
  local name="$1"
  local src="$ROOT/examples/stacks/$name"
  [ -d "$src" ] || fail "no stack at $src"

  case "$name" in
    exoscale)
      VARS=()
      # The pack's own fake pair, exported last so it outranks whatever the
      # caller's shell holds — the property #525 leaned on, and the one thing
      # that stopped that incident from reaching a paying account.
      # `set -a` around it, which is what the file's own header asks for: it
      # holds assignments, not exports.
      set -a
      # shellcheck source=/dev/null
      . "$ROOT/tools/conformance/exoscale/fake-credentials.env"
      set +a
      export EXOSCALE_API_ENDPOINT="$ENDPOINT/v2"
      export EXOSCALE_ZONE="${EXOSCALE_ZONE:-ch-dk-2}"
      export TF_VAR_zone="$EXOSCALE_ZONE"
      ;;
    *)
      VARS=(-var "endpoint=$ENDPOINT")
      ;;
  esac

  # A copy, so the working directory of a repository nobody asked to dirty stays
  # clean: state files and provider caches belong to the run, not to the tree.
  WORK="$(mktemp -d)"
  STACK="$name"
  DESTROYED=0
  # *.tf and modules/ explicitly, never the whole directory: a reader who ran
  # a stack in place leaves .terraform/ and terraform.tfstate behind, and
  # copying those would hand this run somebody else's state.
  cp "$src"/*.tf "$WORK/"
  if [ -d "$src/modules" ]; then
    cp -R "$src/modules" "$WORK/"
  fi

  cd "$WORK"
  export TF_IN_AUTOMATION=1 TF_INPUT=0

  echo "- $name: init and apply"
  "$TF" init -no-color -upgrade >/dev/null || fail "$name: init failed"
  "$TF" apply -no-color -auto-approve ${VARS[@]+"${VARS[@]}"} >/dev/null \
    || fail "$name: apply failed"
  ok "applied"

  # The assertion that separates a test from a demonstration. Both defects this
  # file's header names were caught here rather than by the apply.
  echo "- $name: the second plan changes no resource"
  # Resources, not `-detailed-exitcode`. That flag answers 2 for ANY difference,
  # outputs included, and Terraform itself prints "without changing any real
  # infrastructure" for the case it then fails on: two outputs of the Exoscale
  # stack are read through data sources and reported as additions on the second
  # plan while every resource is a no-op (measured 2026-09-05).
  #
  # What this gate is for is resource drift — a route that could not point at a
  # Net peering (#249), a tagged NIC that read back without its tags (#250) —
  # and both of those are resource changes, so both still fail here.
  "$TF" plan -no-color -out tfplan ${VARS[@]+"${VARS[@]}"} >/dev/null 2>&1 \
    || fail "$name: the second plan errored"
  local changes
  changes="$("$TF" show -json tfplan | jq '[.resource_changes[]? | select(.change.actions != ["no-op"])] | length')"
  if [ "$changes" != "0" ]; then
    "$TF" plan -no-color ${VARS[@]+"${VARS[@]}"} || true
    fail "$name: the emulator does not read back what the stack sent ($changes resource change(s))"
  fi
  ok "no resource changes"
  # And the outputs, printed rather than judged: a difference here is worth
  # seeing and is not infrastructure drift.
  local outputs
  outputs="$("$TF" show -json tfplan | jq '[.output_changes // {} | to_entries[] | select(.value.actions != ["no-op"])] | length')"
  [ "$outputs" = "0" ] || echo "  note: $outputs output(s) would be recorded by an apply, no resource affected"

  echo "- $name: destroy"
  "$TF" destroy -no-color -auto-approve ${VARS[@]+"${VARS[@]}"} >/dev/null \
    || fail "$name: destroy failed"
  DESTROYED=1
  ok "destroyed"
  cleanup_stack
}

run_stack scaleway
run_stack outscale
# Exoscale, applied here from 2026-09-05: suspended for ten days because the
# published provider split its calls between this emulator and a paying account
# (#525), restored on the measurement that showed v0.71.0 does not (#644).
run_stack exoscale

echo "conformance: the example stacks applied, re-planned empty and destroyed"
