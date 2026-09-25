#!/usr/bin/env bash
# A met promotion criterion says so where somebody reads it (#125).
#
# runtime-proof.yml counts consecutive green scheduled runs and, at fourteen,
# writes into $GITHUB_STEP_SUMMARY: "The criterion is met. Move this workflow
# onto pull_request and close #125."
#
# It did exactly that on 2026-09-21. Nobody read it. The streak broke the next
# night — #740 armed `guard.sh verification` and a real broken claim reddened
# four nights running — and the moment passed unnoticed.
#
# The failure mode is the one the workflow's own report job already names, one
# sentence above this file's reason for existing: a job log and a step summary
# are "the two places nobody opens without already knowing there is a problem".
# That lesson was applied to red nights (#502) and not to a met criterion, so
# the half that asks for action was the half staying silent.
#
# So this posts a comment on the issue instead. Once — a comment carrying the
# marker below already on the issue means it has been said, and saying it again
# every night is how a notification becomes noise somebody mutes.
#
# The logic lives here rather than in a run: block, for the reason
# night-report.sh states: a run: block cannot be executed outside GitHub
# Actions, and this repository has paid for CI fixes described in comments and
# never executed. The controls on this file:
#
#   - tools/ci/streak_report_test.go drives it with a stubbed `gh`;
#   - tools/falsify/specs/a-met-criterion-is-announced.json replays those tests
#     with each decision neutralised.
#
# Usage: streak-report.sh [--apply] <streak> <target> <issue>
#
# Without --apply it prints what it would do and writes nothing.
set -euo pipefail

apply=false
if [ "${1:-}" = "--apply" ]; then
  apply=true
  shift
fi

streak="${1:?usage: streak-report.sh [--apply] <streak> <target> <issue>}"
target="${2:?usage: streak-report.sh [--apply] <streak> <target> <issue>}"
issue="${3:?usage: streak-report.sh [--apply] <streak> <target> <issue>}"
repo="${GITHUB_REPOSITORY:-stephrobert/feint}"
run_url="${FEINT_RUN_URL:-}"

# The marker is what makes this idempotent. It is invisible in the rendered
# comment and unique to this announcement, so a maintainer's own prose about
# the streak never counts as one.
marker="<!-- feint:streak-criterion-met -->"

if [ "$streak" -lt "$target" ]; then
  echo "verdict: not met (${streak}/${target}), nothing to announce"
  exit 0
fi

# Said once. `gh issue view --json comments` answers every comment, and the
# marker is searched in their bodies rather than in a title or an author: a
# reply quoting this comment must not count as the comment itself, so the
# marker sits on its own line and nothing else writes it.
already=false
if comments="$(gh issue view "${issue}" --repo "${repo}" --json comments \
                 --jq '.comments[].body' 2>/dev/null)"; then
  if printf '%s' "${comments}" | grep -qF "${marker}"; then
    already=true
  fi
fi

if [ "$already" = true ]; then
  echo "verdict: met (${streak}/${target}), already announced on #${issue}"
  exit 0
fi

body_file="$(mktemp)"
trap 'rm -f "${body_file}"' EXIT
{
  echo "${marker}"
  echo
  echo "**The promotion criterion is met: ${streak} consecutive green scheduled runs, target ${target}.**"
  echo
  echo "This is the number this issue asks for, counted from the Actions history by the"
  echo "\`Consecutive green scheduled runs\` job rather than from anybody's memory. Nothing"
  echo "else has to be decided: the remaining work is to move \`runtime-proof.yml\` onto"
  echo "\`pull_request\` and close this issue."
  echo
  if [ -n "${run_url}" ]; then
    echo "The run that counted it: ${run_url}"
    echo
  fi
  echo "Posted automatically, once. The criterion was met before — on 2026-09-21, with"
  echo "fourteen greens — and the only trace was a step summary, which is why this comment"
  echo "exists at all."
} > "${body_file}"

echo "verdict: met (${streak}/${target}), announcing on #${issue}"
if [ "$apply" = true ]; then
  gh issue comment "${issue}" --repo "${repo}" --body-file "${body_file}"
else
  echo "--- the comment it would post ---"
  cat "${body_file}"
fi
