package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// A met promotion criterion says so where somebody reads it (#125).
//
// The streak job counts consecutive green scheduled runs and, at fourteen,
// writes the verdict into $GITHUB_STEP_SUMMARY. It did on 2026-09-21. Nobody
// read it, the streak broke the next night, and the moment passed — which is
// the failure the workflow's own report job already names: a job log and a step
// summary are the two places nobody opens without already knowing there is a
// problem.
//
// These tests drive the real script with a stubbed `gh`, so the writes land in
// a log instead of on the repository's issues.

// streakStub answers the two calls the script makes and records every write.
// It dispatches on the subcommand rather than on the whole argument line: a
// body carrying the word "comment" must not be mistaken for the verb.
const streakStub = `#!/bin/sh
set -eu
case "$1" in
  issue)
    case "$2" in
      view)
        cat "${GH_STUB_COMMENTS}"
        ;;
      comment)
        printf '%s\n' "$*" >>"${GH_STUB_LOG}"
        prev=""
        for a in "$@"; do
          if [ "${prev}" = "--body-file" ]; then
            cat "${a}" >>"${GH_STUB_LOG}"
          fi
          prev="${a}"
        done
        ;;
      *)
        echo "unexpected gh issue call: $*" >&2
        exit 64
        ;;
    esac
    ;;
  *)
    echo "unexpected gh call: $*" >&2
    exit 64
    ;;
esac
`

// runStreak executes streak-report.sh and returns its exit code, its output,
// and the log of every gh write the stub received. comments is what
// `gh issue view --json comments --jq '.comments[].body'` would answer.
func runStreak(t *testing.T, streak, target int, comments string, apply bool) (int, string, string) {
	t.Helper()

	stubDir := t.TempDir()
	stub := filepath.Join(stubDir, "gh")
	if err := os.WriteFile(stub, []byte(streakStub), 0o755); err != nil { //nolint:gosec // a stub this test runs
		t.Fatal(err)
	}
	commentsFile := filepath.Join(stubDir, "comments.txt")
	if err := os.WriteFile(commentsFile, []byte(comments), 0o600); err != nil {
		t.Fatal(err)
	}
	log := filepath.Join(stubDir, "calls.log")

	args := []string{"streak-report.sh"}
	if apply {
		args = append(args, "--apply")
	}
	args = append(args, strconv.Itoa(streak), strconv.Itoa(target), "125")

	cmd := exec.Command("bash", args...)
	cmd.Env = append(os.Environ(),
		"PATH="+stubDir+":"+os.Getenv("PATH"),
		"GH_STUB_COMMENTS="+commentsFile,
		"GH_STUB_LOG="+log,
		"GITHUB_REPOSITORY=stephrobert/feint",
		"FEINT_RUN_URL=https://example.invalid/run/1",
	)
	out, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if err != nil {
		if ok := asExitError(err, &exit); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("run streak-report.sh: %v\n%s", err, out)
		}
	}
	written, _ := os.ReadFile(log) //nolint:gosec // a path this test made
	return code, string(out), string(written)
}

// Below the target, nothing is said.
//
// A comment every night would be the shape #502 refuses for red nights: a
// notification that arrives whatever happens teaches its reader to skip it.
func TestAStreakBelowTheTargetAnnouncesNothing(t *testing.T) {
	code, out, writes := runStreak(t, 13, 14, "", true)
	if code != 0 {
		t.Fatalf("exit %d for a streak below target: %s", code, out)
	}
	if writes != "" {
		t.Errorf("it wrote to the issue at 13/14:\n%s", writes)
	}
	if !strings.Contains(out, "not met") {
		t.Errorf("the verdict does not say the criterion is unmet: %s", out)
	}
}

// At the target, the issue gets the comment.
//
// This is the whole point: on 2026-09-21 the criterion was met and the only
// trace was a step summary.
func TestAMetCriterionIsAnnouncedOnTheIssue(t *testing.T) {
	code, out, writes := runStreak(t, 14, 14, "", true)
	if code != 0 {
		t.Fatalf("exit %d when the criterion is met: %s", code, out)
	}
	if !strings.Contains(writes, "comment") || !strings.Contains(writes, "125") {
		t.Fatalf("no comment was posted on #125 when the criterion was met:\n%s", writes)
	}
	// The number is in the comment, because "the criterion is met" without it
	// asks the reader to go and count.
	if !strings.Contains(writes, "14 consecutive green scheduled runs") {
		t.Errorf("the comment does not carry the count that earns it:\n%s", writes)
	}
	// And what to do next, since the issue's own answer is a one-line action.
	if !strings.Contains(writes, "pull_request") {
		t.Errorf("the comment does not name the promotion it asks for:\n%s", writes)
	}
}

