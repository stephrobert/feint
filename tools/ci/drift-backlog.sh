#!/usr/bin/env bash
# The undecided backlog: what is waiting for a decision, whatever the baseline
# holds (#886).
#
# The drift gate answers a narrower question — did an operation appear upstream
# that is not in the committed baseline. The baseline *records* the unknown
# operations, so the first refresh after an arrival writes the arrival down and
# every later night finds nothing new while the operation is still undecided.
# Measured: the two baremetal commitment operations arrived in the 2026-10-28
# refresh (#827) and were undecided for fourteen green nights (#875).
#
# `--fail-on-unknown` exits 2 when an upstream operation is neither served nor
# declined, and it does that whatever the baseline holds. That is the question
# nobody was asking.
#
# WHY THIS IS A FILE AND NOT A `run:` BLOCK
#
# It was a run: block, and it failed on its first real run. GitHub executes
# `run:` with `bash -e {0}`, so the first probe to answer 2 — which is the
# normal, expected answer, the one this step exists to produce — killed the step
# before the line that reads the code. `set -uo pipefail` at the top of the
# block does not undo the -e the shell was started with, and the Go test that
# covered this change executed the OTHER step's shell.
#
# So the arithmetic lives here, where tools/ci/drift_backlog_test.go runs it
# with a stubbed feint, under `bash -e`, and asserts that an exit of 2 still
# produces a verdict. That is the same reason tools/ci/drift-report.sh and
# tools/ci/night-report.sh are files: a run: block cannot be executed outside
# GitHub Actions, and this repository has paid for CI fixes described in
# comments and never executed.
#
# Usage: drift-backlog.sh <feint-binary>
#
# Writes `verdict=` and `health=` to $GITHUB_OUTPUT when it is set, and a
# readable account to stdout. Three verdicts, never two:
#
#   verdict=failure  something is undecided, which earns the issue;
#   verdict=success  nothing is, which is what closes it;
#   verdict=error    a source could not be measured, which closes nothing. An
#                    absence of findings from a scan that did not run is not a
#                    finding of absence.
#
# Exit 0 unless its own arguments are wrong: the verdict is the product, not the
# exit code. A step that failed here would skip `propose`, and a pull request
# withheld because one probe broke would cost the measurement that worked.
set -uo pipefail

FEINT="${1:?usage: drift-backlog.sh <feint-binary>}"
HERE="$(cd "$(dirname "$0")" && pwd)"

SCALEWAY_SDK="${FEINT_SDK_SCALEWAY:-.upstream/scaleway-sdk-go}"
OUTSCALE_SDK="${FEINT_SDK_OUTSCALE:-.upstream/osc-sdk-go}"

undecided=0
unmeasured=0

# The status is collected with `|| status=$?` rather than read from $? on the
# next line. Under `set -e` the second form never runs, and under no -e the two
# are the same: one of them is right in both worlds, and this is it.
probe() {
  local name="$1"
  shift
  local status=0
  echo "::group::backlog: ${name}"
  "$@" --fail-on-unknown || status=$?
  case "${status}" in
    0) echo "${name}: nothing undecided" ;;
    2) echo "${name}: operations are waiting for a decision"
       undecided=1 ;;
    *) echo "${name}: could not be measured (exit ${status})"
       unmeasured=1 ;;
  esac
  echo "::endgroup::"
}

# The Scaleway products come from the file that decides what is under the gate,
# never from a second copy of the list: the triage view carried one and covered
# three products while the gate scanned eleven (#886).
probe scaleway "${FEINT}" coverage --sdk "${SCALEWAY_SDK}" \
  --products "$("${HERE}/../drift/gate.sh" products)"
probe outscale "${FEINT}" coverage --provider outscale --sdk "${OUTSCALE_SDK}" \
  --contract contracts/outscale.json
probe exoscale "${FEINT}" coverage --provider exoscale \
  --contract contracts/exoscale.json

say() {
  echo "$1"
  [ -n "${GITHUB_STEP_SUMMARY:-}" ] && printf '%s\n' "$1" >> "${GITHUB_STEP_SUMMARY}"
  return 0
}

emit() {
  [ -n "${GITHUB_OUTPUT:-}" ] && printf '%s\n' "$1" >> "${GITHUB_OUTPUT}"
  return 0
}

if [ "${unmeasured}" = 1 ]; then
  emit "verdict=error"
  emit "health=unmeasured"
  say "A source could not be measured, so this night says nothing about the backlog."
  exit 0
fi

emit "health=ok"
if [ "${undecided}" = 1 ]; then
  emit "verdict=failure"
  say "## Operations upstream are waiting for a decision"
else
  emit "verdict=success"
  say "Every operation upstream is served or declined with a reason."
fi
