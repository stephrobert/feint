// Package ci holds the tests of the shell this repository's workflows call.
// The workflows themselves only run on GitHub's runners; the decisions they
// delegate to versioned scripts do not, and those are exactly the part that
// has to be provable before a push — a run: block is untestable by
// construction, which is why the logic does not live in one.
package ci

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// night-report.sh (#502) turns a scheduled run's outcome into one issue,
// updated and eventually closed, never a new one per night. These tests drive
// the real script with a stubbed `gh` that replays field-trimmed payloads of
// real runs of runtime-proof.yml, captured on 2026-08-26:
//
//	testdata/red-night        run 32924409224 — schedule, failure; the incus
//	                          job failed on "Scaleway ssh suite", the
//	                          incus-ovn job on "Outscale network suite"
//	testdata/green-night      run 32441329459 — schedule, success
//	testdata/cancelled-night  run 31716251612 — cancelled, and its one step
//	                          with conclusion "failure" is "Report what was
//	                          exercised", an if: always() step that failed
//	                          downstream of the cancellation, not a cause
//	testdata/race-night-completed
//	                  run 43353368876 — schedule, success, every proof job
//	                  green, except the streak job, flipped to failure with
//	                  no failing step (its shape on the fourteen nights of
//	                  #830); the report job itself is in_progress, as it is
//	                  while the script runs
//	testdata/race-night-running
//	                  the same night, the same flip, except the streak job
//	                  is still in_progress — the streak job's status and
//	                  conclusion, and those of its steps, are the only
//	                  difference from race-night-completed
//	testdata/race-night-red
//	                  the same night, the streak job flipped as above and
//	                  the incus job failing on "Scaleway ssh suite": the
//	                  control that a judged list still measures
//	testdata/race-night-verdict-red
//	                  the same night, the verdict job ("Us, or the world?")
//	                  flipped to failure instead: it sits in the same race
//	                  as streak, and its red must be ignored the same way
//	testdata/race-night-unaccounted
//	                  the same night, the streak job renamed to "Nightly
//	                  housekeeping": a job of the run that is neither
//	                  judged nor ignored, so the night must be refused
//	testdata/race-night-judged-running
//	                  the same night, the incus job still in_progress: a
//	                  judged job that has not finished, so the night must
//	                  be refused rather than read green
//	testdata/history.json     every completed scheduled run of the workflow
//
// The writes land in a log instead of on GitHub, so the create/update/close
// branching is asserted without opening a test issue on the repository.
// tools/falsify/specs/a-red-night-opens-an-issue.json replays these tests
// with each decision neutralised.

// The stub dispatches on the subcommand, never on the whole argument line: a
// close comment carries the run's URL, so a pattern like *"/actions/runs/"*
// matched against "$*" catches the write and answers it with run.json — a
// harness lying before its subject, caught on this stub's first run.
const ghStub = `#!/usr/bin/env bash
set -eu
case "$1" in
  api)
    case "$*" in
      *"/actions/runs/"*"/jobs"*) cat "${GH_STUB_DIR}/jobs.json" ;;
      *"/actions/workflows/"*"/runs"*) cat "${GH_STUB_HISTORY}" ;;
      *"/actions/runs/"*) cat "${GH_STUB_DIR}/run.json" ;;
      *)
        echo "unexpected gh api call: $*" >&2
        exit 64
        ;;
    esac
    ;;
  issue)
    if [ "$2" = "list" ]; then
      cat "${GH_STUB_ISSUES}"
      exit 0
    fi
    printf '%s\n' "$*" >>"${GH_STUB_LOG}"
    prev=""
    for a in "$@"; do
      if [ "${prev}" = "--body-file" ]; then
        cat "${a}" >>"${GH_STUB_LOG}"
      fi
      prev="${a}"
    done
    ;;
  label)
    printf '%s\n' "$*" >>"${GH_STUB_LOG}"
    ;;
  *)
    echo "unexpected gh call: $*" >&2
    exit 64
    ;;
esac
`

// runReport executes night-report.sh against the fixtures of one night and
// returns its exit code, its stdout+stderr, and the log of every gh write the
// stub received. openIssues is a JSON array in `gh issue list --json
// number,title` shape: what the repository's open scheduled-red issues look
// like at that moment. judged is the NIGHT_JUDGED_JOBS value and ignored the
// NIGHT_IGNORED_JOBS value: empty judges every completed job, as before.
func runReport(t *testing.T, night, runID, openIssues string, apply bool) (int, string, string) {
	return runReportJudged(t, night, runID, openIssues, apply, "", "")
}

