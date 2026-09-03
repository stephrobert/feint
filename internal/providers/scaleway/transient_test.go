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

// A reboot is observable with the mode OFF, which is the default (#654).
//
// This test replaces one that asserted the opposite, and the reversal is the
// decision #654 asked for. The old property was "with the mode off nothing
// about a reboot changed", and it was true: measured on main, six reads of
// `running` after a reboot that answered success. A Day-2 client cannot build
// on that — an action that was ACCEPTED is not an action that HAPPENED, so a
// waiter watches the state leave `running`, and here it never did. The reporter's
// module timed out at 300 s against an emulator whose reboot had worked.
//
// poweron and poweroff stay observable in this mode by accident of arriving
// somewhere else. Reboot is the one action whose target state is its starting
// state, so it is the one the mode made invisible, and PendingOnlySignal is
// what singles it out without the core learning the word.
//
// What does NOT change: the settled state. The chain still ends at `running`,
// and a client that reads twice more sees it.
func TestARebootIsObservableWithoutEventualConsistency(t *testing.T) {
	ts := newTestServer(t)
	id, _ := serverWith(t, ts, `{"name":"rebooter","commercial_type":"DEV1-S"}`)

	if status, _ := do(t, ts, "POST", zoneURL+"/servers/"+id+"/action", `{"action":"poweron"}`); status != http.StatusAccepted {
		t.Fatalf("poweron: %d", status)
	}
	// The accepting half, and it is not decoration: a mechanism that walked
	// every chain in every mode would pass the reboot assertions below and
	// change what every existing client sees of a poweron.
	if got := state(t, ts, id); got != "running" {
		t.Fatalf("poweron answered %q rather than settling at running, with the mode off", got)
	}
	if status, _ := do(t, ts, "POST", zoneURL+"/servers/"+id+"/action", `{"action":"reboot"}`); status != http.StatusAccepted {
		t.Fatalf("reboot: %d", status)
	}
	want := []string{"stopping", "starting", "running"}
	for i, expect := range want {
		if got := state(t, ts, id); got != expect {
			t.Fatalf("read %d after a reboot answered %q, want %q: the chain a client waits on", i, got, expect)
		}
	}
	// And it settles: a client that keeps reading is not left walking for ever.
	if got := state(t, ts, id); got != "running" {
		t.Errorf("the reboot settled at %q, want running", got)
	}
}

// A poweroff is NOT given the reboot's treatment, and neither is a poweron.
//
// Their chains narrate a change the state already carries, so the consistency
// mode governs them as it did before #654. Without this the fix would be "walk
// every chain always", which is the change nobody asked for: it would make a
// local emulator answer `starting` to clients that have waited for nothing
// since #124 settled that question.
func TestAPoweronAndAPoweroffStaySettledWithoutEventualConsistency(t *testing.T) {
	ts := newTestServer(t)
	id, _ := serverWith(t, ts, `{"name":"plain","commercial_type":"DEV1-S"}`)

	if status, _ := do(t, ts, "POST", zoneURL+"/servers/"+id+"/action", `{"action":"poweron"}`); status != http.StatusAccepted {
		t.Fatalf("poweron: %d", status)
	}
	for i := 0; i < 2; i++ {
		if got := state(t, ts, id); got != "running" {
			t.Fatalf("read %d after a poweron answered %q, and with the mode off it settles at once", i, got)
		}
	}
	if status, _ := do(t, ts, "POST", zoneURL+"/servers/"+id+"/action", `{"action":"poweroff"}`); status != http.StatusAccepted {
		t.Fatalf("poweroff: %d", status)
	}
	for i := 0; i < 2; i++ {
		if got := state(t, ts, id); got != "stopped" {
			t.Fatalf("read %d after a poweroff answered %q, and with the mode off it settles at once", i, got)
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
