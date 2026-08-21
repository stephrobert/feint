// Package conformance holds the tests of the shell the conformance suites
// share. The suites themselves need real clients, a real emulator and, for the
// ssh ones, a real machine runtime; their preconditions do not, and those are
// exactly the part that has to hold on a host where nothing is set up yet.
package conformance

import (
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// The ssh suites ask whether the emulator can keep the promise they are about
// to make, before they make it (#335).
//
// runtime-proof.yml failed on the "Scaleway ssh suite" step on five consecutive
// scheduled nights, 2026-08-16 to 2026-08-20. The cause was in its own log every
// night:
//
//	WARN no image of ours for this system, booting the upstream one …
//	     fix="feint images"
//
// Nothing ran it. The suite started anyway, registered a key, booted a server,
// waited for an address, and failed ninety seconds later on "no ssh daemon
// answered … the published address is a promise nobody keeps" — a message that
// blames the address, the network or the daemon, and names none of the three
// correctly. Reproduced on 2026-08-20 by hiding the five feint/* aliases on a
// host that held them: 21s green with them, 93s red without, same message as CI.
//
// #125 promotes this workflow to pull_request after a run of consecutive green
// scheduled nights, so while this stayed broken that counter was pinned at zero
// by construction.
//
// The guard is shell because the suites are shell, and this is the test that
// makes it a control rather than a paragraph: it drives guard_images itself,
// against a stub emulator and a stub binary, and asserts what it does in each of
// the four situations it can meet. tools/falsify/specs/ssh-suite-needs-its-images.json
// replays it with the guard neutralised.
func TestTheImageGuardRefusesASuiteWhoseMachinesCannotAnswer(t *testing.T) {
	// A runtime is on and the binary reports missing images: refuse, and name
	// the command. This is the CI case, and the one that cost five nights.
	code, output := runGuard(t, `{"machines":"incus"}`, 2)
	if code == 0 {
		t.Errorf("the guard passed while the runtime holds no image:\n%s", output)
	}
	for _, want := range []string{"feint images", "incus", "port 22"} {
		if !strings.Contains(output, want) {
			t.Errorf("the refusal never says %q, so it does not name what is missing:\n%s", want, output)
		}
	}
}

// The other three situations, each asserted rather than assumed. A precondition
// that only knows how to fail is as useless as one that only knows how to pass.
func TestTheImageGuardLetsAPreparedRunThrough(t *testing.T) {
	code, output := runGuard(t, `{"machines":"incus"}`, 0)
	if code != 0 {
		t.Errorf("the guard refused a host that holds every image (exit %d):\n%s", code, output)
	}
}

// With no runtime the suites skip their login step by design, and there is no
// image to hold. The guard must not ask the binary at all: the stub here exits 2
// on any call, so a guard that asked would fail this.
func TestTheImageGuardAsksNothingWhenNoRuntimeIsOn(t *testing.T) {
	code, output := runGuard(t, `{"machines":"none"}`, 2)
	if code != 0 {
		t.Errorf("the guard refused a --vm off emulator (exit %d):\n%s", code, output)
	}
	if !strings.Contains(output, "not needed") {
		t.Errorf("the guard passed in silence; a precondition that says nothing on the case "+
			"it exists for reads as green when it never ran:\n%s", output)
	}
}

// An endpoint that answers nothing is not an endpoint with no runtime, and the
// difference is the whole point: the first is a broken harness, the second a
// deliberate mode. Answering "fine" to the first is how a suite proves nothing
// and says it passed.
func TestTheImageGuardRefusesAnEndpointThatAnswersNothing(t *testing.T) {
	code, output := runGuard(t, "", 0)
	if code == 0 {
		t.Errorf("the guard passed against an emulator that answered no health payload:\n%s", output)
	}
}

// The binary is the subject of the question, so its absence is an error and
// never a reason to skip. A guard that shrugs when it cannot look is the defect
// this repository keeps naming.
func TestTheImageGuardRefusesWhenItCannotAsk(t *testing.T) {
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"machines":"incus"}`)
	}))
	defer health.Close()

	code, output := bashGuard(t, `. "$1"; guard_images "$2"`, health.URL, filepath.Join(t.TempDir(), "no-such-feint"))
	if code == 0 {
		t.Errorf("the guard passed with no binary to ask:\n%s", output)
	}
	if !strings.Contains(output, "no feint binary") {
		t.Errorf("the refusal does not say the binary is what is missing:\n%s", output)
	}
}

// The call sites, because a shared guard nobody calls guards nothing.
//
// Structural on purpose, and it is the weaker half of the pair: it reads the
// three scripts rather than running them, which is a document check. What makes
// it worth having is that the behavioural half above cannot see a deleted call,
// and a deleted call is exactly how the suites were before #335.
func TestEverySuiteThatLogsInAsksForItsImages(t *testing.T) {
	suites := []string{
		filepath.Join("scaleway", "ssh.sh"),
		filepath.Join("outscale", "ssh.sh"),
		filepath.Join("exoscale", "ssh.sh"),
	}
	for _, suite := range suites {
		body, err := os.ReadFile(suite) //nolint:gosec // a fixed path in this directory
		if err != nil {
			t.Fatalf("read %s: %v", suite, err)
		}
		// A line of its own, so the call is a statement rather than a mention.
		// The first version of this test matched the name anywhere in the file,
		// and the falsification caught it at once: `true || guard_images
		// "$ENDPOINT"` still contains the name and calls nothing.
		called := false
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), `guard_images "`) {
				called = true
				break
			}
		}
		if !called {
			t.Errorf("%s logs into a machine and never calls guard_images on a line of its own: "+
				"it will start without its images and fail on an ssh timeout that names nothing", suite)
		}
	}
}

