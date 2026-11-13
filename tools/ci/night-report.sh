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
#     failing step (31716251612), plus a night where an auxiliary job is red
#     beside the report (43353368876, three times: the streak job completed
#     and still running, and the verdict job red — #830);
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
# NIGHT_STREAK_TARGET (optional; empty hides the target sentence),
# NIGHT_JUDGED_JOBS (optional; one job display name per line — empty judges
# every completed job, as before).
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
#
# NIGHT_JUDGED_JOBS names the jobs whose conclusion is the verdict, one display
# name per line, as the jobs API reports them. A caller that waits for every
# other job of its workflow leaves it empty and the verdict reads every
# completed job, as before. A caller with a job running beside the report —
# runtime-proof.yml's streak and verdict — sets it, and every other job is
# ignored whatever its status (#830): a verdict that depends on whether an
# auxiliary job happened to finish before this call is a verdict by lot. A
# name that matches no job of the run is an error, not a green night: a
# renamed job would otherwise narrow the verdict silently.
jobs_json="$(gh api --paginate "repos/${repo}/actions/runs/${run_id}/jobs?per_page=100" \
  | jq -s '[.[].jobs[]]')"
if [ -n "${NIGHT_JUDGED_JOBS:-}" ]; then
  missing="$(jq -r --arg judged "${NIGHT_JUDGED_JOBS}" '
    ($judged | split("\n") | map(select(length > 0))) as $names
    | ($names - [.[].name]) | join(", ")' <<<"${jobs_json}")"
  if [ -n "${missing}" ]; then
    echo "FAIL: judged job(s) not found in run ${run_id}: ${missing}; the judged list no longer matches this workflow." >&2
    exit 1
  fi
  jobs_json="$(jq --arg judged "${NIGHT_JUDGED_JOBS}" '
    ($judged | split("\n") | map(select(length > 0))) as $names
    | map(select(.name as $n | $names | index($n) != null))' <<<"${jobs_json}")"
fi
jobs_json="$(jq '[.[] | select(.status == "completed")]' <<<"${jobs_json}")"

# Three outcomes, never two (measurement-integrity): a run with no completed
# job is not green and not red — it is a run this script cannot judge, and
# saying so beats inventing a verdict.
if [ "$(jq 'length' <<<"${jobs_json}")" -eq 0 ]; then
  echo "FAIL: run ${run_id} has no completed job; nothing to judge." >&2
  exit 1
fi

# The timeouts this workflow declares for itself, read from the checkout. A job
# that ran its whole budget and stopped is not a lost runner, and until
# 2026-11-12 this script said it was (#890): the falsify night was cancelled
# after 210.3 minutes against a declared `timeout-minutes: 210`, its long step
# carried the conclusion `cancelled` rather than `failure`, and the issue
# reported "the run's own infrastructure — a lost runner, a cancellation, an
# unreachable dependency". The cause was in the repository: the replay had grown
# past the budget it was given.
#
# Absent or unreadable, the list is empty and nothing is claimed. A fixture
# without timestamps therefore keeps the old wording, which is the honest
# answer when the duration is unknown.
#
# `|| true` is not decoration: this file runs under `set -euo pipefail`, grep
# exits 1 when it matches nothing, and a workflow that declares no timeout is
# the ordinary case — drift.yml is one. Without it the whole script dies on the
# nights that have nothing to report here, which is the same trap that killed
# the backlog step of drift.yml the first time it met a `run:` block's own -e.
budgets="$( { grep -hoE '^[[:space:]]*timeout-minutes:[[:space:]]+[0-9]+' \
  "${FEINT_WORKFLOW_DIR:-.github/workflows}/${wf_file}" 2>/dev/null \
  | grep -oE '[0-9]+' | jq -Rs 'split("\n") | map(select(length > 0) | tonumber)'; } || true)"
[ -n "${budgets}" ] || budgets="[]"

# One finding per job that did not succeed. The first failing step of a job
# carries the cause: the steps after it run under if: always() and fail
# downstream of it (measured on 31716251612, where "Report what was exercised"
# failed because the cancellation had killed the emulator it reads).
#
# A job with no failing step is one of two different things, and folding them
# together is what #890 paid for: either it ran its declared budget to the end,
# which is a measured fact about the work, or it did not, which is the run's own
# infrastructure. Only the second may be called a lost runner.
findings="$(jq --argjson budgets "${budgets}" '
  def failed_steps: [.steps[] | select(.conclusion == "failure")];
  def minutes:
    if (.started_at != null and .completed_at != null)
    then (((.completed_at | fromdateiso8601) - (.started_at | fromdateiso8601)) / 60)
    else null end;
  [.[]
   | select(.conclusion != "success" and .conclusion != "skipped" and .conclusion != "neutral")
   | if .conclusion == "failure" and (failed_steps | length) > 0
     then {kind: "measured", job: .name, step: (failed_steps | first).name}
     else (minutes as $m
           | ($budgets | map(select($m != null and $m >= .))) as $reached
           | if ($reached | length) > 0
             then {kind: "budget", job: .name, conclusion: .conclusion,
                   minutes: ($m | floor), budget: ($reached | max)}
             else {kind: "infra", job: .name, conclusion: .conclusion}
             end)
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
  budget_lines="$(jq -r '.[] | select(.kind == "budget")
    | "- job `\(.job)` ended `\(.conclusion)` after \(.minutes) minutes, and this workflow declares `timeout-minutes: \(.budget)`"' <<<"${findings}")"
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
    if [ -n "${budget_lines}" ]; then
      echo "Where the job ran out of its own budget:"
      echo
      echo "${budget_lines}"
      echo
      echo "That is a measured fact about this repository, and not weather: the"
      echo "work no longer fits the budget it was given. Read what grew before"
      echo "touching the ceiling — raising it is how a budget stops measuring"
      echo "anything."
      echo
    fi
    if [ -n "${infra_lines}" ]; then
      echo "Where no step carries the failure:"
      echo
      echo "${infra_lines}"
      echo
      echo "A red with no failing step, and no budget reached, is the run's own"
      echo "infrastructure — a lost runner, a cancellation, an unreachable dependency —"
      echo "not a measured verdict on any suite. No suite is named for it because none"
      echo "failed."
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