func runReportJudged(t *testing.T, night, runID, openIssues string, apply bool, judged, ignored string) (int, string, string) {
	t.Helper()

	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "gh")
	if err := os.WriteFile(stub, []byte(ghStub), 0o755); err != nil {
		t.Fatal(err)
	}
	issues := filepath.Join(stubDir, "issues.json")
	if err := os.WriteFile(issues, []byte(openIssues), 0o644); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(stubDir, "calls.log")

	fixtures, err := filepath.Abs(filepath.Join("testdata", night))
	if err != nil {
		t.Fatal(err)
	}
	history, err := filepath.Abs(filepath.Join("testdata", "history.json"))
	if err != nil {
		t.Fatal(err)
	}

	args := []string{"night-report.sh"}
	if apply {
		args = append(args, "--apply")
	}
	args = append(args, runID)

	cmd := exec.Command("bash", args...)
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_STUB_DIR="+fixtures,
		"GH_STUB_HISTORY="+history,
		"GH_STUB_ISSUES="+issues,
		"GH_STUB_LOG="+log,
		"GITHUB_REPOSITORY=stephrobert/feint",
		"NIGHT_STREAK_TARGET=14",
		"NIGHT_JUDGED_JOBS="+judged,
		"NIGHT_IGNORED_JOBS="+ignored,
		// The script reads the budget a workflow declares for itself, and these
		// tests run from the package directory rather than the repository root.
		// A seam, like the injectable runner of the Incus driver: on a runner
		// the default is the checkout's own .github/workflows.
		"FEINT_WORKFLOW_DIR="+filepath.Join("..", "..", ".github", "workflows"),
	)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("running night-report.sh: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	calls, readErr := os.ReadFile(log)
	if readErr != nil && !os.IsNotExist(readErr) {
		t.Fatal(readErr)
	}
	return code, string(out), string(calls)
}

const noOpenIssue = `[]`

// oneOpenIssue is what `gh issue list` answers once a first red night has
// opened the issue: number 600, the exact title the script composes.
const oneOpenIssue = `[{"number": 600, "title": "Red scheduled night: Runtime proof"}]`

// A red night names the step and the job that failed, and carries the streak —
// not "the workflow failed", which is the noise #502 exists to replace. The
// numbers are the real ones: the fifth consecutive red night, after a green
// streak of one.
func TestARedNightNamesTheStepTheJobAndTheStreak(t *testing.T) {
	code, out, _ := runReport(t, "red-night", "32924409224", noOpenIssue, false)
	if code != 0 {
		t.Fatalf("the script failed on a real red run (exit %d):\n%s", code, out)
	}
	for _, want := range []string{
		"verdict: red",
		"plan: create the issue",
		"title: Red scheduled night: Runtime proof",
		"- step `Scaleway ssh suite` — job `incus`",
		"- step `Outscale network suite` — job `incus-ovn`",
		"Consecutive red scheduled nights, this one included: **5**.",
		"The green streak before this series was **1**",
		"(target: 14)",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the report never says %q; a notification that does not name "+
				"the step, the job and the streak is a constat, not an alert:\n%s", want, out)
		}
	}
}

// The first red night creates the issue; without --apply nothing is written at
// all. Both halves matter: the second is what lets the script be pointed at
// any real run to be read.
func TestAFirstRedNightOpensTheIssue(t *testing.T) {
	_, _, calls := runReport(t, "red-night", "32924409224", noOpenIssue, false)
	if calls != "" {
		t.Errorf("a dry run performed writes:\n%s", calls)
	}

	code, out, calls := runReport(t, "red-night", "32924409224", noOpenIssue, true)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(calls, "issue create") ||
		!strings.Contains(calls, "--title Red scheduled night: Runtime proof") ||
		!strings.Contains(calls, "--label scheduled-red") {
		t.Errorf("the first red night did not create the labelled issue:\n%s", calls)
	}
	if strings.Contains(calls, "issue comment") {
		t.Errorf("commented with no issue to comment on:\n%s", calls)
	}
}

