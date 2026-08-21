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
set -euo pipefail

leg="${1:-}"
endpoint="${2:-http://${FEINT_ADDR:-127.0.0.1:4599}}"
addr="${endpoint#http://}"

usage() {
  cat >&2 <<'EOF'
usage: mise run conformance:leg -- <leg>

  scw-cli     the Scaleway CLI alone
  terraform   both Terraform fixtures, one engine
  opentofu    the same through OpenTofu
  oapi-cli    the Outscale CLI alone
  exo-cli     the Exoscale CLI alone
  probe       the contract-driven probe alone, no client at all
  fields      every client against one emulator, with the whole-run gate on

The leg names are the matrix entries of .github/workflows/conformance.yml. A
name that is not one of them is refused rather than guessed: a leg that silently
ran something else would measure the wrong thing and report success.
EOF
  exit 2
}

case "$leg" in
  scw-cli|terraform|opentofu|oapi-cli|exo-cli|probe|fields) ;;
  *) usage ;;
esac

# --shapes is not passed: it is a `serve` flag whose default is already `shapes`,
# and `start` spawns serve. Passing it here fails on an unknown flag, which is
# how this line was found.
./feint start --addr "$addr" --vm off --contracts contracts --timeout 60s
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
  oapi-cli)
    tools/conformance/outscale/oapi-cli.sh "$endpoint"
    ;;
  exo-cli)
    tools/conformance/exoscale/exo-cli.sh "$endpoint"
    ;;
  probe)
    ./feint probe --endpoint "$endpoint" --contracts contracts
    ;;
  fields)
    tools/conformance/scaleway/scw-cli.sh "$endpoint"
    tools/conformance/outscale/oapi-cli.sh "$endpoint"
    tools/conformance/exoscale/exo-cli.sh "$endpoint"
    tools/conformance/scaleway/terraform.sh "$endpoint"
    tools/conformance/outscale/terraform.sh "$endpoint"
    # The fault-injection suite runs on this leg in CI, and it belongs here for
    # the same reason: it is the only leg carrying all four clients at once.
    # Its own emulator is on its own port, so the score below still judges the
    # run this script started, and that separation is what the score's own
    # injected-answer refusal is there to hold.
    tools/conformance/faults.sh
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
