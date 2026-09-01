#!/usr/bin/env bash
# The drift gate, in one place because it was in two and they disagreed.
#
# `mise run drift:check` measured Scaleway on instance, vpc and ipam, plus
# Outscale, plus Exoscale. The weekly CI job measured Scaleway on instance alone
# and never looked at Exoscale at all. So an upstream change in vpc, ipam or the
# Exoscale API failed the local task and passed CI — while the README claimed a
# committed baseline turns an untriaged operation into a CI failure.
#
# Worse, the CI job also *rewrote* the baseline with its narrower product list.
# The first scheduled run would have dropped every vpc and ipam entry and opened
# a pull request that looked like upstream drift and was a configuration skew.
#
# Both callers now run this file. Divergence is no longer possible by editing one
# of them.
#
# Usage: tools/drift/gate.sh check|update [feint-binary]
# Exit:  0 nothing moved, 1 error, 2 drift detected.
set -uo pipefail

MODE="${1:-check}"
FEINT="${2:-./feint}"

SCALEWAY_SDK="${FEINT_SDK_SCALEWAY:-.upstream/scaleway-sdk-go}"
OUTSCALE_SDK="${FEINT_SDK_OUTSCALE:-.upstream/osc-sdk-go}"

# The products the emulator has started. The gate covers these and says so:
# asking it to account for all 1700 upstream operations would fail forever and
# teach everyone to ignore it. Adding a product here is what puts it under the
# gate — in both callers at once.
# account joined on 2026-08-27 with #372: `data "scaleway_account_project"` is
# the first call every third-party VPC stack makes, so the product stopped being
# one nobody reaches. Adding it here is what puts its twelve operations in front
# of the triage — two served, ten declined with a reason.
SCALEWAY_PRODUCTS="${FEINT_PRODUCTS:-instance,vpc,ipam,iam,marketplace,block,lb,vpcgw,account}"

[ -x "$FEINT" ] || { echo "no feint binary at $FEINT (build it: mise run build)" >&2; exit 1; }

case "$MODE" in
  check)
    # Every provider runs even when an earlier one drifts, so one report carries
    # everything that moved rather than one provider at a time. The exit code is
    # the worst of them.
    #
    # --artefact on every call, because the baseline only watches the upstream
    # side. #298 measured the other side: a decline reason rewritten in a pack
    # took four days to reach coverage/scaleway-coverage.json, while this gate
    # passed (names and statuses, never reasons) and docs:check passed (the
    # README regenerates from the same stale artefact) — two gates agreeing
    # with each other while both disagreed with the code. The flag compares the
    # committed artefact with what the pack declares today and exits 2 on any
    # skew; TestTheCommittedArtefactCarriesWhatThePacksDeclare fails on the
    # same skew, and tools/falsify/specs/artefact-reasons.json proves it bites.
    status=0
    # --document on top of --sdk, and it is not --contract: the second witness
    # (#622). The baseline watches upstream and --artefact watches the copy, but
    # both read the *scan*, so neither can see an operation the portal publishes
    # and scaleway-sdk-go never wrapped. Six of those existed, and `unknown: 0`
    # was exact over a set that did not contain them.
    #
    # --document rather than --contract because --contract also regroups the
    # report onto the document's tags, which would move every published table as
    # a side effect of adding a check. Outscale below does want that regrouping;
    # Scaleway must not have it.
    "$FEINT" coverage --sdk "$SCALEWAY_SDK" --products "$SCALEWAY_PRODUCTS" \
      --baseline coverage/scaleway-baseline.json \
      --artefact coverage/scaleway-coverage.json \
      --document contracts/scaleway.json \
      --contract-only coverage/contract-only.json || status=$?
    # --contract on top of --sdk: the SDK lists the operations, the contract
    # carries the tags their document files them under, which oapi-codegen
    # flattens away. Without it every artefact row collapses back into `osc`.
    "$FEINT" coverage --provider outscale --sdk "$OUTSCALE_SDK" \
      --contract contracts/outscale.json \
      --baseline coverage/outscale-baseline.json \
      --artefact coverage/outscale-coverage.json || status=$?
    "$FEINT" coverage --provider exoscale --contract contracts/exoscale.json \
      --baseline coverage/exoscale-baseline.json \
      --artefact coverage/exoscale-coverage.json || status=$?
    exit $status
    ;;
  update)
    set -e
    "$FEINT" coverage --sdk "$SCALEWAY_SDK" --products "$SCALEWAY_PRODUCTS" \
      --baseline coverage/scaleway-baseline.json --write-baseline
    "$FEINT" coverage --sdk "$SCALEWAY_SDK" --products "$SCALEWAY_PRODUCTS" \
      --format json > coverage/scaleway-coverage.json
    "$FEINT" coverage --provider outscale --sdk "$OUTSCALE_SDK" \
      --contract contracts/outscale.json \
      --baseline coverage/outscale-baseline.json --write-baseline
    "$FEINT" coverage --provider outscale --sdk "$OUTSCALE_SDK" \
      --contract contracts/outscale.json \
      --format json > coverage/outscale-coverage.json
    "$FEINT" coverage --provider exoscale --contract contracts/exoscale.json \
      --baseline coverage/exoscale-baseline.json --write-baseline
    "$FEINT" coverage --provider exoscale --contract contracts/exoscale.json \
      --format json > coverage/exoscale-coverage.json
    # The README's coverage tables are generated from the artefacts just written,
    # so they cannot be stale by the time the pull request is opened.
    "$FEINT" docs
    ;;
  *)
    echo "usage: $0 check|update [feint-binary]" >&2
    exit 1
    ;;
esac
