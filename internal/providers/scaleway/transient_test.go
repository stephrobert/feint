package scaleway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// eventualServer is newTestServer with transient states turned on, which is what
// `feint serve --consistency eventual` does.
//
// Its own helper rather than a parameter on newTestServer, for the reason
// serveWithProjects gives about its own: every other test in this package
// asserts the settled emulator, and a shared helper taking the flag would invite
// them to drift into asking for something else.
func eventualServer(t *testing.T) *httptest.Server {
	t.Helper()

	var seq int
	st := store.New()
	st.Eventual(true)
	env := &emulator.Env{
		Store: st,
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			seq++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
		},
	}
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// A reboot is observable: a client waiting on it sees the machine leave the
// state it started in (#637).
//
// Why this action and not another. A reboot's target state is the state it
// started from, so "the state equals running" proves nothing. Without task
// support the only signal a waiter has is watching the machine LEAVE its initial
// state, and this emulator answered `running` from the first read to the last —
// so a client's own waiting guard could not be exercised here at all, and the
// author of #637 had to prove it by unit test instead.
//
// The chain is fr-par's, measured 2026-09-03 by polling a real DEV1-S:
// stopping (detail "rebooting"), starting, running.
func TestARebootIsObservableUnderEventualConsistency(t *testing.T) {
	ts := eventualServer(t)
	id, _ := serverWith(t, ts, `{"name":"rebooter","commercial_type":"DEV1-S"}`)

	if status, _ := do(t, ts, "POST", zoneURL+"/servers/"+id+"/action", `{"action":"poweron"}`); status != http.StatusAccepted {
		t.Fatalf("poweron: %d", status)
	}
	// poweron is observable too, and settling it here is what makes the reboot
	// below start from `running` rather than from the tail of this chain.
	settle := func(want string) {
		t.Helper()
		for i := 0; i < 10; i++ {
			if state(t, ts, id) == want {
				return
			}
		}
		t.Fatalf("the server never settled at %q, last %q", want, state(t, ts, id))
	}
	settle("running")

	if status, _ := do(t, ts, "POST", zoneURL+"/servers/"+id+"/action", `{"action":"reboot"}`); status != http.StatusAccepted {
		t.Fatalf("reboot: %d", status)
	}

	// The property the issue asks for, in its own words: at least one read
	// between the action and the final state returns something other than the
	// initial state.
	var seen []string
	for i := 0; i < 10; i++ {
		current := state(t, ts, id)
		if len(seen) == 0 || seen[len(seen)-1] != current {
			seen = append(seen, current)
		}
		if current == "running" && len(seen) > 1 {
			break
		}
	}
	if len(seen) < 2 {
		t.Fatalf("a reboot answered %v from the first read to the last: a client waiting on it "+
			"observes nothing and returns in 0.000 s", seen)
	}
	if seen[0] == "running" {
		t.Errorf("the first read after a reboot answered running, which is the state it started "+
			"from: %v", seen)
	}
	if seen[len(seen)-1] != "running" {
		t.Errorf("the reboot settled at %q, want running: %v", seen[len(seen)-1], seen)
	}
	// And the chain is the cloud's, not an invented one.
	want := []string{"stopping", "starting", "running"}
	if len(seen) != len(want) {
		t.Fatalf("the reboot walked %v, want %v", seen, want)
	}
	for i, state := range want {
		if seen[i] != state {
			t.Errorf("step %d is %q, want %q (%v)", i, seen[i], state, seen)
		}
	}
}

// With the mode off — the default — nothing about a reboot changed, which is
// what keeps every other test in this package and the conformance suite
// untouched.
//
// The accepting half of the guard above: a mechanism that made every emulator
// walk chains would pass that test and change what every existing client sees.
func TestARebootIsStillInstantWithoutEventualConsistency(t *testing.T) {
	ts := newTestServer(t)
	id, _ := serverWith(t, ts, `{"name":"rebooter","commercial_type":"DEV1-S"}`)

	if status, _ := do(t, ts, "POST", zoneURL+"/servers/"+id+"/action", `{"action":"poweron"}`); status != http.StatusAccepted {
		t.Fatalf("poweron: %d", status)
	}
	if got := state(t, ts, id); got != "running" {
		t.Fatalf("poweron answered %q rather than settling at running", got)
	}
	if status, _ := do(t, ts, "POST", zoneURL+"/servers/"+id+"/action", `{"action":"reboot"}`); status != http.StatusAccepted {
		t.Fatalf("reboot: %d", status)
	}
	for i := 0; i < 3; i++ {
		if got := state(t, ts, id); got != "running" {
			t.Fatalf("read %d after a reboot answered %q, and with the mode off the state must "+
				"never leave running", i, got)
		}
	}
}

// state reads one server's state.
func state(t *testing.T, ts *httptest.Server, id string) string {
	t.Helper()
	status, body := do(t, ts, "GET", zoneURL+"/servers/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get server: %d (%v)", status, body)
	}
	server, _ := body["server"].(map[string]any)
	got, _ := server["state"].(string)
	return got
}
