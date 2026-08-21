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

// The score refuses a run that answered with injected faults (#26, #356).
//
// Fault injection can make this emulator refuse, and #26 named the way that
// could turn the coverage story into fiction: a route that looks exercised when
// it only ever answered a fault somebody armed. The core closes the half it
// owns — an injected answer moves no counter but `injected` — and this closes
// the other half, which is about the run rather than the route: a score
// computed over a run carrying any staged answer would be describing a mixture
// of what the emulator serves and what a rule was told to say.
//
// A comment saying "the suite must not run with rules armed" would be the
// defect this repository keeps naming, so it is the gate instead. Fault
// injection has its own suite on its own emulator and its own port
// (tools/conformance/faults.sh) precisely so that this stays at zero for the
// shared run.
func TestTheScoreRefusesARunCarryingAnInjectedAnswer(t *testing.T) {
	code, output := runScore(t, `{
		"served": 2, "exercised": 1, "probed": 0,
		"calls": {"stub/v1/API.ListThings": 1},
		"injected": {"stub/v1/API.GetThing": 2},
		"probes": {}, "untouched": [], "contracts": [],
		"violations": {}, "unread_request_fields": {},
		"fields": {"missing": {}, "excused": {}, "unconfirmed": {}, "stale_declines": [], "compared": []},
		"machines": "none", "evidence": {}
	}`)
	if code == 0 {
		t.Errorf("the score accepted a run carrying two injected answers:\n%s", output)
	}
	for _, want := range []string{"injected", "stub/v1/API.GetThing", "faults.sh"} {
		if !strings.Contains(output, want) {
			t.Errorf("the refusal never says %q, so nobody reading it knows what to clear:\n%s", want, output)
		}
	}
}

// The other half, without which the gate above is satisfied by a script that
// refuses everything: an ordinary run — the default, since rules are off unless
// somebody arms them — still scores.
func TestTheScoreAcceptsARunThatStagedNothing(t *testing.T) {
	code, output := runScore(t, `{
		"served": 2, "exercised": 1, "probed": 0,
		"calls": {"stub/v1/API.ListThings": 1},
		"injected": {},
		"probes": {}, "untouched": ["stub/v1/API.GetThing"], "contracts": [],
		"violations": {}, "unread_request_fields": {},
		"fields": {"missing": {}, "excused": {}, "unconfirmed": {}, "stale_declines": [], "compared": []},
		"machines": "none", "evidence": {}
	}`)
	if code != 0 {
		t.Errorf("the score refused an ordinary run (exit %d):\n%s", code, output)
	}
	if !strings.Contains(output, "conformance score") {
		t.Errorf("the score printed no verdict at all:\n%s", output)
	}
}

// runScore drives the real score.sh against a stub emulator answering one
// report. The script is the subject, so it is sourced from where the suites run
// it and never copied here.
func runScore(t *testing.T, report string) (int, string) {
	t.Helper()
	requireTool(t, "bash")
	requireTool(t, "curl")
	requireTool(t, "jq")

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/_feint/conformance" {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, report)
	}))
	defer stub.Close()

	score, err := filepath.Abs("score.sh")
	if err != nil {
		t.Fatalf("locate score.sh: %v", err)
	}
	if _, err := os.Stat(score); err != nil {
		t.Fatalf("score.sh is not where this test looks (%s): %v", score, err)
	}

	cmd := exec.Command("bash", score, stub.URL) //nolint:gosec // fixed script, test-controlled arguments
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run the score: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out)
}
