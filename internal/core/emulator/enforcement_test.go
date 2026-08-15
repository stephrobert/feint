package emulator_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// enforcingPack is a stubPack that wires the firewall; plain stubPack does not.
type enforcingPack struct {
	stubPack
	wires bool
}

func (p enforcingPack) EnforcesFirewall() bool { return p.wires }

// TestEnforcementNamesOnlyThePacksThatWireIt holds `/_feint/health`'s `enforced`
// key to the one thing it claims: which packs hand work to a capability the
// driver declares.
//
// Both halves are asserted, and the refusing half is the one this exists for.
// Before #180 the endpoint published `firewall: true` from the driver alone,
// for the whole process, while one pack of three handed a rule over — so a user
// who followed this project's own advice and keyed on the capability probed a
// port a deny-default group should have closed and found it open.
//
// The third pack is the case that matters most: a pack that implements the
// interface and answers false must be as absent as one that never implemented
// it. "Wires it and says no" and "never said" are the same thing to a consumer,
// and both mean do not rely on it here.
func TestEnforcementNamesOnlyThePacksThatWireIt(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env,
		enforcingPack{stubPack: stubPack{name: "wires"}, wires: true},
		enforcingPack{stubPack: stubPack{name: "declines"}, wires: false},
		stubPack{name: "silent"},
	)
	if err != nil {
		t.Fatalf("mount the stub packs: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/_feint/health") //nolint:noctx // a local test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var health struct {
		Enforced map[string][]string `json:"enforced"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&health); err != nil {
		t.Fatalf("decode health: %v", err)
	}

	firewall, present := health.Enforced["firewall"]
	if !present {
		t.Fatal("health publishes no `enforced.firewall`: a consumer cannot tell " +
			"\"no pack wires it\" from \"this build does not say\"")
	}
	if len(firewall) != 1 || firewall[0] != "wires" {
		t.Errorf("enforced.firewall must name the pack that hands rules over and "+
			"only that one, got %v", firewall)
	}
}

// And the key survives a process with no pack wiring anything: empty, not
// missing. An absent key reads as the older payload, which is the shape a
// consumer branches on schema_version to avoid.
func TestEnforcementPublishesAnEmptyListRatherThanNoKey(t *testing.T) {
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, stubPack{name: "silent"})
	if err != nil {
		t.Fatalf("mount the stub pack: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	resp, err := ts.Client().Get(ts.URL + "/_feint/health") //nolint:noctx // a local test server
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()

	var raw map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode health: %v", err)
	}
	enforced, ok := raw["enforced"].(map[string]any)
	if !ok {
		t.Fatalf("health carries no `enforced` object, got %T", raw["enforced"])
	}
	list, ok := enforced["firewall"].([]any)
	if !ok {
		t.Fatalf("`enforced.firewall` is absent or not a list, got %T", enforced["firewall"])
	}
	if len(list) != 0 {
		t.Errorf("no pack wires the firewall here, so the list must be empty, got %v", list)
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health answered %d", resp.StatusCode)
	}
}
