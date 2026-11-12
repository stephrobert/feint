package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The undecided backlog, and the shell it is measured in.
//
// ## The defect these tests exist for, which this change shipped once
//
// The arithmetic was a `run:` block in .github/workflows/drift.yml. GitHub runs
// those with `bash -e {0}`, so the first probe to answer 2 — the normal answer,
// the one the step exists to produce — killed the step before the line that
// reads the code. `set -uo pipefail` at the top of the block does not undo the
// -e the shell was started with.
//
// It passed every local check and failed on its first real run. The Go test
// written with it executed the OTHER step's shell, which is the measurement
// mistake this repository names most often: the instrument pointed next to the
// subject.
//
// So the arithmetic moved into tools/ci/drift-backlog.sh, and these tests run
// that file with a stubbed feint, **under `bash -e`**, which is the shell the
// workflow will use.

// backlogStub answers like `feint coverage`: it finds the provider in its
// arguments and exits with the code the test asked for that provider.
const backlogStub = `#!/bin/sh
provider=scaleway
for arg in "$@"; do
  case "${arg}" in
    outscale|exoscale) provider="${arg}" ;;
  esac
done
echo "stub feint: ${provider}"
case "${provider}" in
  scaleway) exit "${STUB_SCALEWAY:-0}" ;;
  outscale) exit "${STUB_OUTSCALE:-0}" ;;
  exoscale) exit "${STUB_EXOSCALE:-0}" ;;
esac
`

// runBacklog executes the real script under `bash -e`, from the repository root
// the way the workflow does, and returns its exit code, its output and the
// key=value pairs it wrote to $GITHUB_OUTPUT.
func runBacklog(t *testing.T, scaleway, outscale, exoscale string) (int, string, map[string]string) {
	t.Helper()

	dir := t.TempDir()
	stub := filepath.Join(dir, "feint")
	if err := os.WriteFile(stub, []byte(backlogStub), 0o755); err != nil { //nolint:gosec // a stub this test runs
		t.Fatal(err)
	}
	output := filepath.Join(dir, "output.txt")
	if err := os.WriteFile(output, nil, 0o600); err != nil {
		t.Fatal(err)
	}

	// `bash -e` and not plain bash: that is what `run:` gets, and the whole
	// reason this file exists. A test that dropped the -e would pass on the
	// defect it was written for.
	cmd := exec.Command("bash", "-e", filepath.Join("tools", "ci", "drift-backlog.sh"), stub)
	cmd.Dir = filepath.Join("..", "..")
	cmd.Env = append(os.Environ(),
		"STUB_SCALEWAY="+scaleway,
		"STUB_OUTSCALE="+outscale,
		"STUB_EXOSCALE="+exoscale,
		"GITHUB_OUTPUT="+output,
		"GITHUB_STEP_SUMMARY="+filepath.Join(dir, "summary.md"),
	)
	out, err := cmd.CombinedOutput()
	code := 0
	var exit *exec.ExitError
	if err != nil {
		if ok := asExitError(err, &exit); ok {
			code = exit.ExitCode()
		} else {
			t.Fatalf("run drift-backlog.sh: %v\n%s", err, out)
		}
	}

	raw, readErr := os.ReadFile(output) //nolint:gosec // a path this test made
	if readErr != nil {
		t.Fatalf("read the step output: %v", readErr)
	}
	written := map[string]string{}
	for _, line := range strings.Split(string(raw), "\n") {
		if key, value, found := strings.Cut(line, "="); found {
			written[key] = value
		}
	}
	return code, string(out), written
}

// One probe answering 2 is a verdict, not a dead step.
//
// This is the regression. Exit 2 is `--fail-on-unknown` saying an operation is
// waiting for a decision — the answer the whole mechanism is built to carry —
// and under `bash -e` the first version of this code died on it.
func TestABacklogProbeExitingTwoStillProducesAVerdict(t *testing.T) {
	code, out, written := runBacklog(t, "2", "0", "0")
	if code != 0 {
		t.Fatalf("the backlog exited %d because a probe answered 2: that is the expected answer, and "+
			"the step died on it under `bash -e`\n%s", code, out)
	}
	if written["verdict"] != "failure" {
		t.Errorf("verdict=%q, want failure: something is undecided and the issue is not opened", written["verdict"])
	}
	if written["health"] != "ok" {
		t.Errorf("health=%q, want ok: every source was measured, one of them found something", written["health"])
	}
	if !strings.Contains(out, "scaleway: operations are waiting for a decision") {
		t.Errorf("the account does not name the provider that is waiting:\n%s", out)
	}
}

// Every provider is asked, and any one of them can carry the finding.
//
// The accepting half of the test above: a probe that only ever looked at
// Scaleway would pass it and leave the other two unmeasured, which reads as two
// providers with nothing undecided.
func TestEachProviderCanCarryTheBacklogOnItsOwn(t *testing.T) {
	for _, c := range []struct{ scaleway, outscale, exoscale string }{
		{"2", "0", "0"},
		{"0", "2", "0"},
		{"0", "0", "2"},
	} {
		_, out, written := runBacklog(t, c.scaleway, c.outscale, c.exoscale)
		if written["verdict"] != "failure" {
			t.Errorf("scaleway=%s outscale=%s exoscale=%s: verdict=%q, want failure\n%s",
				c.scaleway, c.outscale, c.exoscale, written["verdict"], out)
		}
	}
}

// Nothing undecided is the verdict that closes the issue, and it has to be
// reachable: a script that always said `failure` would pass the tests above and
// leave an issue nobody can close.
func TestAQuietBacklogSaysSoAndThatIsWhatClosesTheIssue(t *testing.T) {
	code, out, written := runBacklog(t, "0", "0", "0")
	if code != 0 {
		t.Fatalf("exit %d on a quiet backlog:\n%s", code, out)
	}
	if written["verdict"] != "success" || written["health"] != "ok" {
		t.Errorf("verdict=%q health=%q, want success/ok on a backlog with nothing in it",
			written["verdict"], written["health"])
	}
}

// A probe that could not run is neither a finding nor an absence of one.
//
// Exit 1 is `feint: error`, and 127 is the binary that is not there — the shape
// of the night where the build is what failed, which is exactly when this step
// runs anyway because it is `always()`. Either way the verdict must be `error`:
// `success` would close the drift issue with "Every operation upstream is
// triaged" over a scan that measured nothing.
func TestAnUnmeasurableProbeIsNotNothingUndecided(t *testing.T) {
	for _, code := range []string{"1", "127"} {
		_, out, written := runBacklog(t, code, "0", "0")
		if written["verdict"] != "error" {
			t.Errorf("a probe exiting %s gave verdict=%q, want error\n%s", code, written["verdict"], out)
		}
		if written["health"] == "ok" {
			t.Errorf("a probe exiting %s still reported health=ok, so an unmeasured source passes for a "+
				"measured one", code)
		}
	}
}