// A streak past the target still announces, once.
//
// Fourteen is a floor, not an equality: a run at fifteen must not fall through
// the condition and go silent.
func TestAStreakPastTheTargetStillAnnounces(t *testing.T) {
	_, _, writes := runStreak(t, 21, 14, "", true)
	if !strings.Contains(writes, "comment") {
		t.Errorf("nothing was posted at 21/14: the criterion is a floor, not an equality:\n%s", writes)
	}
}

// It is said once, not every night.
//
// The marker is what makes it idempotent. Without this, a met criterion posts a
// comment every night until somebody acts, which is how a notification becomes
// noise somebody mutes — and then the next one is missed too.
func TestAnAnnouncedCriterionIsNotRepeated(t *testing.T) {
	const posted = "<!-- feint:streak-criterion-met -->\n\nThe promotion criterion is met."
	code, out, writes := runStreak(t, 15, 14, posted, true)
	if code != 0 {
		t.Fatalf("exit %d on a second night: %s", code, out)
	}
	if writes != "" {
		t.Errorf("it posted a second time:\n%s", writes)
	}
	if !strings.Contains(out, "already announced") {
		t.Errorf("the verdict does not say it was already announced: %s", out)
	}
}

// A maintainer's own prose about the streak is not the announcement.
//
// The marker is searched, not the words: somebody writing "the criterion is
// met" in a discussion must not silence the mechanism.
func TestHumanProseDoesNotCountAsTheAnnouncement(t *testing.T) {
	const prose = "I think the promotion criterion is met, should we move it onto pull_request?"
	_, _, writes := runStreak(t, 14, 14, prose, true)
	if !strings.Contains(writes, "comment") {
		t.Errorf("a comment merely mentioning the criterion silenced the announcement:\n%s", writes)
	}
}

// Without --apply it writes nothing, so the script can be run against any past
// number and read.
func TestTheStreakReportWritesNothingWithoutApply(t *testing.T) {
	_, out, writes := runStreak(t, 14, 14, "", false)
	if writes != "" {
		t.Errorf("it wrote without --apply:\n%s", writes)
	}
	if !strings.Contains(out, "the comment it would post") {
		t.Errorf("a dry run does not show what it would post: %s", out)
	}
}

// The workflow actually calls the announcement.
//
// The script and its tests prove the decision; they say nothing about whether
// anything runs it. On 2026-09-21 the counting worked perfectly and the verdict
// reached nobody, so "the logic is correct" was already true when the failure
// happened.
func TestTheStreakJobAnnouncesOnTheIssue(t *testing.T) {
	workflow := readWorkflow(t, "runtime-proof.yml")

	block := stepBlock(workflow, "A met criterion says so on the issue")
	if block == "" {
		t.Fatal("runtime-proof.yml counts the streak and never announces it: the verdict " +
			"reaches a step summary and stops there, which is what happened on 2026-09-21")
	}

	// The command the step RUNS, not a substring of the line. Asserting
	// `Contains(…, "streak-report.sh --apply")` passes for
	// `echo streak-report.sh --apply`, which announces nothing — falsify caught
	// exactly that, and it is the third time in this repository that a Contains
	// has been satisfied by a longer string that does the opposite.
	run := commandOf(block)
	if !strings.HasPrefix(run, "tools/ci/streak-report.sh --apply") {
		t.Fatalf("the announcing step runs %q, not the announcement", run)
	}
	// Writing on an issue needs the permission; without it the step fails, or
	// worse, reports success having posted nothing.
	streakJob := workflow[strings.Index(workflow, "  streak:"):]
	if cut := strings.Index(streakJob, "\n  report:"); cut > 0 {
		streakJob = streakJob[:cut]
	}
	if !strings.Contains(streakJob, "issues: write") {
		t.Error("the streak job cannot write on an issue: `issues: write` is missing, and a " +
			"comment it cannot post is a silence that reports success")
	}
	// The issue it announces on is this one; a typo here posts the verdict
	// somewhere nobody is waiting for it.
	if !strings.Contains(block, " 125") {
		t.Errorf("the announcement does not name issue 125:\n%s", block)
	}
}

// commandOf answers a step's `run:` command, or "" when it declares none.
//
// The value, so a check on it cannot be satisfied by a longer line that merely
// contains it.
func commandOf(block string) string {
	for _, line := range strings.Split(block, "\n") {
		if after, found := strings.CutPrefix(strings.TrimSpace(line), "run:"); found {
			return strings.TrimSpace(after)
		}
	}
	return ""
}
