#!/usr/bin/env bash
# The Exoscale stack, driven by the patched provider.
#
# WHY THIS IS A TASK OF ITS OWN AND NOT PART OF `mise run conformance`
#
# The published Exoscale provider builds two clients and only one honours
# EXOSCALE_API_ENDPOINT, so an apply does not fail — it *splits*, half against
# this emulator and half against a paying account with whatever credentials the
# environment holds. The emulator refuses that client by its user agent rather
# than serving half of it (docs/limits.md, upstream #573).
#
# The fix is four lines per site and lives on a fork, pinned in docs/limits.md
# to commit 2e78b42. Two consequences, both deliberate and both stated in
# CLAUDE.md:
#
#   1. No gate here clones a third-party repository. That would put someone
#      else's availability in this project's CI, and a client this project
#      patched is not the official client, so it could not count towards
#      conformance anyway. This script is therefore never called by
#      `mise run conformance` — it is `mise run conformance:exoscale-terraform`,
#      on the same terms as `conformance:ssh`.
#   2. The refusal is named rather than hidden. A guard with no way past it gets
#      worked around by copying the emulator, which teaches nobody anything, so
#      FEINT_EXOSCALE_ALLOW_TERRAFORM=1 exists and this script sets it.
#
# What it is worth in spite of that: it exercises the Exoscale pack through the
# client a user would actually reach for. Applied by hand on 2026-08-18 —
# 13 resources, empty second plan, clean destroy — and this script is that
# procedure, so the next reader does not have to reconstruct it.
set -euo pipefail

ENDPOINT="${1:?usage: terraform.sh <emulator-url>}"
ROOT="$(cd "$(dirname "$0")/../../.." && pwd)"
STACK="$ROOT/examples/stacks/exoscale"
PROVIDER_DIR="${FEINT_EXOSCALE_PROVIDER_DIR:-/tmp/tfp}"
BIN="$PROVIDER_DIR/terraform-provider-exoscale"
PINNED_COMMIT=2e78b42

fail() { printf '\nFAIL: %s\n' "$*" >&2; exit 1; }
ok()   { printf '  ok: %s\n' "$*"; }

# The doorstep. Asked at second zero rather than thirty steps in, on the model
# #375 established: the answer has not changed since the script started, so
# there is no reason to discover it after an apply has already run.
#
# It names the remedy in full. A reader who has never built the fork must not
# have to open docs/limits.md to get past this line — that is what turns a
# guard into something people route around.
if [ ! -x "$BIN" ]; then
  cat >&2 <<MSG

FAIL: the patched Exoscale provider is not built at $BIN

  The published provider cannot drive this emulator: it builds two clients and
  only one honours EXOSCALE_API_ENDPOINT, so an apply splits between the
  emulator and a paying account. The emulator refuses it by user agent.

  Build the pinned fork (docs/limits.md, "The patched provider, while upstream
  decides"), or set FEINT_EXOSCALE_PROVIDER_DIR at a directory that holds it:

    git clone -b fix/v2-client-honours-api-endpoint \\
      https://github.com/stephrobert/terraform-provider-exoscale
    cd terraform-provider-exoscale && git checkout $PINNED_COMMIT
    go build -o $PROVIDER_DIR/terraform-provider-exoscale .

  No gate in this repository clones that repository for you, deliberately:
  it would put somebody else's availability in this pipeline.

MSG
  exit 1
fi
ok "the patched provider is built at $BIN"

command -v terraform >/dev/null 2>&1 || fail "terraform is not installed"

WORK="$(mktemp -d)"
CLEAN=1
cleanup() {
  # Destruction first, and its verdict is reported rather than swallowed: a
  # teardown that fails silently leaves the next run to meet the leftovers.
  if [ "$CLEAN" = 1 ] && [ -f "$WORK/terraform.tfstate" ]; then
    TF_CLI_CONFIG_FILE="$WORK/dev.tfrc" terraform -chdir="$WORK" destroy -auto-approve \
      >/dev/null 2>&1 || printf 'WARN: destroy failed, %s kept for inspection\n' "$WORK" >&2
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT INT TERM

cp "$STACK"/*.tf "$WORK/"
cat > "$WORK/dev.tfrc" <<RC
provider_installation {
  dev_overrides { "exoscale/exoscale" = "$PROVIDER_DIR" }
  direct {}
}
RC

# The credentials are the repository's public placeholders, in the one file that
# holds them. Never inline: CLAUDE.md rule 8, and a value written into a script
# is a value somebody eventually believes is real.
set -a
# shellcheck source=/dev/null
. "$ROOT/tools/conformance/exoscale/fake-credentials.env"
set +a

export TF_CLI_CONFIG_FILE="$WORK/dev.tfrc"
export EXOSCALE_API_ENDPOINT="$ENDPOINT"

echo "- the Exoscale stack applies through the patched provider"
terraform -chdir="$WORK" apply -auto-approve -input=false >"$WORK/apply.log" 2>&1 \
  || { tail -30 "$WORK/apply.log" >&2; fail "apply rejected"; }
applied="$(terraform -chdir="$WORK" state list | wc -l)"
[ "$applied" -gt 0 ] || fail "the apply created no resource, so nothing below measures anything"
ok "$applied resources"

# The second plan is the assertion that matters. An apply that succeeds proves
# the emulator accepted the writes; an empty second plan proves it answers back
# what the provider wrote, which is the property a stack actually depends on.
echo "- a second plan is empty"
if ! terraform -chdir="$WORK" plan -detailed-exitcode -input=false >"$WORK/plan.log" 2>&1; then
  case $? in
    2) tail -40 "$WORK/plan.log" >&2
       fail "the second plan is not empty: the emulator does not read back what the stack sent" ;;
    *) tail -30 "$WORK/plan.log" >&2; fail "plan rejected" ;;
  esac
fi
ok "no drift"

echo "- destroy leaves nothing"
terraform -chdir="$WORK" destroy -auto-approve -input=false >"$WORK/destroy.log" 2>&1 \
  || { tail -30 "$WORK/destroy.log" >&2; fail "destroy rejected"; }
left="$(terraform -chdir="$WORK" state list | wc -l)"
[ "$left" = 0 ] || fail "$left resource(s) survived the destroy"
CLEAN=0
ok "state is empty"

printf '\nexoscale terraform: %s resources applied, empty second plan, clean destroy\n' "$applied"
