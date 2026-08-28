#!/usr/bin/env bash
# The quick start, run exactly the way the README tells a reader to run it.
#
# #593 asks for a first example short enough to read, and then for the thing
# that keeps it from rotting: *a quickstart nobody runs is a README that rots*,
# and this is the example the most people will copy. So it gets a gate, and the
# gate is cheap precisely because the example is small.
#
# What it asserts, and the third is the one that finds things:
#
#   1. `feint up` on the example's own directory reaches a state where the
#      emulator answers and every declared ready condition passed — which is the
#      four-command journey the README prints, not an approximation of it;
#   2. the emulator itself answers afterwards, read from the emulator rather
#      than from Terraform's state file;
#   3. **the second plan is empty** — where an emulator that answers 200 and
#      stores something else shows up. Both defects the qualification stacks
#      found (#249, #250) were found by that plan rather than by an apply.
#
# Then `feint down`, and nothing answering the port afterwards.
#
# **No machine runtime, ever.** The examples declare `runtime: mode: off` and
# this script never overrides it: thirty seconds means a control plane, an
# address and a server the API describes. `FEINT_VM` is deliberately not read
# here — the runtime depth has examples/stacks and its own suites.
#
# It carries no proof.json and must not grow one. #503's rule is that a family a
# stack declares must be asserted and a family it cannot offer must write its
# reason; a quickstart that boots no machine can honour none of the families
# tools/conformance/functional.sh asks about, so declaring them would be a proof
# it cannot honour. functional.sh reads examples/stacks and only that.
#
# Usage: tools/conformance/quickstart.sh [addr]
set -uo pipefail

# Its own port by default, never the shared one: this suite starts and stops an
# emulator of its own, and pointing it at the address the rest of a run shares
# would have its cleanup stop somebody else's process. Same reasoning as
# tools/conformance/environment/up.sh, faults.sh and zones.sh.
ADDR="${1:-127.0.0.1:4597}"
SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
ROOT="$(cd "$SCRIPT_DIR/../.." && pwd)"
FEINT="${FEINT_BIN:-$ROOT/feint}"

fail() { echo "FAIL: $*" >&2; exit 1; }
ok() { echo "  ok: $*"; }

# Never let a client reach anything but the local emulator. Without this, a
# missing endpoint does not fail: every official client falls back to the
# operator's stored credentials, and a test creates billable resources on a real
# account. That is not hypothetical — it happened, to this repository.
# shellcheck source=/dev/null
. "$SCRIPT_DIR/guard.sh"
guard_local "http://$ADDR"

WORK=""
EXAMPLE=""
cleanup() {
  # The emulator goes even when an assertion failed: a leftover process holds
  # the port, and the next run then measures it instead of the code under test.
  # `down` first, so a failed run still destroys what it created; `stop` after,
  # because `down` is the verb that can refuse.
  if [ -n "$WORK" ] && [ -f "$WORK/terraform.tfstate" ]; then
    (cd "$WORK" && "$FEINT" down >/dev/null 2>&1) \
      || echo "warning: could not take $EXAMPLE down; the stop below still runs" >&2
  fi
  "$FEINT" stop --addr "$ADDR" >/dev/null 2>&1
  [ -n "$WORK" ] && rm -rf "$WORK"
  WORK=""
}
# EXIT rather than RETURN, and the difference is measured: fail() exits the
# script, and bash runs a RETURN trap only when a function returns. With RETURN
# the first broken run leaves the emulator holding the port, and every later run
# reports its leftovers instead of the defect that stopped the first.
trap cleanup EXIT INT TERM

[ -x "$FEINT" ] || fail "no feint binary at $FEINT; run \`mise run build\` first"

echo "conformance: the quick start examples, run the way the README prints them, on $ADDR"

