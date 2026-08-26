#!/usr/bin/env bash
# A red scheduled night opens or updates one issue; a green one closes it (#502).
#
# runtime-proof.yml was red ten scheduled nights out of twelve (#501) and the
# only trace was a job log — the one place nobody opens without already knowing
# there is a problem. This script turns a scheduled run's outcome into the one
# notification a maintainer actually triages, following the rule drift.yml's
# report job already carries: one issue, updated, never a new one per night. An
# issue per night teaches everyone to close them unread, and the silence comes
# back in another shape.
#
# The logic lives here, not in a run: block, because a run: block cannot be
# executed outside GitHub Actions and this repository has already paid for CI
# fixes described in comments and never executed (CLAUDE.md, "Un commentaire
# n'est pas un contrôle"). The controls on this file:
#
#   - tools/ci/night_report_test.go drives it with a stubbed `gh` over
#     field-trimmed payloads of real runs of runtime-proof.yml — a red night
#     (32924409224), a green one (32441329459) and a cancellation with no
#     failing step (31716251612);
#   - tools/falsify/specs/a-red-night-opens-an-issue.json replays those tests
#     with each decision neutralised, so a mutation that silences the report
#     makes a named test fail.
#
# Usage: night-report.sh [--apply] <run-id>
#
# Without --apply it prints what it would do — verdict, plan, title, body — and
# writes nothing. With --apply it also performs the gh writes. The reusable
# workflow .github/workflows/night-report.yml is the only caller that passes
# --apply; a human proving the script points it at any past run and reads.
#
# Environment: GITHUB_REPOSITORY (owner/repo, required), GH_TOKEN (for gh),
# NIGHT_STREAK_TARGET (optional; empty hides the target sentence).
set -euo pipefail

usage() {
  echo "usage: night-report.sh [--apply] <run-id>" >&2
  exit 64
}

apply=0
if [ "${1:-}" = "--apply" ]; then
  apply=1
  shift
fi
[ "$#" -eq 1 ] || usage
run_id="$1"

repo="${GITHUB_REPOSITORY:?GITHUB_REPOSITORY must hold owner/repo}"
target="${NIGHT_STREAK_TARGET:-}"
label="scheduled-red"

run_json="$(gh api "repos/${repo}/actions/runs/${run_id}")"
wf_name="$(jq -r '.name' <<<"${run_json}")"
wf_file="$(jq -r '.path | split("/") | last' <<<"${run_json}")"
run_url="$(jq -r '.html_url' <<<"${run_json}")"
run_created="$(jq -r '.created_at' <<<"${run_json}")"
run_event="$(jq -r '.event' <<<"${run_json}")"
run_date="${run_created%%T*}"

# The run's jobs, completed ones only: when this runs inside the run it judges
# (needs: on the measuring jobs), its own job is still in_progress and must not
# sit in its own verdict.
jobs_json="$(gh api --paginate "repos/${repo}/actions/runs/${run_id}/jobs?per_page=100" \
  | jq -s '[.[].jobs[] | select(.status == "completed")]')"

# Three outcomes, never two (measurement-integrity): a run with no completed
# job is not green and not red — it is a run this script cannot judge, and
# saying so beats inventing a verdict.
if [ "$(jq 'length' <<<"${jobs_json}")" -eq 0 ]; then
  echo "FAIL: run ${run_id} has no completed job; nothing to judge." >&2
  exit 1
fi

# One finding per job that did not succeed. The first failing step of a job
# carries the cause: the steps after it run under if: always() and fail
# downstream of it (measured on 31716251612, where "Report what was exercised"
# failed because the cancellation had killed the emulator it reads). A job with
# no failing step at all — cancelled, timed out, runner lost — is the run's own
# infrastructure, and naming a suite for it would invent a cause.
findings="$(jq '
  def failed_steps: [.steps[] | select(.conclusion == "failure")];
  [.[]
   | select(.conclusion != "success" and .conclusion != "skipped" and .conclusion != "neutral")
   | if .conclusion == "failure" and (failed_steps | length) > 0
     then {kind: "measured", job: .name, step: (failed_steps | first).name}
     else {kind: "infra", job: .name, conclusion: .conclusion}
     end
  ]' <<<"${jobs_json}")"

verdict=green
if [ "$(jq 'length' <<<"${findings}")" -gt 0 ]; then
  verdict=red
fi

