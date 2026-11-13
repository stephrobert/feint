package cli

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// `feint fault` against a real emulator, never a stub.
//
// The verb's whole job is to carry two things faithfully: what is already
// armed, and the sentence the emulator refuses a rule with. A stubbed endpoint
// would let both be whatever the test wrote, which is the measurement this
// repository has been burned by — an instrument agreeing with itself. So every
// test below drives the emulator the binary serves, with its three packs
// mounted and its own validation in force.

// faultLab starts an emulator and answers the --addr to reach it on.
//
// No runtime, no state file, no contracts: /_feint/faults touches none of
// them, and a test that needed a machine would not run in CI.
func faultLab(t *testing.T) string {
	t.Helper()
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return strings.TrimPrefix(ts.URL, "http://")
}

// faultRun drives the binary the way a user does, and answers what they see.
func faultRun(args ...string) (int, string, string) {
	var out, errOut bytes.Buffer
	code := Run(append([]string{"feint"}, args...), &out, &errOut)
	return code, out.String(), errOut.String()
}

// Two operations of one mounted pack. Named here rather than inline so a route
// that moves upstream is renamed once.
const (
	faultOpListServers = "instance/v1/API.ListServers"
	faultOpGetServer   = "instance/v1/API.GetServer"
)

// `add` ADDS. The endpoint underneath it replaces.
//
// PUT /_feint/faults overwrites the whole armed set, so an `add` that simply
// PUT its own rule would disarm every rule set before it — silently, with a
// success message, which is the worst of the three possible behaviours. The
// verb therefore reads the armed set and sends it back with the new rule.
//
// Falsified: with `append(armed.Faults, rule)` replaced by `[]faultRule{rule}`
// in faultAdd, this test fails with
//
//	the second add disarmed what the first armed: 1 rule(s) listed, want 2
func TestAddKeepsWhatWasAlreadyArmed(t *testing.T) {
	addr := faultLab(t)

	if code, _, errOut := faultRun("fault", "add", faultOpListServers, "--addr", addr, "--status", "503"); code != 0 {
		t.Fatalf("the first add exited %d: %s", code, errOut)
	}
	if code, _, errOut := faultRun("fault", "add", faultOpGetServer, "--addr", addr, "--status", "429"); code != 0 {
		t.Fatalf("the second add exited %d: %s", code, errOut)
	}

	code, out, errOut := faultRun("fault", "list", "--addr", addr)
	if code != 0 {
		t.Fatalf("list exited %d: %s", code, errOut)
	}
	armed := 0
	for _, operation := range []string{faultOpListServers, faultOpGetServer} {
		if strings.Contains(out, operation) {
			armed++
		}
	}
	if armed != 2 {
		t.Fatalf("the second add disarmed what the first armed: %d rule(s) listed, want 2.\n%s", armed, out)
	}
	// The rule's own terms, not just its name: a list that printed the
	// operations and lost the statuses would pass the count above while
	// telling an operator nothing about what is armed.
	for _, want := range []string{"status 503", "status 429"} {
		if !strings.Contains(out, want) {
			t.Errorf("list never says %q:\n%s", want, out)
		}
	}
}

