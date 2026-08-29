package cli

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// `feint evidence baseline` / `feint evidence verify` (#488).
//
// What is under test is the promise a consumer outside this repository relies
// on: **feint stopped proving what this project was relying on**, said at the
// moment it happens rather than discovered from the outside three releases
// later, which is what #325 and #326 were.

// artefactWith writes an evidence artefact the two subcommands read.
func artefactWith(t *testing.T, machines []string, operations map[string]map[string]any) string {
	t.Helper()
	doc := map[string]any{
		"format":         "feint-evidence",
		"version":        3,
		"machines":       machines,
		"generated_from": map[string]string{"contracts": "aa", "shapes": "bb", "suites": "cc"},
		"operations":     operations,
	}
	encoded, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	path := filepath.Join(t.TempDir(), "evidence.json")
	if err := os.WriteFile(path, encoded, 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	return path
}

func proven(shape string) map[string]any {
	return map[string]any{
		"driven": true, "probed": "response", "contract": "clean",
		"dataplane": true, "shape": shape, "behaviour": true, "negative": true,
	}
}

// The whole point, in one exchange: an operation whose shape went from a
// recording to `unobserved` fails, naming the operation and both values.
func TestVerifyFailsWhenAPinnedLevelIsNoLongerDelivered(t *testing.T) {
	pinned := artefactWith(t, []string{"incus", "none"}, map[string]map[string]any{
		"instance/v1/API.CreateServer": proven("observed"),
	})
	baseline := filepath.Join(t.TempDir(), "pin.json")
	var out, errOut bytes.Buffer
	if code := evidenceCapture([]string{"--evidence", pinned, "--out", baseline}, &out, &errOut); code != exitOK {
		t.Fatalf("capture exited %d: %s%s", code, out.String(), errOut.String())
	}

	// The same operation, one axis withdrawn.
	regressed := artefactWith(t, []string{"incus", "none"}, map[string]map[string]any{
		"instance/v1/API.CreateServer": proven("unobserved"),
	})
	out.Reset()
	errOut.Reset()
	code := evidenceVerify([]string{"--baseline", baseline, "--evidence", regressed}, &out, &errOut)
	if code != exitDrift {
		t.Fatalf("verify exited %d, want %d (drift): %s%s", code, exitDrift, out.String(), errOut.String())
	}
	for _, want := range []string{"instance/v1/API.CreateServer", "shape", "observed", "unobserved"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the refusal never names %q:\n%s", want, errOut.String())
		}
	}

	// The accepting half, and it is not ceremony: a verify that failed on
	// everything would pass every planted defect above and stop anybody using it.
	out.Reset()
	errOut.Reset()
	if code := evidenceVerify([]string{"--baseline", baseline, "--evidence", pinned}, &out, &errOut); code != exitOK {
		t.Fatalf("verify exited %d on an unchanged artefact: %s%s", code, out.String(), errOut.String())
	}
}

// An operation that disappeared entirely is the strongest fall there is, and it
// must not read as "no verdict to compare".
func TestVerifyCallsAVanishedOperationARegression(t *testing.T) {
	pinned := artefactWith(t, []string{"incus"}, map[string]map[string]any{
		"instance/v1/API.CreateServer": proven("observed"),
	})
	baseline := filepath.Join(t.TempDir(), "pin.json")
	var out, errOut bytes.Buffer
	if code := evidenceCapture([]string{"--evidence", pinned, "--out", baseline}, &out, &errOut); code != exitOK {
		t.Fatalf("capture exited %d", code)
	}

	gone := artefactWith(t, []string{"incus"}, map[string]map[string]any{
		"instance/v1/API.ListServers": proven("observed"),
	})
	out.Reset()
	errOut.Reset()
	if code := evidenceVerify([]string{"--baseline", baseline, "--evidence", gone}, &out, &errOut); code != exitDrift {
		t.Fatalf("an operation that vanished was not a regression (exit %d)", code)
	}
	if !strings.Contains(errOut.String(), "absent") {
		t.Errorf("the refusal does not say the operation is gone:\n%s", errOut.String())
	}
}

