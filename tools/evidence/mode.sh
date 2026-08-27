#!/usr/bin/env bash
# Which runtime the evidence record's second leg runs under, said out loud.
#
# `mise run evidence:update` drives two conformance runs: leg 1 with machines
# off, because the probe walks every Create* route and would boot a container
# per route, and leg 2 with machines on, because the dataplane axis only exists
# there. Leg 2 used to open with
#
#     FEINT_VM="${FEINT_EVIDENCE_VM:-incus}"
#
# and that single line is #574's second half. An operator who exported
# FEINT_VM=incus-ovn got the bridge, was told nothing, and read a verdict about
# a mode nobody had asked for. It manufactured two successive false
# attributions while an Exoscale defect was being diagnosed — first "the
# population", then "the branch" — and cost three 1300-second passes to unmake.
#
# The rule this file is: an instrument that answers under a mode may not choose
# that mode silently. So the caller's FEINT_VM wins, FEINT_EVIDENCE_VM stays as
# the way to steer the leg without touching leg 1, a disagreement between the
# two is refused by name rather than arbitrated, and the mode is announced
# before anything starts.
#
# Prints the resolved mode on stdout, the announcement on stderr, and exits 1
# with the reason when the two variables cannot both be honoured.
#
# Driven by tools/evidence/mode_test.go, which is where the four outcomes are
# asserted; a green run of this script proves nothing on its own.
set -euo pipefail

vm="${FEINT_VM:-}"
evidence_vm="${FEINT_EVIDENCE_VM:-}"

if [ -n "$vm" ] && [ -n "$evidence_vm" ] && [ "$vm" != "$evidence_vm" ]; then
  echo "FEINT_VM=$vm and FEINT_EVIDENCE_VM=$evidence_vm name two different runtimes for the same leg." >&2
  echo "Picking one of them is what made this task report a verdict under a mode nobody asked for (#574)." >&2
  echo "Export one, or set them to the same value." >&2
  exit 1
fi

if [ -n "$vm" ]; then
  mode="$vm"
  source="FEINT_VM, exported by the caller"
elif [ -n "$evidence_vm" ]; then
  mode="$evidence_vm"
  source="FEINT_EVIDENCE_VM"
else
  mode="incus"
  source="this task's default"
fi

# Refused by name rather than honoured: with machines off the runtime leg is
# leg 1 run twice, so it would earn no dataplane axis and quietly narrow the
# record. The refusal says which leg already does that.
if [ "$mode" = "off" ]; then
  echo "the runtime leg was asked for --vm off ($source), which is leg 1 run a second time:" >&2
  echo "it can earn no dataplane axis, and the record would narrow without anyone asking." >&2
  echo "Ask for a runtime (incus, incus-ovn, incus-vm), or run leg 1 alone." >&2
  exit 1
fi

echo "   the runtime leg runs under --vm $mode ($source)" >&2
printf '%s\n' "$mode"
