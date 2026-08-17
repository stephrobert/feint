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

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

echo "conformance: the example stacks against $ENDPOINT, with $TF"

run_stack() { # name
  local name="$1"
  local src="$ROOT/examples/stacks/$name"
  [ -d "$src" ] || fail "no stack at $src"

  # A copy, so the working directory of a repository nobody asked to dirty stays
  # clean: state files and provider caches belong to the run, not to the tree.
  local work
  work="$(mktemp -d)"
  cp "$src"/*.tf "$work/"

  # The destroy has to happen on the error path too. Without it the first broken
  # run poisons every later one, and the operator sweeps by hand.
  local destroyed=0
  cleanup_stack() {
    if [ "$destroyed" = "0" ] && [ -f "$work/terraform.tfstate" ]; then
      (cd "$work" && "$TF" destroy -no-color -auto-approve -var "endpoint=$ENDPOINT" >/dev/null 2>&1) \
        || echo "FAIL: could not destroy $name; resources may be left behind" >&2
    fi
    rm -rf "$work"
  }
  trap cleanup_stack RETURN

  cd "$work"
  export TF_IN_AUTOMATION=1 TF_INPUT=0

  echo "- $name: init and apply"
  "$TF" init -no-color -upgrade >/dev/null || fail "$name: init failed"
  "$TF" apply -no-color -auto-approve -var "endpoint=$ENDPOINT" >/dev/null \
    || fail "$name: apply failed"
  ok "applied"

  # The assertion that separates a test from a demonstration. Both defects this
  # file's header names were caught here rather than by the apply.
  echo "- $name: the second plan is empty"
  local status=0
  "$TF" plan -no-color -detailed-exitcode -var "endpoint=$ENDPOINT" >/dev/null 2>&1 || status=$?
  case "$status" in
    0) ok "no drift between what was sent and what is served" ;;
    2) "$TF" plan -no-color -var "endpoint=$ENDPOINT" || true
       fail "$name: the emulator does not read back what the stack sent" ;;
    *) fail "$name: the second plan errored with status $status" ;;
  esac

  echo "- $name: destroy"
  "$TF" destroy -no-color -auto-approve -var "endpoint=$ENDPOINT" >/dev/null \
    || fail "$name: destroy failed"
  destroyed=1
  ok "destroyed"
}

run_stack scaleway
run_stack outscale

echo "conformance: the example stacks applied, re-planned empty and destroyed"