// Claims are MEANT to be withdrawn here — #475, #481 and #483 are each "this was
// claimed and should not have been". A baseline that only grows would have
// stopped all three, so a withdrawal passes WITH a reason and fails without one.
func TestAWithdrawalPassesWithItsReasonAndFailsWithout(t *testing.T) {
	dir := t.TempDir()
	pinned := artefactWith(t, []string{"incus"}, map[string]map[string]any{
		"lb/v1/ZonedAPI.CreateLb": proven("observed"),
	})
	baseline := filepath.Join(dir, "pin.json")
	var out, errOut bytes.Buffer
	if code := evidenceCapture([]string{"--evidence", pinned, "--out", baseline}, &out, &errOut); code != exitOK {
		t.Fatalf("capture exited %d", code)
	}
	withdrawn := artefactWith(t, []string{"incus"}, map[string]map[string]any{
		"lb/v1/ZonedAPI.CreateLb": proven("unobserved"),
	})

	// Unaccepted: red.
	out.Reset()
	errOut.Reset()
	if code := evidenceVerify([]string{"--baseline", baseline, "--evidence", withdrawn}, &out, &errOut); code != exitDrift {
		t.Fatalf("an unaccepted withdrawal passed (exit %d)", code)
	}

	// Accepted with a reason: green, and the reason is printed rather than
	// swallowed — an acceptance nobody can read is a silencer.
	accepted := filepath.Join(dir, "accepted.json")
	if err := os.WriteFile(accepted, []byte(`{"accepted":[{"operation":"lb/v1/ZonedAPI.CreateLb",`+
		`"axis":"shape","reason":"the recording was withdrawn: it captured a field the cloud stopped sending"}]}`),
		0o600); err != nil {
		t.Fatalf("write accepted: %v", err)
	}
	out.Reset()
	errOut.Reset()
	code := evidenceVerify([]string{"--baseline", baseline, "--evidence", withdrawn, "--accepted", accepted}, &out, &errOut)
	if code != exitOK {
		t.Fatalf("an accepted withdrawal was refused (exit %d): %s%s", code, out.String(), errOut.String())
	}
	if !strings.Contains(out.String(), "the recording was withdrawn") {
		t.Errorf("the accepted withdrawal's reason is not printed:\n%s", out.String())
	}

	// An acceptance with an empty reason is refused, because the whole value of
	// this file is that a withdrawal carries why.
	silent := filepath.Join(dir, "silent.json")
	if err := os.WriteFile(silent, []byte(`{"accepted":[{"operation":"lb/v1/ZonedAPI.CreateLb","axis":"shape","reason":"  "}]}`),
		0o600); err != nil {
		t.Fatalf("write silent: %v", err)
	}
	out.Reset()
	errOut.Reset()
	if code := evidenceVerify([]string{"--baseline", baseline, "--evidence", withdrawn, "--accepted", silent}, &out, &errOut); code == exitOK {
		t.Fatal("a withdrawal accepted with no reason was honoured")
	}
	if !strings.Contains(errOut.String(), "no reason") {
		t.Errorf("the refusal does not say what is missing:\n%s", errOut.String())
	}
}

// A baseline taken from a partial run is worse than none: it pins `dataplane:
// false` on every operation that would have earned it, and the consumer is then
// told nothing regressed on the day it did. Refused AT CAPTURE.
func TestABaselineIsRefusedWhenTheArtefactReachedNoRuntime(t *testing.T) {
	runtimeless := artefactWith(t, []string{"none"}, map[string]map[string]any{
		"instance/v1/API.CreateServer": proven("observed"),
	})
	var out, errOut bytes.Buffer
	code := evidenceCapture([]string{"--evidence", runtimeless}, &out, &errOut)
	if code == exitOK {
		t.Fatalf("a baseline was captured from a run that started no machine:\n%s", out.String())
	}
	for _, want := range []string{"started no machine", "evidence:update"} {
		if !strings.Contains(errOut.String(), want) {
			t.Errorf("the refusal never says %q:\n%s", want, errOut.String())
		}
	}

	// The accepting half: the same artefact earned under a runtime is captured.
	withRuntime := artefactWith(t, []string{"incus-ovn", "none"}, map[string]map[string]any{
		"instance/v1/API.CreateServer": proven("observed"),
	})
	out.Reset()
	errOut.Reset()
	if code := evidenceCapture([]string{"--evidence", withRuntime}, &out, &errOut); code != exitOK {
		t.Fatalf("a record earned under a runtime was refused (exit %d): %s", code, errOut.String())
	}
}

