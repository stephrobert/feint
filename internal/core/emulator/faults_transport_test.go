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
	"net/http/httptest"
	"strings"
	"sync"
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

	// The handler is still parked in its 8 s delay select: the client gave up,
	// the request context has not finished cancelling, and the deferred
	// recordFired has not run. Reading now reads a line in flight, whose fields
	// all still hold their zero values. The wait is on `in_flight`, never on
	// `abandoned`, which is the field under test (#925).
	line := settledReceiptLine(t, ts)
	if transport, present := line["transport"]; present {
		t.Fatalf("the receipt claims a drop that never happened: transport %v. The client left "+
			"during the delay, so the connection was never taken over", transport)
	}
	if abandoned, _ := line["abandoned"].(bool); !abandoned {
		t.Fatalf("the line does not say the client left: %v", line)
	}
}

// settledReceiptLine returns the single receipt line once it is finished.
//
// A line is reserved at arrival and completed when the handler ends, so a
// reader between the two sees every field at its zero value. Before `in_flight`
// existed, the caller below read that state and failed 4 times in 10 on a
// loaded machine, on main, with CI green on the same commit (#925).
//
// It waits on `in_flight` and on nothing else. Waiting on the field a test
// asserts would turn the assertion into a tautology: the loop would stop
// exactly when the test is about to pass.
func settledReceiptLine(t *testing.T, ts *httptest.Server) map[string]any {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	var line map[string]any
	for {
		body, status := faults(t, ts, http.MethodGet, "")
		if status != http.StatusOK {
			t.Fatalf("GET /_feint/faults: %d %v", status, body)
		}
		fired, _ := body["fired"].([]any)
		if len(fired) != 1 {
			t.Fatalf("want one receipt line, got %v", fired)
		}
		line, _ = fired[0].(map[string]any)
		if inFlight, _ := line["in_flight"].(bool); !inFlight {
			return line
		}
		if time.Now().After(deadline) {
			t.Fatalf("the receipt line never left in_flight after 5s: %v", line)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAReceiptLineSaysWhenItIsStillInFlight holds the marker itself, in the
// window where it matters most: a hang holds the connection for minutes, and a
// suite polling the receipt during it is the ordinary case, not an edge.
//
// The order of the two waits below is what makes this test bite, and it is not
// decoration: it waits for `transport == "hang"`, which markTransport writes at
// the hijack, and only then asserts `in_flight`. An implementation that cleared
// the marker at the hijack would satisfy a test that looked at in_flight first
// and fails this one.
//
// Falsified by reserving the line without InFlight: the first assertion fails
// with "the receipt does not say the line is in flight".
func TestAReceiptLineSaysWhenItIsStillInFlight(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{503}))

	armedRule := `{"faults":[{"operation":"stub/v1/API.ListThings","transport":"hang"}]}`
	if body, status := faults(t, ts, http.MethodPut, armedRule); status != http.StatusOK {
		t.Fatalf("arming the hang: %d %v", status, body)
	}

	conn := hold(t, ts.Listener.Addr().String(), "/stub/things")

	// The connection is held right now, so the line exists and is not finished.
	var line map[string]any
	deadline := time.Now().Add(5 * time.Second)
	for {
		body, status := faults(t, ts, http.MethodGet, "")
		if status != http.StatusOK {
			t.Fatalf("GET /_feint/faults: %d %v", status, body)
		}
		fired, _ := body["fired"].([]any)
		if len(fired) == 1 {
			line, _ = fired[0].(map[string]any)
			if transport, _ := line["transport"].(string); transport == "hang" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the hang never reached the receipt after 5s: %v", fired)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if inFlight, _ := line["in_flight"].(bool); !inFlight {
		t.Fatalf("the receipt does not say the line is in flight while the connection is held: %v", line)
	}
	if status, _ := line["status"].(float64); status != 0 {
		t.Fatalf("a line in flight carries a status: %v", line)
	}

	// Letting go finishes it, and the marker goes with the flight.
	_ = conn.Close()
	settled := settledReceiptLine(t, ts)
	if _, present := settled["in_flight"]; present {
		t.Fatalf("a finished line still carries in_flight: %v", settled)
	}
	if transport, _ := settled["transport"].(string); transport != "hang" {
		t.Fatalf("the finished line lost its transport: %v", settled)
	}
}

// TestSettlingOneFiringLeavesTheOtherInFlight holds that the marker is cleared
// on the line that finished, and on that line only.
//
// The defect it rules out, which a falsifier reproduced against the first
// version of this lot: clearing by operation name instead of by slot.
//
//	for i := range fs.fired {
//	    if fs.fired[i].Operation == fs.fired[f.slot].Operation { ... }
//	}
//
// That compiles, and every other test in this package stayed green under it,
// because they all drive ONE client: fired[0] and fired[f.slot] are then the
// same line and the two implementations cannot be told apart. Two clients on
// one hang rule is what a suite does, not an edge case, and under the mutation
// the second connection — still held — stops saying in_flight.
func TestSettlingOneFiringLeavesTheOtherInFlight(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{503}))
	addr := strings.TrimPrefix(ts.URL, "http://")

	armedRule := `{"faults":[{"operation":"stub/v1/API.ListThings","transport":"hang"}]}`
	if body, status := faults(t, ts, http.MethodPut, armedRule); status != http.StatusOK {
		t.Fatalf("arming the hang rule: %d %v", status, body)
	}

	first := hold(t, addr, "/stub/things")
	hold(t, addr, "/stub/things")

	deadline := time.Now().Add(5 * time.Second)
	for {
		fired := firedLines(t, ts)
		held := 0
		for _, raw := range fired {
			line, _ := raw.(map[string]any)
			if transport, _ := line["transport"].(string); transport == "hang" {
				held++
			}
		}
		if held == 2 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the two connections were not both taken over after 5s: %v", fired)
		}
		time.Sleep(5 * time.Millisecond)
	}

	_ = first.Close()

	deadline = time.Now().Add(5 * time.Second)
	for {
		fired := firedLines(t, ts)
		settled := 0
		for _, raw := range fired {
			line, _ := raw.(map[string]any)
			if inFlight, _ := line["in_flight"].(bool); !inFlight {
				settled++
			}
		}
		// The first poll that sees any line settle must see exactly one: the
		// other connection is still held by this test.
		if settled > 0 {
			if settled != 1 {
				t.Fatalf("one client let go and %d of 2 lines settled: the second connection is "+
					"still held and its line no longer says in_flight: %v", settled, fired)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("no line settled after the first client let go: %v", fired)
		}
		time.Sleep(5 * time.Millisecond)
	}
}