// The second red night updates the same issue instead of opening a second one.
// drift.yml states the rule this repeats: an issue per night teaches everyone
// to close them unread, which is the failure mode the mechanism exists to
// avoid.
func TestASecondRedNightUpdatesTheIssue(t *testing.T) {
	code, out, calls := runReport(t, "red-night", "32924409224", oneOpenIssue, true)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(calls, "issue comment 600") {
		t.Errorf("the second red night never commented on the open issue:\n%s", calls)
	}
	if strings.Contains(calls, "issue create") {
		t.Errorf("a second issue was created; one per night is worse than none:\n%s", calls)
	}
}

// A green night closes the issue and says where the streak restarts; a green
// night with nothing open writes nothing, because a notification mechanism
// that speaks when all is well becomes noise of its own.
func TestAGreenNightClosesTheIssue(t *testing.T) {
	code, out, calls := runReport(t, "green-night", "32441329459", oneOpenIssue, true)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(calls, "issue close 600") {
		t.Errorf("the green night left the issue open:\n%s", calls)
	}
	if !strings.Contains(out, "The streak restarts at **1**") {
		t.Errorf("the closing note does not carry the restarted streak:\n%s", out)
	}

	code, out, calls = runReport(t, "green-night", "32441329459", noOpenIssue, true)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if calls != "" {
		t.Errorf("a green night with no open issue still wrote something:\n%s", calls)
	}
}

// A red run where no step failed is an infrastructure failure and must say so
// instead of inventing a cause. The fixture is the sharpest real case on
// record: both jobs of run 31716251612 were cancelled, and each carries one
// step with conclusion "failure" — "Report what was exercised", an
// if: always() step that failed because the cancellation had killed the
// emulator it reads. Naming it would blame the reporter for the outage.
func TestARedWithNoFailingStepBlamesTheRunNotASuite(t *testing.T) {
	code, out, _ := runReport(t, "cancelled-night", "31716251612", noOpenIssue, false)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "no failing step") ||
		!strings.Contains(out, "not a measured verdict") {
		t.Errorf("the infrastructure failure is not written as one:\n%s", out)
	}
	for _, invented := range []string{"Report what was exercised", "ssh suite", "network suite"} {
		if strings.Contains(out, invented) {
			t.Errorf("the report names %q for a run where no suite failed; "+
				"a cause was invented:\n%s", invented, out)
		}
	}
}

// A job that ran its whole budget is a measured fact, not a lost runner (#890).
//
// budget-night is the field-trimmed payload of run 43154285437, the scheduled
// falsify run of 2026-11-12: the job started at 04:21:45 and ended `cancelled`
// at 07:52:04, which is 210.3 minutes against the `timeout-minutes: 210` that
// falsify.yml declares for itself. Its long step carries the conclusion
// `cancelled` rather than `failure`, so the classification found no failing
// step and reported the run's own infrastructure — "a lost runner, a
// cancellation, an unreachable dependency".
//
// None of those was true. The replay had grown past the budget it was given:
// the 0.16 batch added 23 specs and 263 mutations, and that night was the first
// to run them. Reading that as infrastructure is how a real finding gets filed
// as weather, and the one instrument that proves this repository's tests bite
// stopped going to the end with nobody owning the fact.
func TestANightCancelledAtItsOwnCeilingNamesTheBudget(t *testing.T) {
	code, out, _ := runReport(t, "budget-night", "43154285437", noOpenIssue, false)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "ran out of its own budget") {
		t.Errorf("a job cancelled at its declared ceiling is not reported as a budget:\n%s", out)
	}
	// The two numbers, because a sentence without them cannot be acted on: the
	// duration measured, and the ceiling the workflow declares.
	if !strings.Contains(out, "210 minutes") {
		t.Errorf("the report does not say how long the job ran:\n%s", out)
	}
	if !strings.Contains(out, "timeout-minutes: 210") {
		t.Errorf("the report does not name the declared ceiling it reached:\n%s", out)
	}
	// And it must stop reaching for infrastructure, which is the paragraph that
	// was posted on this very run. Asserted on the word, not on the sentence
	// that denies it: the budget paragraph used to say "and not a lost runner",
	// so a search for that phrase passed on a report that still blamed one.
	if strings.Contains(out, "infrastructure") {
		t.Errorf("the report still reaches for infrastructure on a job that ran its ceiling:\n%s", out)
	}
}