// runGuard drives guard_images against a stub emulator and a stub binary.
//
// The stub binary is what makes this run anywhere: `feint images --check` needs
// an Incus daemon, and the guard's own behaviour is what is under test, not the
// inventory's. Its exit code is the one thing the guard reads.
func runGuard(t *testing.T, healthBody string, binaryExit int) (int, string) {
	t.Helper()

	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if healthBody == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, healthBody)
	}))
	defer health.Close()

	return bashGuard(t, `. "$1"; guard_images "$2"`, health.URL, stubBinary(t, binaryExit))
}

// bashGuard sources the real guard.sh and calls one of its functions in a
// subshell, so what is measured is the shell the suites source and not a copy
// of it. The script and its single argument are passed in because the guards
// differ in what they take: an endpoint for the ones that ask an emulator, a
// runtime name for the one the conformance task runs before an emulator exists.
func bashGuard(t *testing.T, script, argument, binary string) (int, string) {
	t.Helper()
	requireTool(t, "bash")
	requireTool(t, "curl")
	requireTool(t, "jq")

	guard, err := filepath.Abs("guard.sh")
	if err != nil {
		t.Fatalf("locate guard.sh: %v", err)
	}
	if _, err := os.Stat(guard); err != nil {
		t.Fatalf("guard.sh is not where this test looks (%s): %v", guard, err)
	}

	cmd := exec.Command("bash", "-c", script, "bash", guard, argument) //nolint:gosec // fixed script, test-controlled arguments
	cmd.Env = append(os.Environ(), "FEINT_BIN="+binary)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run the guard: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out)
}

// requireTool fails rather than skips. The suites this guard belongs to cannot
// run without curl and jq either, so a host without them is a host where the
// question "does the guard bite" has no answer — and a skip would report that
// as one.
func requireTool(t *testing.T, name string) {
	t.Helper()
	if _, err := exec.LookPath(name); err != nil {
		t.Fatalf("%s is not on PATH: the conformance suites need it, so this test cannot "+
			"be skipped into looking green", name)
	}
}

// The suites also ask whether the host is already holding the blocks they are
// about to take (#375).
//
// The measurement behind it: the runtime leg of `mise run evidence:update`
// failed three times in a row on a dnsmasq an earlier run had left holding a
// gateway address. The sweep in network.sh found it, named it, printed
// `sudo kill <pid>` and exited 1 — the process belongs to the `incus` user and
// the operator running the suite may not signal it. Nothing ran the remedy, so
// every run died in the same place until a human read the log.
//
// The refusal below is what replaces that, and what it refuses to do is as
// important as what it does: it never escalates. The stub binary here stands in
// for `feint clean --check`, whose exit code is the one fact the guard reads.
func TestTheLeftoverGuardRefusesAHostItCannotClean(t *testing.T) {
	code, output := runLeftoverGuard(t, "incus", 1)
	if code == 0 {
		t.Errorf("the guard let a run start on a host whose block is held:\n%s", output)
	}
	for _, want := range []string{"sudo", "clean --vm incus", "cannot end it"} {
		if !strings.Contains(output, want) {
			t.Errorf("the refusal never says %q, so it does not say what to do:\n%s", want, output)
		}
	}
	// And it must never suggest that the suite do the elevating. The command is
	// printed for the operator to run; nothing in this path may acquire a
	// privilege it did not have.
	if strings.Contains(output, "sudo kill") {
		t.Errorf("the refusal falls back to a command with a pid to retype:\n%s", output)
	}
}

// The accepting half: a host the check reports clean starts its run. A guard
// that only knows how to refuse is as useless as one that only knows how to
// pass, and this one sits in front of every runtime suite.
func TestTheLeftoverGuardLetsAPreparedHostThrough(t *testing.T) {
	code, output := runLeftoverGuard(t, "incus", 0)
	if code != 0 {
		t.Errorf("the guard refused a host with nothing left behind (exit %d):\n%s", code, output)
	}
}

