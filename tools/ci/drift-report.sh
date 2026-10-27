#!/usr/bin/env bash
# The drift issue says what the scan found, and closes when there is nothing (#800).
#
# The report was posted whenever `coverage/` OR `contracts/` differed, and it
# always said the same sentence: "The nightly scan found operations upstream
# that nobody has triaged." Those are not the same question.
#
# On 2026-10-27 it posted that sentence and then listed, three times:
#
#   provider scaleway: nothing left to triage
#   provider outscale: nothing left to triage
#   provider exoscale: nothing left to triage
#
# because what had moved was two counters — ImportSnapshotFromObjectStorage went
# from declined to served in #821 and the baseline had not followed the merge.
# A refreshed artefact is a pull request to merge; an undecided operation is a
# decision to make. Reporting the first as the second is how #800 reached
# twenty-one comments, and a notification that arrives whatever happened teaches
# its reader to skip it — the failure #795 was about, in the other mechanism.
#
# So: comment only when something is undecided, and close the issue when the
# last undecided thing is gone. That is the shape night-report.sh already has,
# and the one drift.yml's own comment asks for: "one issue, updated, never a new
# one per night".
#
# The logic lives here rather than in a run: block, for the reason
# night-report.sh states — a run: block cannot be executed outside GitHub
# Actions, and this repository has paid for CI fixes described in comments and
# never executed. The controls on this file:
#
#   - tools/ci/drift_report_test.go drives it with a stubbed `gh`;
#   - tools/falsify/specs/the-drift-report-says-what-it-found.json replays those
#     tests with each decision neutralised.
#
# Usage: drift-report.sh [--apply] <undecided> <triage-file>
#
#   undecided     the outcome of the gate step: "failure" means the scan found
#                 operations nobody has triaged, anything else means it did not
#   triage-file   the triage view, or a path that does not exist
#
# Without --apply it prints the verdict and the body it would post, and writes
# nothing.
set -euo pipefail

apply=false
if [ "${1:-}" = "--apply" ]; then
  apply=true
  shift
fi

undecided="${1:?usage: drift-report.sh [--apply] <undecided> <triage-file>}"
triage="${2:?usage: drift-report.sh [--apply] <undecided> <triage-file>}"
repo="${GITHUB_REPOSITORY:-stephrobert/feint}"
run_url="${FEINT_RUN_URL:-}"
label="drift"
title="Upstream API surface moved"

existing="$(gh issue list --repo "${repo}" --state open --label "${label}" \
              --json number --jq '.[0].number' 2>/dev/null || true)"

# NOTHING UNDECIDED. The scan ran and found no operation waiting for a decision.
# Whatever else moved is an artefact the pull request carries, and a pull request
# is visible without a notification. If the issue is open, this is what closes
# it: the mechanism that opens on a finding must close on its absence, or the
# issue outlives its cause and reads as a standing problem.
if [ "${undecided}" != "failure" ]; then
  if [ -z "${existing}" ]; then
    echo "verdict: nothing undecided, no issue open, nothing to say"
    exit 0
  fi
  echo "verdict: nothing undecided, closing #${existing}"
  body_file="$(mktemp)"
  trap 'rm -f "${body_file}"' EXIT
  {
    echo "Every operation upstream is triaged: implemented, or declined with a reason."
    echo
    echo "Closed by the scan itself. It reopens the day something upstream is"
    echo "undecided again — a refreshed baseline is a pull request to merge, not a"
    echo "decision to make, and it no longer comments here."
    [ -n "${run_url}" ] && { echo; echo "The run that read it: ${run_url}"; }
  } > "${body_file}"
  if [ "${apply}" = true ]; then
    gh issue close "${existing}" --repo "${repo}" --comment "$(cat "${body_file}")"
  else
    echo "--- the comment it would close with ---"
    cat "${body_file}"
  fi
  exit 0
fi

# SOMETHING IS UNDECIDED. This is the message that earns a notification.
echo "verdict: undecided operations found, reporting"
body_file="$(mktemp)"
trap 'rm -f "${body_file}"' EXIT
{
  echo "The nightly scan found operations upstream that nobody has triaged."
  echo
  echo "Each one is a decision, not a task: **implement it**, or **add it to the"
  echo "pack's \`Declined()\` with the reason**. \"Not done yet\" and \"out of scope\""
  echo "are different answers and only the second belongs in Declined."
  echo
  echo "The refreshed baseline is on the branch \`chore/upstream-drift\`. Opening a"
  echo "pull request from it is a human step: this repository withholds that"
  echo "permission from Actions on purpose. Merging it without deciding is what"
  echo "makes this report meaningless — the operations stop being reported and"
  echo "start being forgotten."
  echo
  echo "<details><summary>What is undecided, grouped by subject</summary>"
  echo
  echo '```'
  if [ -s "${triage}" ]; then
    cat "${triage}"
  else
    echo "The triage view did not make it to this run. The scan output is in the run log."
  fi
  echo '```'
  echo
  echo "</details>"
  [ -n "${run_url}" ] && { echo; echo "[Run](${run_url}) · scanned $(date -u +%Y-%m-%d)"; }
} > "${body_file}"

if [ "${apply}" = false ]; then
  echo "--- the body it would post ---"
  cat "${body_file}"
  exit 0
fi

if [ -n "${existing}" ]; then
  gh issue comment "${existing}" --repo "${repo}" --body-file "${body_file}"
else
  gh label create "${label}" --repo "${repo}" --color FBCA04 \
    --description "The upstream API surface moved and needs triage" 2>/dev/null || true
  gh issue create --repo "${repo}" --title "${title}" --label "${label}" \
    --body-file "${body_file}"
fi
