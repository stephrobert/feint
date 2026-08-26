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

// balancingPack is a stubPack that wires the balancer dataplane; it does not
// wire the firewall, so the two lists cannot be conflated by the code under
// test answering one question for both (#481).
type balancingPack struct {
	stubPack
	wires bool
}

func (p balancingPack) EnforcesBalancing() bool { return p.wires }

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
		balancingPack{stubPack: stubPack{name: "balances"}, wires: true},
		// The third-pack case again, on the other list: implements the
		// interface and answers false, and must be as absent as a pack that
		// never said anything. Without this pack, replacing the answer with
		// the type assertion alone would leave the published list unchanged
		// and the mutation would survive falsification.
		balancingPack{stubPack: stubPack{name: "refrains"}, wires: false},
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

	// The balancing list answers its own question (#481): the pack that wires
	// the balancer appears here and nowhere in the firewall list, and the
	// firewall packs do not leak in. A single list answering both questions is
	// how `capabilities` lied in the first place — one answer for two claims.
	balancing, present := health.Enforced["balancing"]
	if !present {
		t.Fatal("health publishes no `enforced.balancing`: a consumer cannot ask " +
			"whether this provider's balancer forwards packets here, which is the " +
			"one-word gap #481 measured")
	}
	if len(balancing) != 1 || balancing[0] != "balances" {
		t.Errorf("enforced.balancing must name the pack that hands its balancers "+
			"over and only that one, got %v", balancing)
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
	for _, capability := range []string{"firewall", "balancing"} {
		list, ok := enforced[capability].([]any)
		if !ok {
			t.Fatalf("`enforced.%s` is absent or not a list, got %T", capability, enforced[capability])
		}
		if len(list) != 0 {
			t.Errorf("no pack wires the %s here, so the list must be empty, got %v", capability, list)
		}
	}
	if resp.StatusCode != http.StatusOK {
		t.Errorf("health answered %d", resp.StatusCode)
	}
}