// A rule the emulator refuses comes back as the emulator's own sentence, and a
// non-zero exit.
//
// The bounds are not re-implemented here — a second copy of a bound is a copy
// that disagrees with the first within the month — so what this holds is that
// the refusal REACHES the operator readably: the reason, not a JSON document,
// and not exit 0 with a warning.
func TestAddRendersTheApisRefusal(t *testing.T) {
	addr := faultLab(t)

	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{
			name: "an operation nothing serves",
			args: []string{"fault", "add", "no/such/API.Nothing", "--status", "503"},
			says: "no route serves the operation",
		},
		{
			name: "a status the pack renders in no dialect",
			args: []string{"fault", "add", faultOpListServers, "--status", "418"},
			says: "does not render 418",
		},
		{
			name: "nth together with times",
			args: []string{"fault", "add", faultOpListServers, "--status", "503", "--nth", "3", "--times", "2"},
			says: "nth",
		},
		{
			name: "a rule that asks for nothing",
			args: []string{"fault", "add", faultOpListServers},
			says: "asks for nothing",
		},
		{
			name: "a delay longer than the bound",
			args: []string{"fault", "add", faultOpListServers, "--delay", "10m"},
			says: "longer than",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := faultRun(append(tc.args, "--addr", addr)...)
			if code != exitError {
				t.Fatalf("exited %d, want %d: %s%s", code, exitError, out, errOut)
			}
			if !strings.Contains(errOut, tc.says) {
				t.Errorf("the refusal never says %q; the operator read:\n%s", tc.says, errOut)
			}
			if strings.Contains(errOut, `{"error"`) {
				t.Errorf("the refusal reached the operator as raw JSON:\n%s", errOut)
			}
		})
	}

	// And nothing was armed by any of them: a refusal that half-wrote the set
	// would leave a campaign nobody asked for.
	code, out, _ := faultRun("fault", "list", "--addr", addr)
	if code != exitOK {
		t.Fatalf("list exited %d, want 0", code)
	}
	if !strings.Contains(out, "no fault is armed") {
		t.Fatalf("a refused rule was armed anyway:\n%s", out)
	}
}

// An emulator with nothing armed says so, out loud, and exits 0.
//
// Silence would be the wrong answer twice: it reads the same as an emulator
// that did not understand the question, and the one question an operator
// meeting a 500 asks is whether anything was staged at all.
func TestListOnAnUnarmedEmulatorDoesNotLie(t *testing.T) {
	addr := faultLab(t)

	code, out, errOut := faultRun("fault", "list", "--addr", addr)
	if code != exitOK {
		t.Fatalf("list exited %d, want 0: %s", code, errOut)
	}
	if !strings.Contains(out, "no fault is armed") {
		t.Errorf("list says nothing about an empty set:\n%s", out)
	}
	if strings.Contains(out, "fired") {
		t.Errorf("list reports a receipt on an emulator where nothing fired:\n%s", out)
	}
}

// `clear` returns to the state every emulator starts in.
func TestClearReturnsToTheStartingState(t *testing.T) {
	addr := faultLab(t)

	before, _, _ := faultRun("fault", "list", "--addr", addr)
	if before != exitOK {
		t.Fatalf("list exited %d before anything was armed", before)
	}
	if code, _, errOut := faultRun("fault", "add", faultOpListServers, "--addr", addr, "--status", "503"); code != 0 {
		t.Fatalf("add exited %d: %s", code, errOut)
	}
	if code, out, errOut := faultRun("fault", "clear", "--addr", addr); code != exitOK {
		t.Fatalf("clear exited %d: %s%s", code, out, errOut)
	}

	code, out, errOut := faultRun("fault", "list", "--addr", addr)
	if code != exitOK {
		t.Fatalf("list exited %d after clear: %s", code, errOut)
	}
	if !strings.Contains(out, "no fault is armed") || strings.Contains(out, faultOpListServers) {
		t.Fatalf("clear left something armed:\n%s", out)
	}
}

// The usage refusals, and the exit codes the repository's rule 9 fixes: 0 for
// success, 1 for an error, never 2 — that one is drift and the CI reads it.
func TestFaultRefusesWhatItCannotDo(t *testing.T) {
	for _, tc := range []struct {
		name string
		args []string
		says string
	}{
		{"no subcommand", []string{"fault"}, "needs a subcommand"},
		{"an unknown subcommand", []string{"fault", "nonsense"}, "unknown fault subcommand"},
		{"add without an operation", []string{"fault", "add"}, "name one operation"},
		{"add with a flag where the operation goes", []string{"fault", "add", "--status", "503"}, "name one operation"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			code, out, errOut := faultRun(tc.args...)
			if code != exitError {
				t.Fatalf("exited %d, want %d: %s%s", code, exitError, out, errOut)
			}
			if !strings.Contains(errOut, tc.says) {
				t.Errorf("the refusal never says %q:\n%s", tc.says, errOut)
			}
		})
	}
}