// TestAStaleFiringDoesNotSettleTheNewCampaignsLine holds the epoch guard for
// the marker, which TestAStaleFiringDoesNotOverwriteTheNewCampaignsLine does
// not: that one asserts on `status`, so clearing in_flight BEFORE the epoch
// check leaves it green. A falsifier measured exactly that.
//
// The sequence: a delayed rule fires and stays in flight, a new campaign
// replaces it — which bumps the epoch and empties the receipt — and a held
// connection takes slot 0 of the new one, the same index the stale firing
// still carries. When the stale firing completes it must touch nothing.
func TestAStaleFiringDoesNotSettleTheNewCampaignsLine(t *testing.T) {
	ts := faultServer(t, echoPack("stub", "/stub", []int{503}))
	addr := strings.TrimPrefix(ts.URL, "http://")

	delayed := `{"faults":[{"operation":"stub/v1/API.ListThings","status":503,"delay":"1s"}]}`
	if body, status := faults(t, ts, http.MethodPut, delayed); status != http.StatusOK {
		t.Fatalf("arming the delayed status: %d %v", status, body)
	}
	var wg sync.WaitGroup
	answered := make(chan struct{})
	wg.Add(1)
	go func() {
		defer wg.Done()
		defer close(answered)
		res, err := ts.Client().Get(ts.URL + "/stub/things")
		if err == nil {
			_ = res.Body.Close()
		}
	}()

	// Wait for the stale firing to have RESERVED its line rather than sleeping
	// a round number: without that reservation there is no stale firing and
	// the test would prove nothing while still passing.
	deadline := time.Now().Add(5 * time.Second)
	for len(firedLines(t, ts)) == 0 {
		if time.Now().After(deadline) {
			t.Fatal("the delayed call never reached the receipt")
		}
		time.Sleep(5 * time.Millisecond)
	}

	hanging := `{"faults":[{"operation":"stub/v1/API.ListThings","transport":"hang"}]}`
	if body, status := faults(t, ts, http.MethodPut, hanging); status != http.StatusOK {
		t.Fatalf("arming the hang rule: %d %v", status, body)
	}
	hold(t, addr, "/stub/things")

	deadline = time.Now().Add(5 * time.Second)
	for {
		fired := firedLines(t, ts)
		if len(fired) == 1 {
			line, _ := fired[0].(map[string]any)
			if transport, _ := line["transport"].(string); transport == "hang" {
				break
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("the held connection never reached the new campaign's receipt: %v", fired)
		}
		time.Sleep(5 * time.Millisecond)
	}

	// The only direction in which this test could pass while proving nothing:
	// if the delayed call finished before the held connection reserved its
	// line, there is no stale firing to collide with, the new campaign's
	// receipt is empty, and the mutation this test exists for touches nothing.
	// The one-second delay leaves 994 ms of margin for a 5.5 ms setup, so this
	// never fires here — and an empty test that stays quiet is the failure
	// this repository refuses, whatever its odds.
	select {
	case <-answered:
		t.Fatal("the delayed call finished before the new campaign reserved its line: nothing was " +
			"in flight from the previous campaign, so this test proves nothing")
	default:
	}

	// The stale firing finishes while this test still holds its connection.
	// The line must stay in flight for as long as we keep looking.
	wg.Wait()
	until := time.Now().Add(300 * time.Millisecond)
	for time.Now().Before(until) {
		fired := firedLines(t, ts)
		if len(fired) != 1 {
			t.Fatalf("the new campaign's receipt holds %d line(s), want 1: %v", len(fired), fired)
		}
		line, _ := fired[0].(map[string]any)
		if inFlight, _ := line["in_flight"].(bool); !inFlight {
			t.Fatalf("the connection is still held and the line no longer says in_flight: a firing "+
				"of the previous campaign settled the slot this one reserved: %v", line)
		}
		time.Sleep(10 * time.Millisecond)
	}
}
