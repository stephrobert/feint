package emulator

// The bound on how long a hung connection is held, fired rather than described.
//
// `maxFaultHang` was a five-minute constant with one use and no test: grep
// returned the declaration and the line that reads it. Its comment named the
// defect it prevents — "one `hang` rule and one client that exits leaks a
// goroutine and a file descriptor for the life of the process" — which is
// exactly the shape this repository calls a comment standing in for a control.
// A review said so, and it was right: nothing in CI would have noticed the
// deadline disappearing.
//
// So the duration lives in a variable, and this shortens it. The test is not
// about five minutes; it is about the bound existing at all.

import (
	"bufio"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// hangPack is the smallest pack that can carry a rule: one operation, one
// handler nobody reaches once the rule is armed.
type hangPack struct{}

func (hangPack) Name() string { return "stub" }
func (hangPack) Routes() []Route {
	return []Route{{Method: "GET", Path: "/stub/things", Operation: "stub/v1/API.ListThings",
		Handler: func(w http.ResponseWriter, _ *http.Request) {
			WriteJSON(w, http.StatusOK, map[string]any{"things": []string{"one"}})
		}}}
}
func (hangPack) Declined() []Decline { return nil }
func (hangPack) Env(endpoint string) Environment {
	return Environment{Vars: map[string]string{"STUB_ENDPOINT": endpoint}}
}

// TestAHeldConnectionIsReleasedWhenTheBoundExpires fires the deadline.
//
// A peer that goes away without closing — killed, or a network that dropped —
// leaves nothing to end the read, so the deadline is the only thing that
// releases the connection. Here the peer stays silent and alive, which is the
// same situation from the emulator's side.
//
// Falsified by removing `conn.SetReadDeadline` from the hang branch: the read
// then never returns, nothing answers within the timeout, and the test fails
// with "the connection was still held".
func TestAHeldConnectionIsReleasedWhenTheBoundExpires(t *testing.T) {
	previous := faultHangLimit.Load()
	faultHangLimit.Store(int64(250 * time.Millisecond))
	t.Cleanup(func() { faultHangLimit.Store(previous) })

	srv, err := NewServer(DefaultEnv(), hangPack{})
	if err != nil {
		t.Fatalf("mount: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	armed := strings.NewReader(`{"faults":[{"operation":"stub/v1/API.ListThings","transport":"hang"}]}`)
	req, err := http.NewRequest(http.MethodPut, ts.URL+"/_feint/faults", armed)
	if err != nil {
		t.Fatalf("build the arming request: %v", err)
	}
	res, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("arm: %v", err)
	}
	if err := res.Body.Close(); err != nil {
		t.Fatalf("close: %v", err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("arming the hang rule answered %d", res.StatusCode)
	}

	conn, err := net.DialTimeout("tcp", strings.TrimPrefix(ts.URL, "http://"), 5*time.Second)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	defer func() { _ = conn.Close() }()
	if _, err := fmt.Fprint(conn, "GET /stub/things HTTP/1.1\r\nHost: feint\r\n\r\n"); err != nil {
		t.Fatalf("write the request: %v", err)
	}

	// The emulator closes it when the bound expires. A read on this side then
	// returns EOF rather than blocking, and the generous deadline is the test
	// harness's, not the bound's.
	if err := conn.SetReadDeadline(time.Now().Add(10 * time.Second)); err != nil {
		t.Fatalf("set the read deadline: %v", err)
	}
	started := time.Now()
	if _, err := bufio.NewReader(conn).ReadByte(); err == nil {
		t.Fatal("the emulator answered a hang rule, which must never answer")
	} else if timeout := net.Error(nil); errors.As(err, &timeout) && timeout.Timeout() {
		t.Fatalf("the connection was still held after %.1fs, so the bound did not fire and a peer "+
			"that goes away leaks a goroutine and a descriptor for the life of the process",
			time.Since(started).Seconds())
	}
	if elapsed := time.Since(started); elapsed < 200*time.Millisecond {
		t.Fatalf("the connection ended after %v, which is sooner than the bound: something other "+
			"than the deadline closed it, so this test does not measure the bound", elapsed)
	}
}
