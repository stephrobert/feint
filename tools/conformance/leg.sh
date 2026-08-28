#!/usr/bin/env bash
# Reproduce one leg of the conformance matrix, locally, exactly as CI runs it.
#
# Why this exists, and it is not a convenience. `mise run conformance` drives
# every suite against one emulator, which is the population a whole-run verdict
# assumes: a gate that judges a run is green there **by construction**. CI does
# not do that. `.github/workflows/conformance.yml` splits the clients across a
# matrix, one emulator per leg, so every leg but `fields` is a partial run.
#
# That difference cost two red pull requests on the omission gate (#88), both
# invisible to a full local run:
#
#   - the `probe` leg drives no client, so every object in it is the minimal one
#     the probe's seeding builds, and the gate reported UserData and Tags as
#     omissions of the emulator;
#   - the `terraform` and `opentofu` legs drive real clients but not all of
#     them, so no volume comes from a snapshot and no rule names a group.
#
# So: before pushing a change that adds or moves a gate whose verdict is about
# "this run", run it here against the leg that has the least to work with.
#
#     mise run conformance:leg -- probe
#     mise run conformance:leg -- terraform
#
# `fields` is the whole-run leg, the only one where FEINT_FIELD_GATE is set,
# and it is what `mise run conformance` already approximates.
#
# THE LEG THAT WAS MISSING, AND WHAT ITS ABSENCE COST (#459)
#
# Every leg above runs with no machine runtime, because the conformance matrix
# does. The suites that need one live in `.github/workflows/runtime-proof.yml`,
# and until this script grew a `runtime` leg nothing reproduced them but
# `FEINT_VM=incus-ovn mise run conformance` — measured at 1331 s on 2026-08-27,
# of which the four dataplane suites are 675 s and every client suite before
# them is 656 s that a change to `internal/core/machine` did not need. Agents
# working on the lifecycle re-ran the whole thing per attempt.
#
#     FEINT_VM=incus-ovn mise run conformance:leg -- runtime
#
# It refuses to run with no runtime rather than letting its four suites skip
# themselves: a leg that measures nothing must not report success.
set -euo pipefail

leg="${1:-}"
endpoint="${2:-http://${FEINT_ADDR:-127.0.0.1:4599}}"
addr="${endpoint#http://}"
# Read from the environment, exactly as `serve` and `conformance` read it, so
# one habit covers all three. Off by default: starting machines on the
# operator's host is a side effect and it gets asked for.
vm="${FEINT_VM:-off}"

usage() {
  cat >&2 <<'EOF'
usage: mise run conformance:leg -- <leg>

  scw-cli     the Scaleway CLI alone
  terraform   both Terraform fixtures, one engine
  opentofu    the same through OpenTofu
  octl        the Outscale CLI alone
  exo-cli     the Exoscale CLI alone
  probe       the contract-driven probe alone, no client at all
  fields      every client against one emulator, with the whole-run gate on
  runtime     the four dataplane suites, and nothing else; needs FEINT_VM

The first seven names are the matrix entries of
.github/workflows/conformance.yml. A name that is not one of them is refused
rather than guessed: a leg that silently ran something else would measure the
wrong thing and report success.

`runtime` is not one of them: it reproduces the network and balancer steps of
.github/workflows/runtime-proof.yml, which the conformance matrix does not
carry because those need real machines.

    FEINT_VM=incus-ovn mise run conformance:leg -- runtime
EOF
  exit 2
}

case "$leg" in
  scw-cli|terraform|opentofu|octl|exo-cli|probe|fields|runtime) ;;
  *) usage ;;
esac