// And the other half: a cancellation that reached no budget is still the run's
// own infrastructure, and must still say so.
//
// cancelled-night carries no timestamps, which is the honest shape of "the
// duration is unknown": nothing may be claimed about a budget from it. A
// classification that answered "budget" whenever a job was cancelled would pass
// the test above and lie here.
func TestACancellationThatReachedNoBudgetIsStillInfrastructure(t *testing.T) {
	_, out, _ := runReport(t, "cancelled-night", "31716251612", noOpenIssue, false)
	if strings.Contains(out, "ran out of its own budget") {
		t.Errorf("a cancellation with no measured duration was reported as a budget:\n%s", out)
	}
	if !strings.Contains(out, "infrastructure") {
		t.Errorf("a cancellation that reached no budget is no longer reported as infrastructure:\n%s", out)
	}
}

// The drift scan adopts the mechanism (#705), and the two outcomes it has to
// keep apart are held here rather than asserted in a workflow comment.
//
// drift-quiet is the field-trimmed payload of run 33365885627, the scheduled
// run of 2026-08-31: nothing moved, so `propose` and `report` were skipped. No
// scheduled drift run has ever moved the surface or gone red, so the two other
// nights are derived from that payload and say so. drift-moved turns the
// `Coverage report` step's conclusion to failure while its job stays success,
// which is what continue-on-error produces on a night the surface moved, and
// marks the pull request and issue jobs success. drift-red turns the `Checkout
// the Scaleway SDK` step and its job to failure and skips the steps behind it.

// oneOpenDriftIssue is what `gh issue list` answers once a red drift night has
// opened the issue: the exact title the script composes for that workflow.
const oneOpenDriftIssue = `[{"number": 601, "title": "Red scheduled night: Upstream drift"}]`

// A quiet night — the two jobs behind the scan skipped — is green, and a green
// night with nothing open writes nothing.
func TestADriftNightWhereNothingMovedIsGreen(t *testing.T) {
	code, out, calls := runReport(t, "drift-quiet", "33365885627", noOpenIssue, true)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "verdict: green") {
		t.Errorf("a quiet drift night, propose and report skipped, is not green:\n%s", out)
	}
	if calls != "" {
		t.Errorf("a green night with no open issue wrote something:\n%s", calls)
	}
}

// The surface moving is the mechanism working, not a red night: the pull
// request carries it, and this job closes what a real red night had opened.
func TestADriftNightWhereTheSurfaceMovedIsNotARedNight(t *testing.T) {
	code, out, calls := runReport(t, "drift-moved", "33365885627", oneOpenDriftIssue, true)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "verdict: green") {
		t.Errorf("a night where the surface moved reads as red, so every drift would open the red-night issue:\n%s", out)
	}
	if strings.Contains(calls, "issue create") || strings.Contains(calls, "issue comment") {
		t.Errorf("a night where the surface moved wrote to the red-night issue:\n%s", calls)
	}
	if !strings.Contains(calls, "issue close 601") {
		t.Errorf("the green night left the drift issue open:\n%s", calls)
	}
}

// A night where the scan could not conclude opens the issue, names the step
// and the job, and titles it for this workflow.
func TestADriftNightThatCouldNotConcludeOpensTheIssue(t *testing.T) {
	code, out, calls := runReport(t, "drift-red", "33365885627", noOpenIssue, true)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "verdict: red") {
		t.Fatalf("a scan whose checkout failed is not a red night:\n%s", out)
	}
	for _, want := range []string{"Red scheduled night: Upstream drift", "Checkout the Scaleway SDK", "Compare with the upstream SDK"} {
		if !strings.Contains(out, want) {
			t.Errorf("the report does not carry %q:\n%s", want, out)
		}
	}
	if !strings.Contains(calls, "issue create") {
		t.Errorf("the first red drift night did not open the issue:\n%s", calls)
	}
}

