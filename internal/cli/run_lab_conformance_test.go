//go:build conformance

package cli

import (
	"encoding/json"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The lifecycle half of `feint run` (#848), against the BUILT BINARY.
//
// It is here and not in a unit test for a reason that is not taste: `start`
// re-execs os.Executable(), and under `go test` that is the test binary. A
// unit test of the spawn would run the suite again with `serve` arguments, so
// the only honest subject is the command a user actually runs.
//
// What each test asserts is a property a reader of the receipt would rely on,
// and the receipt is read as JSON rather than scraped from prose, which is
// #848's own requirement.

// feintBinary builds the command under test once per test.
func feintBinary(t *testing.T) string {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "feint")
	build := exec.CommandContext(t.Context(), "go", "build", "-o", binary, "../../cmd/feint")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build feint: %v\n%s", err, out)
	}
	return binary
}

// labRun runs the command and splits its streams, because the contract is that
// stdout carries the receipt and nothing else.
func labRun(t *testing.T, binary, workdir, tmpdir string, args ...string) (int, string, string) {
	t.Helper()
	cmd := exec.CommandContext(t.Context(), binary, args...)
	cmd.Dir = workdir
	// A TMPDIR of its own, so that "the lab left nothing behind" can be
	// asserted by looking at a directory this test owns rather than at the
	// machine's /tmp, where another run's leftovers would make it flaky in the
	// direction that hides a defect.
	cmd.Env = append(os.Environ(), "TMPDIR="+tmpdir)
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	err := cmd.Run()
	code := 0
	var exit *exec.ExitError
	switch {
	case err == nil:
	case errorsAs(err, &exit):
		code = exit.ExitCode()
	default:
		t.Fatalf("run %v: %v\n%s", args, err, stderr.String())
	}
	return code, stdout.String(), stderr.String()
}

func errorsAs(err error, target **exec.ExitError) bool {
	exit, ok := err.(*exec.ExitError)
	if ok {
		*target = exit
	}
	return ok
}

func listening(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	_ = conn.Close()
	return true
}

func hostPort(t *testing.T, endpoint string) string {
	t.Helper()
	return strings.TrimPrefix(strings.TrimPrefix(endpoint, "http://"), "https://")
}

// TestARunOwnsItsLabAndLeavesNothing is cited by startOwnedLab.
//
// Four properties in one run, because they are only meaningful together: the
// lab exists while the child runs, the child is TOLD where it is through the
// same variables `feint env` prints, the lab is gone afterwards, and the
// bytes it wrote are gone with it. Any three of them without the fourth is a
// command that looks clean and leaks.
func TestARunOwnsItsLabAndLeavesNothing(t *testing.T) {
	binary := feintBinary(t)
	work, tmp := t.TempDir(), t.TempDir()

	code, stdout, stderr := labRun(t, binary, work, tmp,
		"run", "--json", "--provider", "scaleway", "--",
		"sh", "-c", `printf '%s' "$SCW_API_URL" > seen.txt`)
	if code != 0 {
		t.Fatalf("the run failed (%d)\nstdout:%s\nstderr:%s", code, stdout, stderr)
	}

	var receipt runReceipt
	if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
		t.Fatalf("stdout is not the receipt alone: %v\n%s", err, stdout)
	}
	if receipt.Lab != "owned" || receipt.Cleanup != "clean" {
		t.Errorf("lab=%q cleanup=%q", receipt.Lab, receipt.Cleanup)
	}
	if receipt.SchemaVersion != runReceiptSchemaVersion {
		t.Errorf("receipt schema %d", receipt.SchemaVersion)
	}
	if len(receipt.Providers) != 1 || receipt.Providers[0] != "scaleway" {
		t.Errorf("--provider scaleway served %v", receipt.Providers)
	}

	seen, err := os.ReadFile(filepath.Join(work, "seen.txt"))
	if err != nil {
		t.Fatalf("the child wrote nothing: %v", err)
	}
	if string(seen) != receipt.Endpoint {
		t.Errorf("the child was pointed at %q and the receipt names %q", seen, receipt.Endpoint)
	}
	if !strings.HasPrefix(string(seen), "http://127.0.0.1:") {
		t.Errorf("the child was pointed somewhere that is not a local lab: %q", seen)
	}

	if listening(hostPort(t, receipt.Endpoint)) {
		t.Errorf("the lab on %s is still up after a clean run", receipt.Endpoint)
	}
	entries, err := os.ReadDir(tmp)
	if err != nil {
		t.Fatalf("read the run's TMPDIR: %v", err)
	}
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), "feint-lab-") || strings.HasPrefix(entry.Name(), runDirPrefix) {
			t.Errorf("%s outlived the run", entry.Name())
		}
	}
}

