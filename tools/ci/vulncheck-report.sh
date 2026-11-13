#!/usr/bin/env bash
# The govulncheck issue says what the nightly scan found, and closes when the
# pin has moved past it.
#
# It is the drift issue's shape, for the other half of #865: the pull request
# carries the mechanical part — a pin moved to a newer patch — and the issue
# carries the signal a human has to see, because a pull request that cannot be
# opened by Actions (this repository withholds that permission on purpose) is
# otherwise a pushed branch nobody opens. When the fixed version is in the pin,
# a later clean night closes the issue: the mechanism that opens on a finding
# must close on its absence, or the issue outlives its cause and reads as a
# standing problem.
#
# The logic lives here rather than in a run: block, for the reason
# night-report.sh states — a run: block cannot be executed outside GitHub
# Actions, and this repository has paid for CI fixes described in comments and
# never executed. The controls on this file:
#
#   - tools/ci/vulncheck_report_test.go drives it with a stubbed `gh`;
#   - tools/falsify/specs/the-vulncheck-report-says-what-it-found.json replays
#     those tests with each decision neutralised.
#
# Usage: vulncheck-report.sh [--apply] <verdict> <advisory> <found> <fixed> <branch> <pr-url>
#
#   verdict   advisory | clean | inconclusive
#   advisory  the GO id, e.g. GO-2026-6859 (empty unless advisory)
#   found     the pinned Go the advisory was found in (empty unless advisory)
#   fixed     the Go version the advisory is fixed in (empty unless advisory)
#   branch    the branch carrying the moved pin (empty unless advisory)
#   pr-url    the pull request, when one was opened (empty when the repository's
#             policy refused it, or when the branch was only pushed)
#
# Without --apply it prints the verdict and the body it would post, and writes
# nothing.
set -euo pipefail

apply=false
if [ "${1:-}" = "--apply" ]; then
  apply=true
  shift
fi

# Six arguments, all six, even when some are empty: on a clean night the
# advisory, the versions and the branch are all empty, and an empty argument
# is not a missing one. The count is the check, not the content.
if [ "$#" -ne 6 ]; then
  echo "usage: vulncheck-report.sh [--apply] <verdict> <advisory> <found> <fixed> <branch> <pr-url>" >&2
  exit 1
fi

verdict="$1"
advisory="$2"
found="$3"
fixed="$4"
branch="$5"
pr_url="$6"
repo="${GITHUB_REPOSITORY:-stephrobert/feint}"
run_url="${FEINT_RUN_URL:-}"
label="govulncheck"

# THE SCAN COULD NOT SAY. Neither branch below applies: there is nothing to
# report, and nothing has been measured that would justify closing anything.
# Exits quiet rather than green, because the night job is what reports a scan
# that could not conclude (#705) and it reads the run, not this script.
if [ "${verdict}" != advisory ] && [ "${verdict}" != clean ]; then
  echo "verdict: the scan did not conclude (${verdict}); closing nothing, saying nothing"
  exit 0
fi

# NOTHING TOUCHES THE PIN. The scan ran and found no advisory in it. Close
# every issue this mechanism opened — matched on the title prefix it composes,
# not on the label alone, because the label is not an identifier: anything
# about this mechanism carries it.
if [ "${verdict}" != "advisory" ]; then
  open="$(gh issue list --repo "${repo}" --state open --label "${label}" \
          --json number,title 2>/dev/null \
          | jq -r '[.[] | select(.title | startswith("govulncheck: ")) | .number] | join(" ")' \
          2>/dev/null || true)"
  if [ -z "${open}" ]; then
    echo "verdict: nothing touches the pin, no issue open, nothing to say"
    exit 0
  fi
  body_file="$(mktemp)"
  trap 'rm -f "${body_file}"' EXIT
  {
    echo "The nightly scan concluded clean: no advisory touches the pinned Go."
    echo
    echo "Closed by the scan itself. It reopens the day an advisory touches the"
    echo "pin again — a moved pin is a pull request to merge, not a decision to"
    echo "make, and it no longer comments here."
    [ -n "${run_url}" ] && { echo; echo "The run that read it: ${run_url}"; }
  } > "${body_file}"
  for number in ${open}; do
    echo "verdict: nothing touches the pin, closing #${number}"
    if [ "${apply}" = true ]; then
      gh issue close "${number}" --repo "${repo}" --comment "$(cat "${body_file}")"
    else
      echo "--- the comment it would close with ---"
      cat "${body_file}"
    fi
  done
  exit 0
fi

# AN ADVISORY TOUCHES THE PIN. This is the message that earns a notification.
title="govulncheck: ${advisory} touches the pinned Go ${found}"
existing="$(gh issue list --repo "${repo}" --state open --label "${label}" \
              --json number,title 2>/dev/null \
              | jq -r --arg t "${title}" '[.[] | select(.title == $t)] | .[0].number // empty' \
              2>/dev/null || true)"

echo "verdict: ${advisory} touches the pinned Go, reporting"
body_file="$(mktemp)"
trap 'rm -f "${body_file}"' EXIT
{
  echo "The nightly scan found ${advisory} in the pinned Go ${found}; it is fixed in ${fixed}."
  echo
  echo "The moved pin is on the branch \`${branch}\`."
  if [ -n "${pr_url}" ]; then
    echo "The pull request is ${pr_url}."
  else
    echo "No pull request was opened from it: this repository does not let Actions"
    echo "open pull requests, which is a deliberate hardening choice. Open it by hand:"
    echo "https://github.com/${repo}/compare/main...${branch}?expand=1"
  fi
  echo
  echo "Merging it moves all nineteen pins together — mise.toml and every workflow"
  echo "that names a Go version — so the next night scans with the fixed toolchain."
  echo "Its CI must be green: a pin-bump pull request that is born red is a gate"
  echo "people stop reading."
  [ -n "${run_url}" ] && { echo; echo "[Run](${run_url}) · scanned $(date -u +%Y-%m-%d)"; }
} > "${body_file}"

if [ "${apply}" = false ]; then
  echo "--- the title it would open with ---"
  echo "${title}"
  echo "--- the body it would post ---"
  cat "${body_file}"
  exit 0
fi

if [ -n "${existing}" ]; then
  gh issue comment "${existing}" --repo "${repo}" --body-file "${body_file}"
else
  gh label create "${label}" --repo "${repo}" --color 1D76DB \
    --description "A Go advisory touches the pinned toolchain" 2>/dev/null || true
  gh issue create --repo "${repo}" --title "${title}" --label "${label}" \
    --body-file "${body_file}"
fi
