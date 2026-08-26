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
// like at that moment.
func runReport(t *testing.T, night, runID, openIssues string, apply bool) (int, string, string) {
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
