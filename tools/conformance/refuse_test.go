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
	"sync/atomic"
	"testing"
)

// refuse_client is the control behind the `negative` cases of #428, and it
// exists because the three official clients make this measurement ambiguous.
//
// `scw` validates enums against its own SDK, `exo` resolves a NAME|ID by
// listing before it acts, and both then report their OWN refusal with a
// non-zero exit code and a `{"message": ...}` body — the same two things the
// API's refusal produces. A suite reading the client's text cannot tell the two
// apart, so a case that silently stops reaching the emulator reads exactly like
// the suite working, and the operation it was written for falls back to zero
// with nothing red.
//
// So the helper reads the EMULATOR's verdict: a `negative` span that closes 409
// is the emulator saying it observed no client refusal inside the block. The
// three tests below are the three outcomes, and each is needed:
//
//   - a client refusal that never reached the emulator must FAIL the case;
//   - a refusal the emulator made must PASS it, or a helper that refused
//     everything would satisfy the first test;
//   - a call the API ACCEPTED must fail too, which is the defect the case is
//     written to catch in the first place.
func TestARefusalTheClientMadeItselfFailsTheCase(t *testing.T) {
	code, output := runRefuseClient(t, spanClose{status: http.StatusConflict}, clientExit(1))
	if code == 0 {
		t.Errorf("a case whose refusal never reached the emulator passed, so it measures nothing:\n%s", output)
	}
	for _, want := range []string{"the-case", "measures nothing"} {
		if !strings.Contains(output, want) {
			t.Errorf("the failure never says %q, so nobody reading it knows which case is blind:\n%s", want, output)
		}
	}
}

func TestARefusalTheEmulatorMadePassesTheCase(t *testing.T) {
	code, output := runRefuseClient(t, spanClose{status: http.StatusOK}, clientExit(1))
	if code != 0 {
		t.Errorf("a refusal the emulator itself answered was rejected (exit %d):\n%s", code, output)
	}
}

func TestACallTheAPIAcceptedFailsTheCase(t *testing.T) {
	code, output := runRefuseClient(t, spanClose{status: http.StatusOK}, clientExit(0))
	if code == 0 {
		t.Errorf("the case passed on a call the API accepted, which is the defect it exists to catch:\n%s", output)
	}
	if !strings.Contains(output, "answered success") {
		t.Errorf("the failure does not say the call was accepted:\n%s", output)
	}
}

// The span must actually have been opened and closed. Without this, a helper
// that never called the emulator at all would satisfy the passing case above:
// "nothing was found" and "nothing was looked for" read the same.
func TestTheCaseOpensAndClosesASpan(t *testing.T) {
	var opened, closed int32
	code, output := runRefuseClientCounting(t, spanClose{status: http.StatusOK}, clientExit(1), &opened, &closed)
	if code != 0 {
		t.Fatalf("the case failed (exit %d):\n%s", code, output)
	}
	if atomic.LoadInt32(&opened) != 1 || atomic.LoadInt32(&closed) != 1 {
		t.Errorf("the case opened %d span(s) and closed %d, so its verdict came from somewhere else",
			atomic.LoadInt32(&opened), atomic.LoadInt32(&closed))
	}
}

type spanClose struct{ status int }

// clientExit builds a stand-in client: a shell command that prints and exits
// with the code given. The real clients are not driven here — what is under
// test is how the helper reads their outcome against the emulator's, and a
// stand-in makes the third outcome reproducible, which no installed client
// would.
func clientExit(code int) []string {
	return []string{"sh", "-c", fmt.Sprintf(`echo "client said something"; exit %d`, code)}
}

func runRefuseClient(t *testing.T, close spanClose, client []string) (int, string) {
	t.Helper()
	var opened, closed int32
	return runRefuseClientCounting(t, close, client, &opened, &closed)
}

// runRefuseClientCounting sources the real prove.sh — the subject, never a copy
// — and calls refuse_client against a stub emulator.
func runRefuseClientCounting(t *testing.T, closeWith spanClose, client []string, opened, closed *int32) (int, string) {
	t.Helper()
	requireTool(t, "bash")
	requireTool(t, "curl")
	requireTool(t, "jq")

	stub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodPost && r.URL.Path == "/_feint/assert":
			atomic.AddInt32(opened, 1)
			w.Header().Set("Content-Type", "application/json")
			fmt.Fprint(w, `{"id":"span-1"}`)
		case r.Method == http.MethodPost && strings.HasPrefix(r.URL.Path, "/_feint/assert/"):
			atomic.AddInt32(closed, 1)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(closeWith.status)
			if closeWith.status == http.StatusOK {
				fmt.Fprint(w, `{"operations":["stub/v1/API.GetThing"],"unattributed":0}`)
			} else {
				fmt.Fprint(w, `{"error":"the span demanded a refusal and the emulator answered no client with a 4xx inside it"}`)
			}
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer stub.Close()

	prove, err := filepath.Abs("prove.sh")
	if err != nil {
		t.Fatalf("locate prove.sh: %v", err)
	}
	if _, err := os.Stat(prove); err != nil {
		t.Fatalf("prove.sh is not where this test looks (%s): %v", prove, err)
	}

	script := `set -uo pipefail; . "$1"; shift; refuse_client "the-case" "$@"`
	args := append([]string{"-c", script, "bash", prove}, client...)
	cmd := exec.Command("bash", args...) //nolint:gosec // fixed script, test-controlled arguments
	cmd.Env = append(os.Environ(), "ENDPOINT="+stub.URL)
	out, err := cmd.CombinedOutput()
	code := 0
	if err != nil {
		var exit *exec.ExitError
		if !errors.As(err, &exit) {
			t.Fatalf("run refuse_client: %v\n%s", err, out)
		}
		code = exit.ExitCode()
	}
	return code, string(out)
}