# run_stack is the same verb tools/conformance/stacks.sh uses, and the spelling
# matters beyond taste: internal/cli/docs_proved.go reads `run_stack <name>`
# lines to answer which examples CI applies, and `feint docs --check` refuses an
# example directory no such line names. One idiom, one reader.
run_stack() { # name
  local name="$1"
  local src="$ROOT/examples/quickstart/$name"
  [ -d "$src" ] || fail "no quickstart at $src"

  # A copy, so the repository nobody asked to dirty stays clean: `up` records an
  # instance and the engine writes state beside the declaration. *.tf and
  # feint.yaml explicitly, never the whole directory: a reader who ran the
  # example in place leaves .terraform/ and terraform.tfstate behind, and
  # copying those would hand this run somebody else's state.
  WORK="$(mktemp -d)"
  EXAMPLE="$name"
  cp "$src"/*.tf "$WORK/" || fail "$name: cannot copy the configuration"
  cp "$src/feint.yaml" "$WORK/" || fail "$name: cannot copy the declaration"
  sed -i "s|addr: 127.0.0.1:4599|addr: $ADDR|g; s|tcp:127.0.0.1:4599|tcp:$ADDR|g" "$WORK/feint.yaml"
  cd "$WORK" || fail "$name: cannot enter the work directory"
  export TF_IN_AUTOMATION=1 TF_INPUT=0

  echo "- $name: feint up"
  local log="$WORK/up.log"
  "$FEINT" up --timeout 120s >"$log" 2>&1
  local code=$?
  [ "$code" -eq 0 ] || { cat "$log"; fail "$name: up exited $code"; }
  ok "up exited 0"

  # Every condition the declaration names was said out loud and confirmed. A
  # green run that printed nothing would be indistinguishable from one that
  # skipped the wait. The list is read from the declaration rather than restated
  # here, so an example that adds a condition is checked on it the same day.
  local conditions condition
  conditions="$(sed -n 's/^ *- \(http:[^ ]*\|tcp:[^ ]*\|resource:[^ ]*\)$/\1/p' "$WORK/feint.yaml")"
  [ -n "$conditions" ] || fail "$name: feint.yaml declares no ready condition, so up proved nothing"
  while IFS= read -r condition; do
    [ -n "$condition" ] || continue
    grep -qF "ok: $condition" "$log" \
      || { cat "$log"; fail "$name: the ready condition $condition was never confirmed"; }
  done <<<"$conditions"
  ok "every declared ready condition was said out loud and confirmed"

  # Asserted against the emulator rather than against up's own output.
  curl -sf "http://$ADDR/_feint/health" >/dev/null \
    || fail "$name: the emulator up brought up does not answer"
  ok "the emulator answers"

  # And the line the README prints under those four commands is the line this
  # run produced.
  #
  # #593's second finding was that it was not: the block showed `Apply complete!
  # Resources: 5 added` above three commands with no directory, no `main.tf` and
  # no provider block, so a reader following the quick start exactly could not
  # arrive at the output the quick start displayed. The number is derived now
  # (internal/cli/docs_quickstart.go counts the resource blocks), and this is the
  # other half: the sentence is lifted out of the README and required verbatim,
  # so a generated claim about output is checked against output.
  #
  # Only for the example the README documents, which is read from the block's own
  # `cd` line rather than assumed: the README leads with one pack and this suite
  # runs every one of them.
  local readme="$ROOT/README.md"
  if [ -f "$readme" ]; then
    local documented printed
    documented="$(sed -n 's|^cd [^/]*/examples/quickstart/\([a-z0-9-]*\)$|\1|p' "$readme" | head -1)"
    printed="$(grep -m1 '^Apply complete! Resources:' "$readme")"
    if [ "$documented" = "$name" ]; then
      [ -n "$printed" ] \
        || fail "$name: the README documents this example and prints no apply line, so nothing says what the four commands produce"
      grep -qF "$printed" "$log" \
        || { cat "$log"; fail "$name: the README prints \"$printed\" and this run did not: the output shown is not the output produced"; }
      ok "the README's own apply line is the one this run printed"
    fi
  fi

  # The assertion that separates a test from a demonstration: what was sent is
  # what is served back. No environment is exported for it, and that is not an
  # omission — both examples carry their credentials and their endpoint in the
  # configuration, which is what makes them copyable in the first place.
  #
  # The engine is the one the declaration names, resolved the way `feint up`
  # resolves it: exec.LookPath on `iac.engine`, so the binary that plans is the
  # binary that applied. The first version of this script guessed instead —
  # `tofu` if installed, else `terraform` — and on a station holding both it
  # planned with OpenTofu a directory Terraform had initialised. What comes back
  # is not a drift report but *Inconsistent dependency lock file*, because the
  # lock names `registry.terraform.io` and OpenTofu wants
  # `registry.opentofu.org`: exit status 1, and a case arm reading "the second
  # plan errored" about an emulator that had answered perfectly. Measured
  # 2026-08-28, on the first real run of this gate.
  local engine
  engine="$(sed -n 's/^ *engine: *\([a-z]*\) *$/\1/p' "$WORK/feint.yaml" | head -1)"
  [ -n "$engine" ] || fail "$name: feint.yaml declares no iac.engine, so nothing says what applied"
  command -v "$engine" >/dev/null 2>&1 \
    || fail "$name: the declaration names the engine $engine and it is not installed"

  echo "- $name: the second plan is empty, read by $engine"
  "$engine" plan -no-color -detailed-exitcode -var "endpoint=http://$ADDR" >/dev/null 2>&1
  local status=$?
  case "$status" in
    0) ok "no drift between what was sent and what is served" ;;
    2) "$engine" plan -no-color -var "endpoint=http://$ADDR" || true
       fail "$name: the emulator does not read back what the quick start sent" ;;
    *) "$engine" plan -no-color -var "endpoint=http://$ADDR" || true
       fail "$name: the second plan errored with status $status" ;;
  esac

  echo "- $name: feint down"
  "$FEINT" down >"$WORK/down.log" 2>&1
  code=$?
  [ "$code" -eq 0 ] || { cat "$WORK/down.log"; fail "$name: down exited $code"; }
  ok "down exited 0"

  # Checked, not assumed.
  if curl -sf --max-time 2 "http://$ADDR/_feint/health" >/dev/null 2>&1; then
    fail "$name: down returned and something still answers on $ADDR"
  fi
  ok "nothing answers on $ADDR"

  cd "$ROOT" || fail "$name: cannot leave the work directory"
  rm -rf "$WORK"
  WORK=""
}

run_stack scaleway
run_stack outscale

echo "conformance: the quick start examples were brought up, re-planned empty and taken down"
