package emulator_test

// The bounds on a transport fault, each one written after a security review
// reproduced what its absence costs (#851).
//
// Both are about the same asymmetry: a `hang` rule turns one client request
// into a resource this process holds, and the client decides how many. Every
// other fault answers and lets go.

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"testing"
	"time"
)

// hold opens a raw connection, sends one request, and answers it, leaving the
// connection open. The caller closes it, which is what releases the hang.
func hold(t *testing.T, addr, path string) net.Conn {
	t.Helper()
	conn, err := net.DialTimeout("tcp", addr, 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	if _, err := fmt.Fprintf(conn, "GET %s HTTP/1.1\r\nHost: feint\r\n\r\n", path); err != nil {
		t.Fatalf("write the request: %v", err)
	}
	return conn
}

// TestAHangRuleStopsTakingConnectionsBeforeItLocksTheOperatorOut holds the
// bound on connections held at once.
//
// What it prevents, measured before the bound existed: one `hang` rule and 61
// connections against a server started with `ulimit -n 64` consumed every
// descriptor. `GET /_feint/health` and `DELETE /_feint/faults` both hung, the
// log repeated `accept4: too many open files` once a second, and the only door
// that could disarm the rule was gone until the peers left. The hijacked
// connections and that door share one listener.
//
// The assertion is in two halves, and the second is why this is not a guard
// that refuses everything: past the bound the emulator answers rather than
// hijacking, and the admin plane still answers.
//
// Falsified by neutralising the `held > maxHeldConnections` branch in
// serveTransportFault: the 65th call then hangs like the others and the test
// fails on its read deadline.
func TestAHangRuleStopsTakingConnectionsBeforeItLocksTheOperatorOut(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{503}))
	addr := strings.TrimPrefix(ts.URL, "http://")

	armed := `{"faults":[{"operation":"stub/v1/API.ListThings","transport":"hang"}]}`
	if body, status := faults(t, ts, http.MethodPut, armed); status != http.StatusOK {
		t.Fatalf("arming the hang rule: %d %v", status, body)
	}

	// The bound is 64; 64 connections are held and the 65th must be answered.
	const bound = 64
	for range bound {
		hold(t, addr, "/stub/things")
	}
	// Each held connection is taken when its request is read, which is not
	// instantaneous: wait until the emulator says it has them all rather than
	// racing it.
	deadline := time.Now().Add(10 * time.Second)
	for {
		body, _ := faults(t, ts, http.MethodGet, "")
		fired, _ := body["fired"].([]any)
		if len(fired) >= bound {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("only %d of %d connections were taken over", len(fired), bound)
		}
		time.Sleep(20 * time.Millisecond)
	}

	// Past the bound: an answer, not a hijack, and it says the failure was not
	// injected rather than pretending it was.
	over := hold(t, addr, "/stub/things")
	if err := over.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set the read deadline: %v", err)
	}
	res, err := http.ReadResponse(bufio.NewReader(over), nil)
	if err != nil {
		t.Fatalf("past the bound the connection was taken over anyway, so a client can hold every "+
			"descriptor this process has: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var answer map[string]any
	if err := json.NewDecoder(res.Body).Decode(&answer); err != nil {
		t.Fatalf("decode the refusal: %v", err)
	}
	if message, _ := answer["error"].(string); !strings.Contains(message, "maximum number of connections") {
		t.Fatalf("the answer past the bound does not say the failure was not injected: %v", answer)
	}

	// And the door that disarms still answers, which is the whole point.
	if body, status := faults(t, ts, http.MethodDelete, ""); status != http.StatusOK {
		t.Fatalf("the admin plane is unreachable with the bound reached: %d %v", status, body)
	}
}

// TestADroppedConnectionIsClaimedOnlyWhenItHappened holds the receipt's
// honesty about an effect nobody produced.
//
// Measured before the fix: a rule of `{"transport":"close","delay":"8s"}` and a
// client that gives up after a second never reaches the hijack — serveFault's
// delay select returns on the request context — and the receipt still said
// "the connection was dropped". The transport name was recorded when the line
// was reserved, at arrival, before anything was injected.
//
// Falsified by reserving the line with the rule's transport again: the test
// fails with "the receipt claims a drop that never happened".
func TestADroppedConnectionIsClaimedOnlyWhenItHappened(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{503}))

	armed := `{"faults":[{"operation":"stub/v1/API.ListThings","transport":"close","delay":"8s"}]}`
	if body, status := faults(t, ts, http.MethodPut, armed); status != http.StatusOK {
		t.Fatalf("arming the delayed close: %d %v", status, body)
	}

	client := &http.Client{Timeout: 300 * time.Millisecond}
	res, err := client.Get(ts.URL + "/stub/things")
	if err == nil {
		_ = res.Body.Close()
		t.Fatal("the client was answered, so it did not give up during the delay")
	}

	body, status := faults(t, ts, http.MethodGet, "")
	if status != http.StatusOK {
		t.Fatalf("GET /_feint/faults: %d %v", status, body)
	}
	fired, _ := body["fired"].([]any)
	if len(fired) != 1 {
		t.Fatalf("want one receipt line, got %v", fired)
	}
	line, _ := fired[0].(map[string]any)
	if transport, present := line["transport"]; present {
		t.Fatalf("the receipt claims a drop that never happened: transport %v. The client left "+
			"during the delay, so the connection was never taken over", transport)
	}
	if abandoned, _ := line["abandoned"].(bool); !abandoned {
		t.Fatalf("the line does not say the client left: %v", line)
	}
}
