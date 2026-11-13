package ci

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// The wrapper around a Terraform command that downloads (#895, #899).
//
// Two defects on one line, and the tests that hold them apart:
//
//   - a transient registry 5xx reddened a conformance leg on a change that
//     touched no route. Measured on 2026-11-12, pull request #893: the same
//     branch and the same commit passed at 15:18 and failed at 15:34 with
//     `github.com: bad response code: 500`, while main passed at 15:34;
//   - the version the engine resolved reached no log, because four callers
//     silenced the init. On run 43271335604 the log carried `scaleway v2.83.0`
//     and `exoscale v0.72.0` from other steps and nothing for the three example
//     stacks, which are applied in that same log.
//
// These drive the real script with a stubbed engine, so the arithmetic is
// exercised without a registry and without waiting a quarter of an hour.

// tfStub writes a fake engine that counts its invocations and behaves as asked.
//
// script is bash, and it receives the attempt number in $n. It exists so a test
// can say "fail the first two attempts the way a CDN does, then succeed"
// without any timing or network.
func tfStub(t *testing.T, dir, script string) (bin, counter string) {
	t.Helper()
	counter = filepath.Join(dir, "attempts")
	if err := os.WriteFile(counter, []byte("0"), 0o600); err != nil {
		t.Fatal(err)
	}
	bin = filepath.Join(dir, "terraform")
	body := `#!/usr/bin/env bash
n=$(cat "` + counter + `")
n=$((n + 1))
printf '%s' "$n" > "` + counter + `"
` + script + `
`
	if err := os.WriteFile(bin, []byte(body), 0o755); err != nil { //nolint:gosec // a stub this test runs
		t.Fatal(err)
	}
	return bin, counter
}

// runRetry runs the wrapper and answers its exit code, its output and how many
// times the engine was invoked.
func runRetry(t *testing.T, script string, args ...string) (code int, out string, attempts int) {
	t.Helper()
	dir := t.TempDir()
	bin, counter := tfStub(t, dir, script)

	cmd := exec.Command("bash",
		append([]string{filepath.Join("..", "..", "tools", "ci", "tf-retry.sh"), bin, "testleg"}, args...)...)
	cmd.Env = append(os.Environ(),
		// The measured budget is ten attempts fifteen seconds apart. A test
		// that waited for it would take two and a half minutes to prove
		// arithmetic, so the interval is a seam; the COUNT stays what the
		// script defaults to unless a case is about the count itself.
		"FEINT_TF_RETRY_INTERVAL=0",
	)
	raw, err := cmd.CombinedOutput()
	var exit *exec.ExitError
	if err != nil {
		if !asExitError(err, &exit) {
			t.Fatalf("run tf-retry.sh: %v\n%s", err, raw)
		}
		code = exit.ExitCode()
	}

	body, readErr := os.ReadFile(counter) //nolint:gosec // a path this test made
	if readErr != nil {
		t.Fatalf("read the attempt counter: %v", readErr)
	}
	attempts, _ = strconv.Atoi(strings.TrimSpace(string(body)))
	return code, string(raw), attempts
}

// A registry 5xx is survived.
//
// This is the measured case: the failure is in the download, before anything is
// created, and the next attempt succeeds. A leg that died on it reported a red
// on the change under review rather than on the weather.
func TestATransientRegistryFailureIsRetried(t *testing.T) {
	code, out, attempts := runRetry(t, `
if [ "$n" -lt 3 ]; then
  echo "Error: Failed to install provider"
  echo "Error while installing scaleway/scaleway v2.83.0: github.com: bad response code: 500"
  exit 1
fi
echo "- Installing scaleway/scaleway v2.83.0..."
`, "init", "-upgrade")

	if code != 0 {
		t.Fatalf("exit %d, want 0: the third attempt succeeded\n%s", code, out)
	}
	if attempts != 3 {
		t.Errorf("the engine ran %d time(s), want 3: two failures then a success", attempts)
	}
	if !strings.Contains(out, "transient registry failure") {
		t.Errorf("the retry is silent, so a slow leg looks like a hung one:\n%s", out)
	}
}

// A failure that is not an outage fails on the first attempt.
//
// The accepting half, and the one that keeps the retry honest: a pinned version
// that does not exist, a provider renamed, a lock file that disagrees. Retrying
// those for two minutes turns a clear error into a slow one, which is the line
// tools/ci/fetch.sh already draws at 404.
func TestAFailureThatIsNotAnOutageIsNotRetried(t *testing.T) {
	for _, message := range []string{
		"Error: Failed to query available provider packages\nno available releases match the given constraints 1.9.9",
		"Error: Invalid provider configuration",
		"Error: Inconsistent dependency lock file",
	} {
		code, out, attempts := runRetry(t, "echo "+strconv.Quote(message)+"\nexit 1\n", "init")
		if code == 0 {
			t.Errorf("a non-transient failure exited 0:\n%s", out)
		}
		if attempts != 1 {
			t.Errorf("%q was retried %d times; it cannot succeed, and the wait hides the message",
				strings.SplitN(message, "\n", 2)[0], attempts)
		}
		if !strings.Contains(out, "not an outage") {
			t.Errorf("the refusal does not say why it did not retry:\n%s", out)
		}
	}
}

// The versions the engine resolved reach the output.
//
// Without this nobody can attribute a green run to a version, which is what
// #899 measured: four callers silenced the init, so the conformance log carried
// no version at all for the three example stacks it applies.
func TestTheResolvedVersionsReachTheOutput(t *testing.T) {
	code, out, _ := runRetry(t, `
echo "Initializing provider plugins..."
echo "- Finding outscale/outscale versions matching \"1.8.1\"..."
echo "- Installing outscale/outscale v1.8.1..."
echo "- Using previously-installed hashicorp/null v3.2.4"
`, "init", "-upgrade")

	if code != 0 {
		t.Fatalf("exit %d\n%s", code, out)
	}
	for _, want := range []string{"outscale/outscale v1.8.1", "hashicorp/null v3.2.4"} {
		if !strings.Contains(out, want) {
			t.Errorf("the output does not name %q, so no green run can be attributed to a version:\n%s",
				want, out)
		}
	}
	// And not the whole log: the callers redirect the rest for a reason, and a
	// wrapper that printed everything would bury the two lines that matter.
	if strings.Contains(out, "Initializing provider plugins") {
		t.Errorf("the wrapper prints the whole init output:\n%s", out)
	}
}

// The budget is the one that was measured, not a new guess.
//
// tools/ci/fetch.sh's header carries it: ten attempts against a 67% failure
// rate fail 1.7% of the time, four fail 19.8%. A wrapper that gave up after
// three would be back where the leg was.
func TestTheRetryBudgetIsTheMeasuredOne(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "tools", "ci", "tf-retry.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(body), "FEINT_TF_RETRY_ATTEMPTS:-10") {
		t.Error("the default attempt count is no longer ten, and ten is what the measurement in " +
			"tools/ci/fetch.sh sized")
	}
	if !strings.Contains(string(body), "FEINT_TF_RETRY_INTERVAL:-15") {
		t.Error("the default interval is no longer fifteen seconds")
	}

	code, out, attempts := runRetry(t, `
echo "Error while installing x: github.com: bad response code: 503"
exit 1
`, "init")
	if code == 0 {
		t.Fatalf("an outage that never clears exited 0:\n%s", out)
	}
	if attempts != 10 {
		t.Errorf("the engine ran %d time(s) against an outage that never clears, want the budget of 10",
			attempts)
	}
}
