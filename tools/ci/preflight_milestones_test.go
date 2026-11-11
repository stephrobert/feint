package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// milestoneGhStub answers one call — the milestones query — from a fixture, and
// refuses anything else loudly. A stub that answered every call would let a
// change to the query pass unnoticed, which is the failure the gate's own
// comment describes happening twice to preflight.sh's CI query.
//
// It APPLIES the --jq expression the gate passes, with the real jq, rather than
// returning the raw fixture. The first version did not, and it is worth saying
// why: the gate then read a JSON array where it expected tab-separated lines,
// matched nothing, and reported the repository coherent on the very payload
// that asked for the gate. The test went green for the only reason that makes a
// green worthless — the instrument, not the subject. Emulating gh's own
// transform is what makes the expression part of what is measured.
const milestoneGhStub = `#!/usr/bin/env bash
set -eu
case "$1" in
  api)
    case "$*" in
      *"/milestones"*)
        expr='.'
        prev=""
        for a in "$@"; do
          if [ "${prev}" = "--jq" ]; then expr="${a}"; fi
          prev="${a}"
        done
        jq -r "${expr}" "${GH_STUB_MILESTONES}"
        ;;
      *)
        echo "unexpected gh api call: $*" >&2
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

// runMilestoneGate drives tools/release/milestone-gate.sh against one fixture
// of the repository's milestones and returns its exit code and its output.
//
// It builds a throwaway git repository carrying the tags the fixtures talk
// about, and runs the gate there. Reading the real repository's tags would
// have been shorter and wrong twice over: a shallow CI checkout has no tags,
// so the red fixture would stop producing findings and this test would start
// measuring the checkout depth; and the day v0.16.0 is tagged for real, the
// accepting fixture would silently become a red one.
func runMilestoneGate(t *testing.T, version, milestones string) (int, string) {
	t.Helper()

	dir := t.TempDir()
	stub := filepath.Join(dir, "gh")
	if err := os.WriteFile(stub, []byte(milestoneGhStub), 0o755); err != nil {
		t.Fatal(err)
	}
	fixture := filepath.Join(dir, "milestones.json")
	if err := os.WriteFile(fixture, []byte(milestones), 0o644); err != nil {
		t.Fatal(err)
	}

	script, err := filepath.Abs(filepath.Join("..", "release", "milestone-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}

	repo := filepath.Join(dir, "repo")
	if err := os.Mkdir(repo, 0o755); err != nil {
		t.Fatal(err)
	}
	// Every GIT_* and PRE_COMMIT_* variable is dropped rather than inherited.
	// This test passed standalone and failed under the pre-push hook, which
	// exports GIT_DIR, GIT_INDEX_FILE and friends: the throwaway repository
	// then pointed at the real one, and `git commit` ran the project's hooks.
	// An inherited environment is the harness measuring whoever ran it.
	clean := make([]string, 0, len(os.Environ()))
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "GIT_") || strings.HasPrefix(kv, "PRE_COMMIT") {
			continue
		}
		clean = append(clean, kv)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", args...)
		cmd.Dir = repo
		cmd.Env = append(append([]string{}, clean...),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@example.invalid",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@example.invalid")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v\n%s", args, err, out)
		}
	}
	// An empty template directory, so no hook of the operator's own is copied
	// into a repository this test is about to commit in.
	git("-c", "init.templateDir=", "init", "--quiet")
	git("commit", "--allow-empty", "--quiet", "-m", "root")
	// The published history these fixtures are about. v0.16.0 is deliberately
	// absent: it is the version being tagged.
	for _, tag := range []string{"v0.12.0", "v0.13.0", "v0.14.0", "v0.15.0"} {
		git("tag", tag)
	}

	cmd := exec.Command("bash", script, version)
	cmd.Dir = repo
	cmd.Env = append(append([]string{}, clean...),
		"PATH="+dir+string(os.PathListSeparator)+os.Getenv("PATH"),
		"GH_STUB_MILESTONES="+fixture,
	)
	out, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if err != nil {
		if ok := asExitError(err, &exit); !ok {
			t.Fatalf("running the gate: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out)
}

// The payload the repository actually answered on 2026-11-11, trimmed to the
// rows that matter. This is the red fixture: it is the measurement that asked
// for the gate, so the gate is tested against the state it was written for
// rather than against a shape invented here.
const milestonesOn20261111 = `[
  {"state":"open","open_issues":19,"title":"0.14.0 — The topology outranks its history"},
  {"state":"open","open_issues":10,"title":"0.15.0 — The cloud lab lives in the repository"},
  {"state":"open","open_issues":9,"title":"0.16.0 — Make every claim inspectable"},
  {"state":"closed","open_issues":0,"title":"0.13.0 — Failure is part of the API"},
  {"state":"closed","open_issues":0,"title":"0.12.0 — One contract, same working cloud"},
  {"state":"open","open_issues":5,"title":"1.0 — the local cloud lab"}
]`

// The shape a coherent repository has at the moment preflight runs: themes with
// no number, and the tagged version's own milestone emptied but **still open**,
// because it is closed after the tag and this check happens before it.
//
// That "still open" is load-bearing, and the first version of this fixture had
// it closed. The gate skips a closed milestone before it reaches the exception,
// so the exception was never exercised: the mutation that deletes it reported
// STILL GREEN. The harness caught the test, which is the only reason this
// comment is not a claim.
const milestonesShipped = `[
  {"state":"open","open_issues":14,"title":"Runtime Contract v1 — the topology outranks its history"},
  {"state":"open","open_issues":0,"title":"0.16.0 — Make every claim inspectable"},
  {"state":"closed","open_issues":0,"title":"0.13.0 — Failure is part of the API"}
]`

// TestTheMilestoneGateRefusesAVersionOnAnOpenMilestone is cited by
// milestone-gate.sh.
//
// Both findings are asserted, not just the count: the gate must name WHICH
// milestone and how many issues it holds, because a gate that refuses a tag
// without saying what to fix teaches `--no-verify`, which this repository has
// written down as worse than no gate at all.
func TestTheMilestoneGateRefusesAVersionOnAnOpenMilestone(t *testing.T) {
	code, out := runMilestoneGate(t, "v0.16.0", milestonesOn20261111)
	if code != 1 {
		t.Fatalf("the gate exited %d on the state that asked for it; 1 is incoherent\n%s", code, out)
	}
	for _, want := range []string{
		`"0.14.0 — The topology outranks its history" claims v0.14.0, which is already tagged, and holds 19`,
		`"0.15.0 — The cloud lab lives in the repository" claims v0.15.0, which is already tagged, and holds 10`,
	} {
		if !strings.Contains(out, want) {
			t.Errorf("the refusal does not name %q:\n%s", want, out)
		}
	}
	// The version being tagged has its own open milestone here, and that is a
	// second, distinct finding: a release cannot ship with open work filed
	// under its own number.
	if !strings.Contains(out, "named after the version being tagged and holds 9") {
		t.Errorf("the gate did not notice open work under the version being tagged:\n%s", out)
	}
	// And a milestone for work still AHEAD is not a finding. This is the
	// narrowing that makes the gate usable: a rule reading "no version number
	// on an open milestone" would have refused every tag from the day the 1.0
	// milestone was created, which is the gate-blocks-the-release shape
	// preflight.sh already carries a scar from.
	if strings.Contains(out, "1.0 — the local cloud lab") {
		t.Errorf("the gate refused a milestone for work that has not shipped, so it would block every tag:\n%s", out)
	}
	// A closed milestone carrying a number is the CONVENTION, never a finding.
	if strings.Contains(out, "0.13.0") || strings.Contains(out, "0.12.0") {
		t.Errorf("the gate complained about a closed milestone, which is how a shipped release is recorded:\n%s", out)
	}
}

// TestTheMilestoneGateAcceptsTheShippedShape is the accepting half, in its own
// test so neither can be satisfied alone.
//
// A gate that refused everything would pass the test above and refuse every
// tag — the shape this repository names as the one that teaches `--no-verify`.
// It also pins the narrow exception: the tagged version's milestone may carry
// its number once it is closed and empty.
func TestTheMilestoneGateAcceptsTheShippedShape(t *testing.T) {
	code, out := runMilestoneGate(t, "v0.16.0", milestonesShipped)
	if code != 0 {
		t.Fatalf("the gate exited %d on the shape 0.9.0 to 0.13.0 all had; it would refuse every tag\n%s",
			code, out)
	}
	if !strings.Contains(out, "no open milestone claims a version that has already shipped") {
		t.Errorf("the gate passed without saying what it checked:\n%s", out)
	}
}

// TestTheMilestoneGateSaysWhenItCouldNotMeasure keeps the third exit code
// honest. An absent `gh` and a repository in a bad state are different answers,
// and exit 2 is what lets preflight.sh print "not checked" instead of refusing
// a tag for a reason that is not about the repository.
func TestTheMilestoneGateSaysWhenItCouldNotMeasure(t *testing.T) {
	script, err := filepath.Abs(filepath.Join("..", "release", "milestone-gate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	// An empty PATH removes gh without removing bash, which is run by name.
	cmd := exec.Command("bash", script, "v0.16.0")
	cmd.Env = []string{"PATH=" + t.TempDir()}
	out, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err == nil || !asExitError(err, &exit) || exit.ExitCode() != 2 {
		t.Fatalf("a gate that cannot measure must exit 2, got err=%v\n%s", err, out)
	}
	if !strings.Contains(string(out), "gh is not installed") {
		t.Errorf("the gate did not say why it could not measure:\n%s", out)
	}
}