// TestARunKeepsAFailedLabOnlyWhenAsked is cited by ownedLab.keep.
//
// Both directions, because the dangerous one is the default: a command that
// kept every failed run's emulator would leave a port and a directory behind
// on each red test, and the operator would never be told. And the exit code is
// asserted on both: a wrapper that swallowed the child's failure would be
// worse than no wrapper.
func TestARunKeepsAFailedLabOnlyWhenAsked(t *testing.T) {
	binary := feintBinary(t)

	t.Run("without the flag the lab goes, and the code is the child's", func(t *testing.T) {
		work, tmp := t.TempDir(), t.TempDir()
		code, stdout, stderr := labRun(t, binary, work, tmp,
			"run", "--json", "--", "sh", "-c", "exit 3")
		if code != 3 {
			t.Fatalf("the child's exit 3 became %d\nstderr:%s", code, stderr)
		}
		var receipt runReceipt
		if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
			t.Fatalf("stdout is not the receipt alone: %v\n%s", err, stdout)
		}
		if receipt.Cleanup != "clean" {
			t.Errorf("a failed run reported cleanup=%q", receipt.Cleanup)
		}
		if receipt.Exit != 3 {
			t.Errorf("the receipt reports exit %d", receipt.Exit)
		}
		if listening(hostPort(t, receipt.Endpoint)) {
			t.Errorf("the lab on %s survived a failure nobody asked to keep", receipt.Endpoint)
		}
	})

	t.Run("with the flag it stays, and the follow-up command is printed", func(t *testing.T) {
		work, tmp := t.TempDir(), t.TempDir()
		code, stdout, stderr := labRun(t, binary, work, tmp,
			"run", "--json", "--keep-on-failure", "--", "sh", "-c", "exit 3")
		if code != 3 {
			t.Fatalf("the child's exit 3 became %d\nstderr:%s", code, stderr)
		}
		var receipt runReceipt
		if err := json.Unmarshal([]byte(stdout), &receipt); err != nil {
			t.Fatalf("stdout is not the receipt alone: %v\n%s", err, stdout)
		}
		if receipt.Cleanup != "kept" {
			t.Fatalf("cleanup=%q with --keep-on-failure", receipt.Cleanup)
		}
		addr := hostPort(t, receipt.Endpoint)
		if !listening(addr) {
			t.Errorf("--keep-on-failure kept nothing: %s answers nobody", receipt.Endpoint)
		}
		// Stop it however the test ends, including on a failure above.
		defer func() {
			stopped := exec.Command(binary, "stop", "--addr", addr)
			stopped.Env = append(os.Environ(), "TMPDIR="+tmp)
			if out, err := stopped.CombinedOutput(); err != nil {
				t.Errorf("could not stop the kept lab: %v\n%s", err, out)
			}
		}()

		// The exact command, not a description of it: an operator debugging a
		// failed run should not have to reconstruct a temporary path.
		if !strings.Contains(stderr, "feint stop --addr "+addr) {
			t.Errorf("the follow-up command is not printed:\n%s", stderr)
		}

		// And it is a real emulator, not a port held open.
		client := &http.Client{Timeout: 5 * time.Second}
		res, err := client.Get(receipt.Endpoint + "/_feint/health")
		if err != nil {
			t.Fatalf("the kept lab does not answer: %v", err)
		}
		defer func() { _ = res.Body.Close() }()
		if res.StatusCode != http.StatusOK {
			t.Errorf("the kept lab answers %d on /_feint/health", res.StatusCode)
		}
	})
}

// TestARunRemovesAMeasuredRerouteFromItsChild is cited by hazardVariables,
// from the only place the property is observable: inside the child.
//
// OSC_PROFILE is the measured case (#342): with it set, the Outscale provider
// 1.1.x reads ~/.osc/config.json and ignores the endpoint export entirely, so
// a run that looks local reaches the real cloud with that profile's
// credentials. Overriding the endpoint does not help; the variable has to go.
func TestARunRemovesAMeasuredRerouteFromItsChild(t *testing.T) {
	binary := feintBinary(t)
	work, tmp := t.TempDir(), t.TempDir()

	cmd := exec.CommandContext(t.Context(), binary, "run", "--json", "--",
		"sh", "-c", `printf '%s' "${OSC_PROFILE-<absent>}" > seen.txt`)
	cmd.Dir = work
	cmd.Env = append(os.Environ(), "TMPDIR="+tmp, "OSC_PROFILE=production")
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("run: %v\nstderr:%s", err, stderr.String())
	}

	seen, err := os.ReadFile(filepath.Join(work, "seen.txt"))
	if err != nil {
		t.Fatalf("the child wrote nothing: %v", err)
	}
	if string(seen) != "<absent>" {
		t.Errorf("OSC_PROFILE reached the child as %q; a run that looks local would reach the real cloud", seen)
	}

	var receipt runReceipt
	if err := json.Unmarshal([]byte(stdout.String()), &receipt); err != nil {
		t.Fatalf("stdout is not the receipt alone: %v\n%s", err, stdout.String())
	}
	if !contains(receipt.Stripped, "OSC_PROFILE") {
		t.Errorf("the receipt does not say what it removed: %v", receipt.Stripped)
	}
}