// runtimeProofLists reads the judged and ignored lists the report job of
// runtime-proof.yml actually passes, and checks the env wiring of
// night-report.yml that carries them to the script — so these tests run on
// the caller's real lists, not on a re-declaration of them in this file. A
// list narrowed in the workflow, or an env line commented out of the reusable
// workflow, used to leave every test green; it now fails here.
func runtimeProofLists(t *testing.T) (judged, ignored string) {
	t.Helper()
	proof, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "runtime-proof.yml"))
	if err != nil {
		t.Fatal(err)
	}
	report := jobSection(string(proof), "report")
	if report == "" {
		t.Fatal("runtime-proof.yml has no report job: the lists below have nowhere to come from")
	}
	judged = blockList(t, report, "judged-jobs")
	ignored = blockList(t, report, "ignored-jobs")
	if ignored == "" {
		t.Fatal("runtime-proof.yml's report job passes no ignored-jobs list: the closure check cannot tell the auxiliary jobs from a new one")
	}
	// The report job is in_progress while the script runs, so the closure
	// check only passes if the caller ignores its own display name — as the
	// jobs API reports it, "<job name> / <step name>".
	if !strings.Contains(ignored, "A red night opens the issue, a green one closes it") {
		t.Fatalf("runtime-proof.yml's ignored list does not name the report job itself:\n%s", ignored)
	}
	night, err := os.ReadFile(filepath.Join("..", "..", ".github", "workflows", "night-report.yml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, wire := range [][2]string{
		{"NIGHT_JUDGED_JOBS", "${{ inputs.judged-jobs }}"},
		{"NIGHT_IGNORED_JOBS", "${{ inputs.ignored-jobs }}"},
	} {
		if !wiredEnv(string(night), wire[0], wire[1]) {
			t.Fatalf("night-report.yml does not wire %s to %s: the list the caller passes never reaches the script", wire[0], wire[1])
		}
	}
	return judged, ignored
}

// wiredEnv reports whether a workflow wires an env var to an input — the
// line itself, not a comment mentioning it. A `# NIGHT_JUDGED_JOBS: ...`
// line still contains the string, so a Contains check would bless a wiring
// that is commented out; this reads lines, and skips comments.
func wiredEnv(body, key, value string) bool {
	for _, line := range strings.Split(body, "\n") {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "#") {
			continue
		}
		if strings.HasPrefix(trimmed, key+":") && strings.Contains(trimmed, value) {
			return true
		}
	}
	return false
}

// blockList is the value of a `key: |` block inside a job section, one entry
// per line, in the order the workflow declares them.
func blockList(t *testing.T, section, key string) string {
	t.Helper()
	lines := strings.Split(section, "\n")
	for i, line := range lines {
		if strings.TrimSpace(line) != key+": |" {
			continue
		}
		indent := len(line) - len(strings.TrimLeft(line, " "))
		var entries []string
		for _, next := range lines[i+1:] {
			if strings.TrimSpace(next) == "" {
				continue
			}
			nextIndent := len(next) - len(strings.TrimLeft(next, " "))
			if nextIndent <= indent {
				break
			}
			entries = append(entries, strings.TrimSpace(next))
		}
		return strings.Join(entries, "\n")
	}
	t.Fatalf("no %s: | block in the section:\n%s", key, section)
	return ""
}

// A red auxiliary job running beside the report must not decide the verdict
// (#830). The payloads are the same night — run 43353368876, every proof job
// green — with one auxiliary job flipped to failure and no failing step.
// race-night-running is the witness of the race, not a control on the fix:
// without the judged list it read green while race-night-completed read red,
// and with the fix it stays green either way, because the completed filter
// already dropped the streak job. The controls are race-night-completed — a
// red auxiliary, completed, must not decide — and race-night-red below, where
// a measured red must still be reported.
func TestARedAuxiliaryJobBesideTheReportDoesNotDecideTheVerdict(t *testing.T) {
	judged, ignored := runtimeProofLists(t)
	for _, night := range []string{"race-night-completed", "race-night-running", "race-night-verdict-red"} {
		code, out, calls := runReportJudged(t, night, "43353368876", noOpenIssue, true, judged, ignored)
		if code != 0 {
			t.Fatalf("%s: exit %d:\n%s", night, code, out)
		}
		if !strings.Contains(out, "verdict: green") {
			t.Errorf("%s: an auxiliary job red beside the report still decides the verdict:\n%s", night, out)
		}
		if calls != "" {
			t.Errorf("%s: a night whose proof jobs are green still wrote something:\n%s", night, calls)
		}
	}
}

// And the control the other way: the judged list must not blind the script
// to a real red. race-night-red is the same night with the incus job failing
// on a step, and the streak job red beside it as above. The verdict must be
// red, it must name the step, and it must not name the streak job at all —
// the auxiliary red is ignored, the measured red is reported.
func TestAJudgedListStillMeasuresTheJobsItNames(t *testing.T) {
	judged, ignored := runtimeProofLists(t)
	code, out, calls := runReportJudged(t, "race-night-red", "43353368876", noOpenIssue, true, judged, ignored)
	if code != 0 {
		t.Fatalf("exit %d:\n%s", code, out)
	}
	if !strings.Contains(out, "verdict: red") {
		t.Fatalf("a judged job failing on a step is not a red night:\n%s", out)
	}
	if !strings.Contains(out, "- step `Scaleway ssh suite` — job `incus`") {
		t.Errorf("the report does not name the failed step and job:\n%s", out)
	}
	if strings.Contains(out, "Consecutive green scheduled runs") {
		t.Errorf("the report names the auxiliary job whose red it must ignore:\n%s", out)
	}
	if !strings.Contains(calls, "issue create") {
		t.Errorf("the red night did not open the issue:\n%s", calls)
	}
}

