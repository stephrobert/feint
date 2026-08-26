#!/usr/bin/env bash
# The Exoscale Terraform suite is suspended: no Terraform for Exoscale.
#
# This is the maintainer's decision of 2026-08-26, taken on #525's measurement,
# and this doorstep is its enforcement here. It refuses rather than skips —
# a suite someone invokes by name that answered green while measuring nothing
# would be the exact verdict measurement-integrity exists to distrust.
#
# The history, dated rather than erased. The published exoscale/exoscale
# provider builds two clients and honours EXOSCALE_API_ENDPOINT for one of
# them, so an apply or destroy *splits* between the emulator and a paying
# account (docs/limits.md, upstream exoscale/terraform-provider-exoscale#573).
# This script used to drive the pinned four-line fork instead, and what that
# proved is real and stays written down: sixteen resources, block storage and
# private networking included, empty second plan, clean destroy (2026-08-24).
# Then #525 measured what every path around the fork costs: a `feint down` on
# the same stack, run without the fork's dev_overrides, resolved the published
# 0.70.0 and sent five signed requests to api-ch-*.exoscale.com, stopped only
# by the pack's fake credential pair outranking the shell's. A client this
# project patched was never the official client anyway, so the fork could not
# count towards conformance — it could only keep this path warm, and #525
# priced what keeping it warm costs.
#
# What drives the Exoscale pack instead, all of it in `mise run conformance`:
# exo-cli.sh, network.sh, ssh.sh and zones.sh — the official exo CLI, which
# honours EXOSCALE_API_ENDPOINT for everything. `feint up`/`feint down` refuse
# `iac.engine: terraform` for Exoscale at the same doorstep
# (internal/cli/up.go, the pack's VetoEngine), and the declaration schema
# refuses FEINT_EXOSCALE_ALLOW_TERRAFORM in `emulator.env` by name.
#
# Terraform returns here the day upstream #573 is fixed in the published
# provider — that is the condition, and the only one.
set -euo pipefail

cat >&2 <<'MSG'
SUSPENDED: no Terraform for Exoscale until upstream #573 is fixed.

  The published provider honours EXOSCALE_API_ENDPOINT for one of the two
  clients it builds, so an apply or destroy splits between the emulator and a
  paying account. #525 measured five signed requests leaving for
  api-ch-*.exoscale.com from a `feint down` on the example stack; since then
  no Terraform, fork included, is pointed at this pack.

  The exo CLI drives Exoscale end to end instead:

    mise run conformance:exo         # exo-cli.sh against the running emulator
    mise run conformance             # the whole population, exo legs included

  Condition of return: exoscale/terraform-provider-exoscale#573 fixed in the
  published provider. The header of this script carries the history.
MSG
exit 1
