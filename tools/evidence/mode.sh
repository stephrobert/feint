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
# The resolution itself moved to tools/runtime-mode.sh when the stack gate was
# found carrying the same line one directory over (#504): what varies between
# the two callers — the subject, the knob, the default, and what `off` would
# cost — is passed, and what does not vary is shared. This file stays because
# the leg's four facts are the leg's, not the resolver's, and because
# tools/evidence/mode_test.go drives it: those tests were written against the
# behaviour, so they are what proves the move changed none of it.
#
# Prints the resolved mode on stdout, the announcement on stderr, and exits 1
# with the reason when the two variables cannot both be honoured.
set -euo pipefail

exec "$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)/runtime-mode.sh" \
	"the runtime leg" \
	FEINT_EVIDENCE_VM \
	incus \
	"That is leg 1 run a second time: it can earn no dataplane axis, and the
record would narrow without anyone asking. Run leg 1 alone if that is what
you meant."