// `response` and `refusal` are both "a real client got an answer here", and
// neither is above the other — #156 took this axis from 181 arrivals down to 83
// verdicts and that was a fix. A baseline that ranked them would fail every
// consumer the day a suite started demanding a refusal where it used to read a
// response.
func TestAProbeVerdictIsDeliveredByEitherVerdict(t *testing.T) {
	pinned := artefactWith(t, []string{"incus"}, map[string]map[string]any{
		"instance/v1/API.CreateServer": proven("observed"),
	})
	baseline := filepath.Join(t.TempDir(), "pin.json")
	var out, errOut bytes.Buffer
	if code := evidenceCapture([]string{"--evidence", pinned, "--out", baseline}, &out, &errOut); code != exitOK {
		t.Fatalf("capture exited %d", code)
	}

	swapped := proven("observed")
	swapped["probed"] = "refusal"
	other := artefactWith(t, []string{"incus"}, map[string]map[string]any{
		"instance/v1/API.CreateServer": swapped,
	})
	out.Reset()
	errOut.Reset()
	if code := evidenceVerify([]string{"--baseline", baseline, "--evidence", other}, &out, &errOut); code != exitOK {
		t.Fatalf("response → refusal was called a regression (exit %d): %s", code, errOut.String())
	}

	// And the fall that IS one: no client reached it at all.
	none := proven("observed")
	none["probed"] = "none"
	unprobed := artefactWith(t, []string{"incus"}, map[string]map[string]any{
		"instance/v1/API.CreateServer": none,
	})
	out.Reset()
	errOut.Reset()
	if code := evidenceVerify([]string{"--baseline", baseline, "--evidence", unprobed}, &out, &errOut); code != exitDrift {
		t.Fatalf("an operation no client reached any more passed (exit %d)", code)
	}
}

// A consumer pins only what it relies on. An axis it never named cannot fail it,
// which is what makes the file something a downstream project actually commits.
func TestOnlyTheAxesAConsumerNamedAreHeldAgainstIt(t *testing.T) {
	pinned := artefactWith(t, []string{"incus"}, map[string]map[string]any{
		"instance/v1/API.CreateServer": proven("observed"),
	})
	baseline := filepath.Join(t.TempDir(), "pin.json")
	var out, errOut bytes.Buffer
	if code := evidenceCapture([]string{"--evidence", pinned, "--out", baseline, "--axes", "contract,driven"},
		&out, &errOut); code != exitOK {
		t.Fatalf("capture exited %d: %s", code, errOut.String())
	}

	// Every other axis withdrawn; the two pinned ones held.
	narrowed := map[string]any{
		"driven": true, "probed": "none", "contract": "clean",
		"dataplane": false, "shape": "unobserved", "behaviour": false, "negative": false,
	}
	after := artefactWith(t, []string{"incus"}, map[string]map[string]any{
		"instance/v1/API.CreateServer": narrowed,
	})
	out.Reset()
	errOut.Reset()
	if code := evidenceVerify([]string{"--baseline", baseline, "--evidence", after}, &out, &errOut); code != exitOK {
		t.Fatalf("an axis nobody pinned failed the run (exit %d): %s", code, errOut.String())
	}

	// And an axis that is not one of the record's is refused by name rather than
	// silently pinning nothing.
	out.Reset()
	errOut.Reset()
	if code := evidenceCapture([]string{"--evidence", pinned, "--axes", "vibes"}, &out, &errOut); code == exitOK {
		t.Fatal("an axis this record does not carry was accepted")
	}
	if !strings.Contains(errOut.String(), "vibes") {
		t.Errorf("the refusal does not name the axis:\n%s", errOut.String())
	}
}

// An axis whose verdict IS the absence of proof is not pinned, and the reason is
// a measurement: the first version of this file pinned them, and run against
// v0.11.0 it reported `osc/Client.AcceptNetPeering behaviour: false → true` as a
// regression. An absence that becomes a proof is progress; a baseline that calls
// it a fall is a ratchet pointing the wrong way.
func TestAnAxisThatProvesNothingIsNotPinned(t *testing.T) {
	nothing := map[string]any{
		"driven": false, "probed": "none", "contract": "unchecked",
		"dataplane": false, "shape": "unobserved", "behaviour": false, "negative": false,
	}
	artefact := artefactWith(t, []string{"incus"}, map[string]map[string]any{
		"instance/v1/API.CreateServer": nothing,
		"instance/v1/API.ListServers":  proven("observed"),
	})
	var out, errOut bytes.Buffer
	if code := evidenceCapture([]string{"--evidence", artefact}, &out, &errOut); code != exitOK {
		t.Fatalf("capture exited %d: %s", code, errOut.String())
	}
	var baseline evidenceBaseline
	if err := json.Unmarshal(out.Bytes(), &baseline); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if _, pinned := baseline.Operations["instance/v1/API.CreateServer"]; pinned {
		t.Errorf("an operation nothing proves was pinned: %v",
			baseline.Operations["instance/v1/API.CreateServer"])
	}
	// The reader proves it can find before it judges: the proven operation IS
	// pinned, on all seven axes.
	if got := len(baseline.Operations["instance/v1/API.ListServers"]); got != 7 {
		t.Errorf("the proven operation pins %d axes, want 7", got)
	}
}
