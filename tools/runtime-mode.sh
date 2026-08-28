#!/usr/bin/env bash
# Which machine runtime a task answers under, resolved once and said out loud.
#
# THE DEFECT THIS IS, AND IT HAS NOW HAPPENED TWICE.
#
# `mise run evidence:update` used to open its second leg with
#
#     FEINT_VM="${FEINT_EVIDENCE_VM:-incus}"
#
# so an operator who exported FEINT_VM=incus-ovn got the bridge, was told
# nothing, and read a verdict about a mode nobody had asked for. It manufactured
# two successive false attributions while an Exoscale defect was being diagnosed
# — first "the population", then "the branch" — and cost three 1300-second
# passes to unmake (#574).
#
# `tools/conformance/functional.sh` carried the same line one directory over:
#
#     RUNTIME="${FEINT_FUNCTIONAL_RUNTIME:-incus-ovn}"
#
# `FEINT_VM=incus mise run conformance:functional` answered under OVN and said
# nothing about it — and the mode is the one variable this gate's whole subject
# turns on, since isolation exists in one of the two (#504).
#
# So the resolution lives here, once, and both callers pass what differs. That
# is the repository's own rule about what varies being a field: a control copied
# into two scripts is a control the third forgets, and the third is the one that
# will be written next.
#
# THE RULE. An instrument that answers under a mode may not choose that mode
# silently. The caller's FEINT_VM wins; the task's own knob steers it without
# touching anything else; a disagreement between the two is refused by name
# rather than arbitrated; `off` is refused with the caller's own reason; and the
# mode is announced, with its provenance, before anything is spent.
#
# Prints the resolved mode on stdout, the announcement on stderr, and exits 1
# with the reason when the two variables cannot both be honoured.
#
#   usage: tools/runtime-mode.sh <subject> <knob variable> <default> <off reason>
#
# Driven by tools/evidence/mode_test.go and tools/conformance/mode_test.go,
# which is where the outcomes are asserted; a green run of this script proves
# nothing on its own.
set -euo pipefail

if [ "$#" -ne 4 ]; then
	echo "usage: tools/runtime-mode.sh <subject> <knob variable> <default> <off reason>" >&2
	echo "  A resolver called with the wrong arity must not guess: the mode it picked" >&2
	echo "  would be nobody's, which is the defect it exists to remove." >&2
	exit 2
fi

subject="$1"
knob="$2"
default="$3"
off_reason="$4"

vm="${FEINT_VM:-}"
# Indirect rather than a case list: the knob's name is the caller's, and this
# file knows no task.
knob_value="${!knob:-}"

if [ -n "$vm" ] && [ -n "$knob_value" ] && [ "$vm" != "$knob_value" ]; then
	echo "FEINT_VM=$vm and $knob=$knob_value name two different runtimes for $subject." >&2
	echo "Picking one of them is what made a task report a verdict under a mode nobody asked for (#574)." >&2
	echo "Export one, or set them to the same value." >&2
	exit 1
fi

if [ -n "$vm" ]; then
	mode="$vm"
	source="FEINT_VM, exported by the caller"
elif [ -n "$knob_value" ]; then
	mode="$knob_value"
	source="$knob"
else
	mode="$default"
	source="this task's default"
fi

# Refused by name rather than honoured. What `off` costs is the caller's to
# say — it is the one fact about the mode that is not the same in two places —
# so the sentence arrives as an argument.
if [ "$mode" = "off" ]; then
	echo "$subject was asked for --vm off ($source)." >&2
	printf '%s\n' "$off_reason" >&2
	echo "Ask for a runtime (incus, incus-ovn, incus-vm)." >&2
	exit 1
fi

echo "   $subject runs under --vm $mode ($source)" >&2
printf '%s\n' "$mode"