// An emulator that is not there is said so, rather than reported as an empty
// set — which is what a bare decode of a failed call would produce, and would
// read as "nothing is armed".
func TestFaultSaysWhenNoEmulatorAnswers(t *testing.T) {
	// A port nothing listens on: the lab is started and immediately closed, so
	// the address is one no other test can claim meanwhile.
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	addr := strings.TrimPrefix(ts.URL, "http://")
	ts.Close()

	for _, args := range [][]string{
		{"fault", "list", "--addr", addr},
		{"fault", "clear", "--addr", addr},
		{"fault", "add", faultOpListServers, "--addr", addr, "--status", "503"},
	} {
		code, out, errOut := faultRun(args...)
		if code != exitError {
			t.Fatalf("%v exited %d, want %d: %s%s", args, code, exitError, out, errOut)
		}
		if !strings.Contains(errOut, "did not answer") {
			t.Errorf("%v failed without saying the emulator is not there:\n%s", args, errOut)
		}
		if strings.Contains(out, "no fault is armed") {
			t.Errorf("%v reported an empty set from an emulator that never answered:\n%s", args, out)
		}
	}
}

// The receipt is what the verb exists to read back: a rule that fired on the
// third call and on no other, said in a line an operator can read.
//
// Driven through the emulator rather than asserted on a fixture: the rank is
// counted by the core, and a test that composed the receipt itself would prove
// only that this file can write JSON.
func TestListReadsBackWhichCallWasHit(t *testing.T) {
	addr := faultLab(t)

	if code, _, errOut := faultRun("fault", "add", faultOpListServers,
		"--addr", addr, "--status", "503", "--nth", "3"); code != 0 {
		t.Fatalf("add exited %d: %s", code, errOut)
	}

	// Three calls to the operation the rule names. Only the third is answered
	// by the rule; the first two reach the handler.
	for i := 0; i < 3; i++ {
		res, err := http.Get("http://" + addr + "/instance/v1/zones/fr-par-1/servers")
		if err != nil {
			t.Fatalf("call %d: %v", i+1, err)
		}
		_ = res.Body.Close()
	}

	code, out, errOut := faultRun("fault", "list", "--addr", addr)
	if code != exitOK {
		t.Fatalf("list exited %d: %s", code, errOut)
	}
	if !strings.Contains(out, "on call 3 only") {
		t.Errorf("list never says the rule names one call:\n%s", out)
	}
	if !strings.Contains(out, "1 hit(s) of 3 call(s)") {
		t.Errorf("list does not separate what the rule saw from what it answered:\n%s", out)
	}
	// The colon arrived with #925, when the three renderings of a receipt line
	// were merged into describeFiredLine: every line now reads
	// "<operation> call <n>: <what happened>", whatever happened.
	if !strings.Contains(out, faultOpListServers+" call 3: answered 503") {
		t.Errorf("the receipt never names the call that was hit:\n%s", out)
	}
}

// `--truncate` was wired and frozen into the CLI surface, and nothing drove it
// end to end: the flag could have been parsed, accepted by the emulator and
// never reached the wire, and every test would still have passed. The bound is
// tested on the emulator's side; what was missing is the journey of the flag.
//
// So this one reads the body a real client gets.
func TestTruncateReachesTheWire(t *testing.T) {
	addr := faultLab(t)

	full, err := http.Get("http://" + addr + "/instance/v1/zones/fr-par-1/servers")
	if err != nil {
		t.Fatal(err)
	}
	whole, err := io.ReadAll(full.Body)
	_ = full.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(whole) < 8 {
		t.Fatalf("the untruncated answer is %d byte(s); there is nothing to cut and this test would "+
			"pass on an empty body", len(whole))
	}

	if code, _, errOut := faultRun("fault", "add", faultOpListServers,
		"--addr", addr, "--truncate", "4"); code != 0 {
		t.Fatalf("add exited %d: %s", code, errOut)
	}

	cut, err := http.Get("http://" + addr + "/instance/v1/zones/fr-par-1/servers")
	if err != nil {
		t.Fatal(err)
	}
	short, err := io.ReadAll(cut.Body)
	_ = cut.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	if len(short) != 4 {
		t.Errorf("the answer is %d byte(s), want 4: --truncate did not reach the wire\n%q",
			len(short), short)
	}
	if string(short) != string(whole[:4]) {
		t.Errorf("the truncated answer %q is not the first 4 bytes of %q", short, whole[:4])
	}
}
