package cli

import (
	"errors"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// The failures below HTTP (#851).
//
// A status injection is a response: a client parses it, reads its body, and
// can branch on it. The two failures here are the absence of a response, which
// is the part of a client's resilience a status can never exercise. The
// package comment of faults.go used to say they would need "a hook at the
// net/http server"; the hook is http.Hijacker, and these tests are what turned
// that sentence into something that fires.
//
// Every test drives a real HTTP client against a real emulator over a real TCP
// connection, because a stub cannot be reset and a recorded transcript cannot
// show an absent answer.

const faultServersPath = "/instance/v1/zones/fr-par-1/servers"

// TestACloseRuleDropsTheConnectionWithoutAnAnswer holds the first half.
//
// Falsified by removing the `if f.Transport != ""` branch from serveFault:
// the call then returns 200 and the test fails with "the client got a
// response: 200", which is exactly the confusion the rule exists to produce in
// the client and not in the reader.
func TestACloseRuleDropsTheConnectionWithoutAnAnswer(t *testing.T) {
	addr := faultLab(t)

	if code, _, errOut := faultRun("fault", "add", faultOpListServers, "--addr", addr, "--close"); code != 0 {
		t.Fatalf("arming --close exited %d: %s", code, errOut)
	}

	res, err := http.Get("http://" + addr + faultServersPath) // #nosec G107 -- a loopback test server
	if err == nil {
		_ = res.Body.Close()
		t.Fatalf("the client got a response: %d; a dropped connection has no status line", res.StatusCode)
	}
	// Not asserted as a literal string: a reset surfaces as "connection reset
	// by peer" on Linux and as an unexpected EOF when the FIN wins the race,
	// and both are the failure this rule exists to produce. What must hold is
	// that it is a transport error rather than an HTTP answer, which is what
	// the nil response above already says.
	if res != nil {
		t.Fatalf("a transport failure answered a response as well as an error: %v", err)
	}

	// And the receipt says which failure it was, rather than leaving a reader
	// with `status: 0, abandoned: true` to interpret.
	code, out, errOut := faultRun("fault", "list", "--addr", addr)
	if code != 0 {
		t.Fatalf("list exited %d: %s", code, errOut)
	}
	if !strings.Contains(out, "the connection was dropped") {
		t.Fatalf("the receipt does not say the connection was dropped:\n%s", out)
	}
}

// TestAHangRuleHoldsTheConnectionAndNeverAnswers holds the second half, and
// holds that it is distinguishable from the first.
//
// The client's own deadline is what ends it, which is the point: this is the
// failure a retry budget and a timeout exist for, and a 503 cannot stand in
// for it.
//
// Falsified by making serveTransportFault close the connection for "hang" as
// well: the test then fails with "the client was answered or reset in 0.0s,
// so nothing hung".
func TestAHangRuleHoldsTheConnectionAndNeverAnswers(t *testing.T) {
	addr := faultLab(t)

	if code, _, errOut := faultRun("fault", "add", faultOpListServers, "--addr", addr, "--hang"); code != 0 {
		t.Fatalf("arming --hang exited %d: %s", code, errOut)
	}

	client := &http.Client{Timeout: 400 * time.Millisecond}
	started := time.Now()
	res, err := client.Get("http://" + addr + faultServersPath)
	elapsed := time.Since(started)
	if err == nil {
		_ = res.Body.Close()
		t.Fatalf("the client got a response: %d; a hang never answers", res.StatusCode)
	}
	var timeout net.Error
	if !errors.As(err, &timeout) || !timeout.Timeout() {
		t.Fatalf("the client failed in %.1fs with %v, which is not its own timeout, "+
			"so nothing hung", elapsed.Seconds(), err)
	}
	if elapsed < 300*time.Millisecond {
		t.Fatalf("the client was answered or reset in %.1fs, so nothing hung", elapsed.Seconds())
	}

	code, out, errOut := faultRun("fault", "list", "--addr", addr)
	if code != 0 {
		t.Fatalf("list exited %d: %s", code, errOut)
	}
	if !strings.Contains(out, "the connection was held open") {
		t.Fatalf("the receipt does not say the connection was held open:\n%s", out)
	}
}

// TestTheTwoTransportFailuresAreRefusedTogether holds the refusal that keeps
// the two apart.
//
// Falsified by removing the `*dropConn && *hangConn` check: the test then
// fails with "both were accepted", and the rule armed would be whichever the
// switch happened to reach first, which is a reading that depends on the code
// rather than on the command.
func TestTheTwoTransportFailuresAreRefusedTogether(t *testing.T) {
	addr := faultLab(t)

	code, _, errOut := faultRun("fault", "add", faultOpListServers, "--addr", addr, "--close", "--hang")
	if code == 0 {
		t.Fatal("both were accepted; one drops the connection and the other holds it open, " +
			"so which one fired would be a question")
	}
	if !strings.Contains(errOut, "--close and --hang") {
		t.Fatalf("the refusal does not name the two flags:\n%s", errOut)
	}
}

// TestATransportRuleRefusesAStatus holds the bound at the emulator rather than
// at the CLI, which is where every other bound of a rule lives.
//
// Falsified by removing the `r.Status != 0` check from the transport block in
// validateFaults: the rule is then armed, and the test fails with
// "a status and a transport failure were armed together".
func TestATransportRuleRefusesAStatus(t *testing.T) {
	addr := faultLab(t)

	code, _, errOut := faultRun("fault", "add", faultOpListServers, "--addr", addr,
		"--close", "--status", "503")
	if code == 0 {
		t.Fatal("a status and a transport failure were armed together; a status is a response " +
			"and a transport failure is the absence of one")
	}
	if !strings.Contains(errOut, "transport failure") {
		t.Fatalf("the refusal does not say why:\n%s", errOut)
	}
}