# The four suites of the `runtime` leg all begin by asking the emulator whether
# a machine runtime is configured, and skip themselves when none is. Skipping is
# right inside `mise run conformance`, which must stay runnable in CI; it is
# wrong here, where the leg was asked for by name. A run that measured nothing
# and exited 0 is the verdict this repository refuses everywhere else.
if [ "$leg" = "runtime" ] && [ "$vm" = "off" ]; then
  echo "conformance:leg runtime: no machine runtime configured." >&2
  echo "  Its four suites would each skip themselves and the leg would report success" >&2
  echo "  having measured nothing. Ask for a runtime:" >&2
  echo "    FEINT_VM=incus-ovn mise run conformance:leg -- runtime" >&2
  exit 2
fi

# The doorstep (#375, #521), on the same terms as `mise run conformance`: the
# question is asked at second zero rather than after every suite has run, and
# again once the emulator is gone, so a leg that leaks fails the leg that
# leaked. With no runtime it says so and asks nothing.
tools/conformance/guard.sh leftovers "$vm"

# --shapes is not passed: it is a `serve` flag whose default is already `shapes`,
# and `start` spawns serve. Passing it here fails on an unknown flag, which is
# how this line was found.
./feint start --addr "$addr" --vm "$vm" --contracts contracts --timeout 60s
trap './feint stop --addr '"$addr"' >/dev/null 2>&1 || true' EXIT

case "$leg" in
  scw-cli)
    tools/conformance/scaleway/scw-cli.sh "$endpoint"
    ;;
  terraform|opentofu)
    # Both fixtures, the way the workflow runs them on either engine.
    tools/conformance/scaleway/terraform.sh "$endpoint"
    tools/conformance/outscale/terraform.sh "$endpoint"
    ;;
  octl)
    tools/conformance/outscale/octl.sh "$endpoint"
    ;;
  exo-cli)
    tools/conformance/exoscale/exo-cli.sh "$endpoint"
    ;;
  probe)
    ./feint probe --endpoint "$endpoint" --contracts contracts
    ;;
  fields)
    tools/conformance/scaleway/scw-cli.sh "$endpoint"
    tools/conformance/outscale/octl.sh "$endpoint"
    tools/conformance/exoscale/exo-cli.sh "$endpoint"
    tools/conformance/scaleway/terraform.sh "$endpoint"
    tools/conformance/outscale/terraform.sh "$endpoint"
    # The fault-injection suite runs on this leg in CI, and it belongs here for
    # the same reason: it is the only leg carrying all four clients at once.
    # Its own emulator is on its own port, so the score below still judges the
    # run this script started, and that separation is what the score's own
    # injected-answer refusal is there to hold.
    tools/conformance/faults.sh
    # The recorded refusals (#390). Unlike the suite above it shares this leg's
    # emulator, which is safe by construction and checked rather than assumed:
    # `feint replay --refusals-only` reads each corpus whole and sends nothing
    # unless every exchange of it is a 4xx.
    tools/conformance/refusals.sh "$endpoint"
    ;;
  runtime)
    # The order runtime-proof.yml uses, and the balancer last because it is the
    # only one whose verdict depends on a declared capability rather than on
    # the mode's name (#315): under a bridge it skips itself and says so.
    tools/conformance/scaleway/network.sh "$endpoint"
    tools/conformance/outscale/network.sh "$endpoint"
    tools/conformance/exoscale/network.sh "$endpoint"
    tools/conformance/outscale/balancer.sh "$endpoint"
    ;;
esac

# The same score step CI runs, with the same rule about the field gate: set only
# on the whole-run leg. Everywhere else the omission findings print and judge
# nothing, and seeing them print here is the point of running a partial leg.
if [ "$leg" = "fields" ]; then
  FEINT_FIELD_GATE=1 tools/conformance/score.sh "$endpoint"
else
  FEINT_FIELD_GATE=0 tools/conformance/score.sh "$endpoint"
fi

# The other half of the doorstep: asked again once the emulator is gone, so the
# leg that left a machine or a network behind is the leg that fails. The trap
# above stays the safety net for a leg that dies mid-flight.
./feint stop --addr "$addr"
tools/conformance/guard.sh leftovers-after "$vm"
