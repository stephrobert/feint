package machine_test

import (
	"context"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// peeringDriver is a runtime whose networks are born separate, recording what
// each network was peered with.
type peeringDriver struct {
	machine.Noop
	peers map[string][]string
}

func (d *peeringDriver) NativeIsolation() bool { return true }
func (d *peeringDriver) PeerNetworks(_ context.Context, network string, peers []string) error {
	d.peers[network] = peers
	return nil
}

// rulesDriver is a runtime whose networks are born joined, recording the
// foreign blocks each network was told to keep out.
type rulesDriver struct {
	machine.Noop
	foreign map[string][]string
}

func (d *rulesDriver) IsolateNetwork(_ context.Context, network string, foreign []string) error {
	d.foreign[network] = foreign
	return nil
}

func threeMembers() []machine.IsolationMember {
	return []machine.IsolationMember{
		{ID: "pn-1", Network: "net-1", Block: "10.0.1.0/24"},
		{ID: "pn-2", Network: "net-2", Block: "10.0.2.0/24"},
		{ID: "pn-3", Network: "net-3", Block: "10.0.3.0/24"},
	}
}

// Under native isolation, exactly the reachable members become peers: nothing
// more — peering two foreign networks reports joined what upstream keeps
// apart, the inverse of the property this project measures — and nothing less.
func TestReconcilePeersExactlyTheReachableMembers(t *testing.T) {
	driver := &peeringDriver{peers: map[string][]string{}}

	// pn-1 and pn-2 reach each other; pn-3 reaches nobody.
	native, applied := machine.ReconcileIsolation(t.Context(), driver, nil, "network",
		threeMembers(), func(from, to int) bool { return from != 2 && to != 2 })
	if !native || !applied {
		t.Fatalf("native=%v applied=%v, want true,true for a native-isolation driver", native, applied)
	}

	if got := driver.peers["net-1"]; len(got) != 1 || got[0] != "net-2" {
		t.Errorf("net-1 was peered with %v, want [net-2]", got)
	}
	if got := driver.peers["net-3"]; len(got) != 0 {
		t.Errorf("net-3 was peered with %v, want nothing", got)
	}
}

// Under rule-set isolation, each network keeps out exactly the blocks of the
// members it cannot reach: a reachable neighbour in the foreign list severs
// what the provider routes, and a foreign one missing from it joins what the
// provider keeps apart.
func TestReconcileKeepsOnlyForeignBlocksOut(t *testing.T) {
	driver := &rulesDriver{foreign: map[string][]string{}}

	native, applied := machine.ReconcileIsolation(t.Context(), driver, nil, "network",
		threeMembers(), func(from, to int) bool { return from != 2 && to != 2 })
	if native || !applied {
		t.Fatalf("native=%v applied=%v, want false,true for a rule-set driver", native, applied)
	}

	if got := driver.foreign["net-1"]; len(got) != 1 || got[0] != "10.0.3.0/24" {
		t.Errorf("net-1 keeps out %v, want [10.0.3.0/24]", got)
	}
	if got := driver.foreign["net-3"]; len(got) != 2 {
		t.Errorf("net-3 keeps out %v, want both other blocks", got)
	}
}

// A member with no backing network is neither configured nor named as a peer,
// and a member with no block never appears in a foreign list: there is nothing
// to configure and nothing to keep out, and a driver told to peer with an
// empty name would refuse every reconciliation after it.
func TestReconcileSkipsWhatHasNoNetworkOrNoBlock(t *testing.T) {
	members := []machine.IsolationMember{
		{ID: "pn-1", Network: "net-1", Block: "10.0.1.0/24"},
		{ID: "pn-2", Network: "", Block: "10.0.2.0/24"},
		{ID: "pn-3", Network: "net-3", Block: ""},
	}
	everyone := func(int, int) bool { return true }

	peering := &peeringDriver{peers: map[string][]string{}}
	machine.ReconcileIsolation(t.Context(), peering, nil, "network", members, everyone)
	if _, configured := peering.peers[""]; configured {
		t.Error("a member without a network was configured under its empty name")
	}
	if got := peering.peers["net-1"]; len(got) != 1 || got[0] != "net-3" {
		t.Errorf("net-1 was peered with %v, want [net-3] only", got)
	}

	rules := &rulesDriver{foreign: map[string][]string{}}
	nobody := func(int, int) bool { return false }
	machine.ReconcileIsolation(t.Context(), rules, nil, "network", members, nobody)
	if got := rules.foreign["net-1"]; len(got) != 1 || got[0] != "10.0.2.0/24" {
		t.Errorf("net-1 keeps out %v, want [10.0.2.0/24]: a blockless member has nothing to keep out", got)
	}

	// And a driver with neither capability reports that nothing was applied,
	// so a pack does not resync rule-carried state no rule carries.
	if _, applied := machine.ReconcileIsolation(t.Context(), machine.Noop{}, nil, "network", members, nobody); applied {
		t.Error("a capability-less driver reported an isolation it cannot have applied")
	}
}