// A judged name that matches no job of the run is an error, not a green
// night: a renamed job would otherwise narrow the verdict silently, and the
// silence would read exactly like a green night. The missing name is printed
// quoted, so a trailing space cannot make it look like a job that is there.
func TestAJudgedJobThatNeverRanIsAnErrorNotAGreenNight(t *testing.T) {
	_, ignored := runtimeProofLists(t)
	code, out, _ := runReportJudged(t, "race-night-completed", "43353368876", noOpenIssue, false, "incus\nno-such-job", ignored)
	if code == 0 {
		t.Fatalf("a judged list naming a job that never ran was accepted:\n%s", out)
	}
	if !strings.Contains(out, `"no-such-job"`) {
		t.Errorf("the failure does not name the missing job, quoted:\n%s", out)
	}
}

// A job of the run that is neither judged nor ignored is an error on the same
// footing: a new job, or a new matrix leg, must force a decision instead of
// vanishing from the verdict. race-night-unaccounted renames the streak job
// to a name on neither list; the script must refuse the night and name it,
// quoted, rather than read the remaining jobs green.
func TestAJobNeitherJudgedNorIgnoredIsAnErrorNotAGreenNight(t *testing.T) {
	judged, ignored := runtimeProofLists(t)
	code, out, _ := runReportJudged(t, "race-night-unaccounted", "43353368876", noOpenIssue, false, judged, ignored)
	if code == 0 {
		t.Fatalf("a run with an unaccounted job was accepted:\n%s", out)
	}
	if !strings.Contains(out, `"Nightly housekeeping"`) {
		t.Errorf("the failure does not name the unaccounted job, quoted:\n%s", out)
	}
}

// A judged job that has not finished is an error on the same footing as a
// missing one: narrowing first and filtering completed afterwards used to drop
// it in silence and read the night green, which is #830 with timing in place
// of renaming. race-night-judged-running leaves incus in_progress.
func TestAJudgedJobStillRunningIsAnErrorNotAGreenNight(t *testing.T) {
	judged, ignored := runtimeProofLists(t)
	code, out, _ := runReportJudged(t, "race-night-judged-running", "43353368876", noOpenIssue, false, judged, ignored)
	if code == 0 {
		t.Fatalf("a run whose judged job had not finished was accepted:\n%s", out)
	}
	if !strings.Contains(out, `"incus"`) {
		t.Errorf("the failure does not name the unfinished judged job, quoted:\n%s", out)
	}
}

// A judged list without the ignore list is an error, not a night judged
// against an empty ignore list: the closure check cannot tell the auxiliary
// jobs from a new one, and a caller that forgot the second list must hear
// about it before the night it misjudges.
func TestAJudgedListWithoutAnIgnoreListIsAnError(t *testing.T) {
	judged, _ := runtimeProofLists(t)
	code, out, _ := runReportJudged(t, "race-night-completed", "43353368876", noOpenIssue, false, judged, "")
	if code == 0 {
		t.Fatalf("a judged list with no ignore list was accepted:\n%s", out)
	}
	if !strings.Contains(out, "NIGHT_IGNORED_JOBS names no job") {
		t.Errorf("the failure does not say the ignore list is missing:\n%s", out)
	}
}

// A judged value of blank lines only is an error, not an empty list: it is a
// mistake wearing the shape of "judge everything", and must not fall back to
// judging every completed job with a green check.
func TestAWhitespaceOnlyJudgedListIsAnError(t *testing.T) {
	_, ignored := runtimeProofLists(t)
	code, out, _ := runReportJudged(t, "race-night-completed", "43353368876", noOpenIssue, false, "   \n\t\n", ignored)
	if code == 0 {
		t.Fatalf("a blank-lines-only judged list was accepted:\n%s", out)
	}
	if !strings.Contains(out, "NIGHT_JUDGED_JOBS names no job") {
		t.Errorf("the failure does not say the judged list names no job:\n%s", out)
	}
}