# The scheduled history strictly before this run, newest first (the API's own
# order). Scheduled runs only, twice: the query says so, and the filter says it
# again so a caller (or a test fixture) that hands back more than asked cannot
# widen the population — the streak counts nights, not button presses, which is
# the rule runtime-proof.yml's streak job states.
history="$(gh api --paginate "repos/${repo}/actions/workflows/${wf_file}/runs?event=schedule&per_page=100" \
  | jq -s --arg id "${run_id}" --arg created "${run_created}" '
      [.[].workflow_runs[]
       | select(.status == "completed")
       | select(.event == "schedule")
       | select((.id | tostring) != $id)
       | select(.created_at < $created)
       | .conclusion]')"
prior_reds="$(jq '[.[] | . != "success"] | (index(false) // length)' <<<"${history}")"
prior_greens="$(jq '[.[] | . == "success"] | (index(false) // length)' <<<"${history}")"
streak_before="$(jq --argjson n "${prior_reds}" \
  '.[$n:] | [.[] | . == "success"] | (index(false) // length)' <<<"${history}")"

target_note=""
if [ -n "${target}" ]; then
  target_note=" (target: ${target})"
fi

title="Red scheduled night: ${wf_name}"
body_file="$(mktemp)"

if [ "${verdict}" = "red" ]; then
  reds_total="$((prior_reds + 1))"
  measured_lines="$(jq -r '.[] | select(.kind == "measured")
    | "- step `\(.step)` — job `\(.job)`"' <<<"${findings}")"
  infra_lines="$(jq -r '.[] | select(.kind == "infra")
    | "- job `\(.job)` ended `\(.conclusion)` with no failing step"' <<<"${findings}")"
  {
    echo "The scheduled run of ${run_date} is red: ${run_url}"
    echo
    if [ -n "${measured_lines}" ]; then
      echo "What failed — the first failing step of a job carries the cause; later"
      echo "failures run under \`if: always()\` and are downstream of it:"
      echo
      echo "${measured_lines}"
      echo
    fi
    if [ -n "${infra_lines}" ]; then
      echo "Where no step carries the failure:"
      echo
      echo "${infra_lines}"
      echo
      echo "A red with no failing step is the run's own infrastructure — a lost runner,"
      echo "a cancellation, an unreachable dependency — not a measured verdict on any"
      echo "suite. No suite is named for it because none failed."
      echo
    fi
    echo "Consecutive red scheduled nights, this one included: **${reds_total}**."
    echo "The green streak before this series was **${streak_before}**; it is now 0${target_note}."
  } > "${body_file}"
else
  new_streak="$((prior_greens + 1))"
  {
    echo "The scheduled night of ${run_date} is green: ${run_url}"
    echo
    echo "The streak restarts at **${new_streak}** consecutive green scheduled night(s)${target_note}."
  } > "${body_file}"
fi

# One issue per workflow, found by label plus exact title — the same lookup
# drift.yml performs, made workflow-specific so several scheduled workflows can
# share the mechanism without sharing an issue.
existing="$(gh issue list --repo "${repo}" --state open --label "${label}" --json number,title \
  | jq -r --arg t "${title}" '[.[] | select(.title == $t)] | first | .number // empty')"

plan="nothing to do (green night, no open issue)"
if [ "${verdict}" = "red" ]; then
  if [ -n "${existing}" ]; then
    plan="comment on issue #${existing}"
  else
    plan="create the issue"
  fi
elif [ -n "${existing}" ]; then
  plan="close issue #${existing}"
fi

echo "run ${run_id} — ${wf_name}, event ${run_event}, created ${run_created}"
echo "verdict: ${verdict}"
echo "plan: ${plan}"
echo "title: ${title}"
echo "--- body ---"
cat "${body_file}"
echo "--- end ---"

if [ "${apply}" -ne 1 ]; then
  exit 0
fi

if [ "${verdict}" = "red" ]; then
  if [ -n "${existing}" ]; then
    gh issue comment "${existing}" --repo "${repo}" --body-file "${body_file}"
  else
    # The label may not exist on a fresh repository; create it once, exactly as
    # drift.yml does for its own.
    gh label create "${label}" --repo "${repo}" --color D93F0B \
      --description "A scheduled run is red and this issue is how somebody learns it" \
      2>/dev/null || true
    gh issue create --repo "${repo}" --title "${title}" --label "${label}" \
      --body-file "${body_file}"
  fi
elif [ -n "${existing}" ]; then
  gh issue close "${existing}" --repo "${repo}" --comment "$(cat "${body_file}")"
fi