// The poorest run this guard will ever meet, and the one it must not touch.
//
// `FEINT_VM=off` is the default, the first leg of `mise run evidence:update`
// and the whole of CI's conformance matrix: nothing there takes an address
// block, so nothing there can be blocked by one being held. The stub exits 1 on
// any call, so a guard that asked would fail this — which is the point, since
// the cost of getting it wrong is a brand new red on the path that was never
// broken, and a doorstep that fires on healthy runs is a doorstep somebody
// disarms.
func TestTheLeftoverGuardAsksNothingWhenNoRuntimeIsOn(t *testing.T) {
	for _, machines := range []string{"none", "off", "null", ""} {
		code, output := runLeftoverGuardFor(t, machines, 1)
		if code != 0 {
			t.Errorf("machines=%q: the guard refused a run that starts no machine (exit %d):\n%s",
				machines, code, output)
		}
		if !strings.Contains(output, "not asked") {
			t.Errorf("machines=%q: the guard passed in silence; a precondition that says nothing "+
				"on the case it exists for reads as green when it never ran:\n%s", machines, output)
		}
	}
}

// An endpoint that answers nothing is a broken harness, not a runtime-free
// mode, and the two must not read the same.
func TestTheLeftoverGuardRefusesAnEndpointThatAnswersNothing(t *testing.T) {
	code, output := runLeftoverGuard(t, "", 0)
	if code == 0 {
		t.Errorf("the guard passed against an emulator that answered no health payload:\n%s", output)
	}
}

// The call sites, because a shared guard nobody calls guards nothing — the same
// weaker-but-necessary structural half TestEverySuiteThatLogsInAsksForItsImages
// holds for #335.
//
// The mise task is in the list, and it is the entry that closes #375 rather
// than merely improving it: the suites below are the twelfth step of
// `mise run conformance`, so a refusal that lives only in them still lets the
// leg burn every client suite first. The answer has not changed since second
// zero, so the task asks at second zero.
func TestEveryRuntimeSuiteAsksAboutLeftovers(t *testing.T) {
	suites := []string{
		filepath.Join("scaleway", "network.sh"),
		filepath.Join("outscale", "network.sh"),
		filepath.Join("exoscale", "network.sh"),
	}
	for _, suite := range suites {
		body, err := os.ReadFile(suite) //nolint:gosec // a fixed path in this directory
		if err != nil {
			t.Fatalf("read %s: %v", suite, err)
		}
		// A line of its own, so the call is a statement rather than a mention:
		// `true || guard_leftovers "$ENDPOINT"` still contains the name and
		// calls nothing. The sibling test above matched the name anywhere in
		// the file when it was first written, and its own falsification caught
		// that at once; this one starts where that one ended up.
		called := false
		for _, line := range strings.Split(string(body), "\n") {
			if strings.HasPrefix(strings.TrimSpace(line), `guard_leftovers "`) {
				called = true
				break
			}
		}
		if !called {
			t.Errorf("%s takes an address block and never calls guard_leftovers on a line of its "+
				"own: it will start on a host whose block is already held and die in the sweep", suite)
		}
	}

	task, err := os.ReadFile(filepath.Join("..", "..", "mise.toml")) //nolint:gosec // a fixed path in this repository
	if err != nil {
		t.Fatalf("read mise.toml: %v", err)
	}
	asked := false
	for _, line := range strings.Split(string(task), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "tools/conformance/guard.sh leftovers ") {
			asked = true
			break
		}
	}
	if !asked {
		t.Error("the conformance task never runs guard.sh leftovers on a line of its own: the leg " +
			"goes back to burning every client suite before it says the host was never ready")
	}
}

// runLeftoverGuard drives guard_leftovers against a stub emulator and a stub
// binary, the way runGuard does for the image guard: `feint clean --check`
// needs a real /proc survey and a real Incus, and the guard's own behaviour is
// what is under test, not the survey's.
func runLeftoverGuard(t *testing.T, machines string, binaryExit int) (int, string) {
	t.Helper()
	body := ""
	if machines != "" {
		body = fmt.Sprintf(`{"machines":%q}`, machines)
	}
	health := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		if body == "" {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, body)
	}))
	defer health.Close()

	return bashGuard(t, `. "$1"; guard_leftovers "$2"`, health.URL, stubBinary(t, binaryExit))
}

// runLeftoverGuardFor drives the entry point the conformance task uses, which
// takes the mode directly because it runs before any emulator exists.
func runLeftoverGuardFor(t *testing.T, machines string, binaryExit int) (int, string) {
	t.Helper()
	return bashGuard(t, `. "$1"; guard_leftovers_for "$2"`, machines, stubBinary(t, binaryExit))
}

// stubBinary is a fake feint whose exit code is the one thing the guard reads.
func stubBinary(t *testing.T, exit int) string {
	t.Helper()
	stub := filepath.Join(t.TempDir(), "feint")
	script := fmt.Sprintf("#!/bin/sh\necho \"stub feint: $*\" >&2\nexit %d\n", exit)
	if err := os.WriteFile(stub, []byte(script), 0o700); err != nil { //nolint:gosec // a stub in a test temp dir
		t.Fatalf("write the stub binary: %v", err)
	}
	return stub
}
