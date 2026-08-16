package storetest_test

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store/storetest"
)

// A sweep that finds nothing passes every barrage ever written, which is the
// instrument measuring its own silence. So it is tested against stores that are
// broken on purpose, and against one that is merely busy.

func res(id, kind string, attrs map[string]any) *resource.Resource {
	return &resource.Resource{
		ID:     id,
		Kind:   kind,
		Tenant: resource.Tenant{Provider: "scaleway", Zone: "fr-par-1"},
		Attrs:  attrs,
	}
}

// The accepting half, and the one that matters most: a healthy store produces no
// finding, including the shapes that look like breaches and are not.
func TestAHealthyStoreSweepsClean(t *testing.T) {
	server := res("s1", "instance/server", map[string]any{
		"name": "web",
		// The same address the flexible IP below carries. Legitimate: one is the
		// address resource, the other is the server it is attached to, and a
		// sweep that flagged this would fail every healthy store.
		"public_ips": []any{map[string]any{"address": "203.0.113.7"}},
		// A subnet, which several resources share by design.
		"subnet": "10.182.0.0/24",
	})
	address := res("ip1", "instance/ip", map[string]any{"address": "203.0.113.7"})
	network := res("pn1", "vpc/private-network", map[string]any{"subnet": "10.182.0.0/24"})

	// Two volumes naming one server in Runtime: a reference, not an object each
	// of them owns.
	volA := res("v1", "instance/volume", map[string]any{"name": "a"})
	volA.Runtime = map[string]string{"server": "s1"}
	volB := res("v2", "instance/volume", map[string]any{"name": "b"})
	volB.Runtime = map[string]string{"server": "s1"}

	server.Runtime = map[string]string{"machine": "feint-scw-s1"}

	// The server and the address record hold one address, legitimately: the
	// record *is* the flexible IP, the server carries it. Since the invariant
	// compares across kinds (#210), that pair has to be declared — and declaring
	// it is the point, because the alternative is a control that stays silent
	// about every collision in order to stay silent about this one.
	attached := func(a, b *resource.Resource) bool {
		kinds := map[string]bool{a.Kind: true, b.Kind: true}
		return kinds["instance/server"] && kinds["instance/ip"]
	}
	if found := storetest.Sweep([]*resource.Resource{server, address, network, volA, volB},
		nil, attached); len(found) != 0 {
		t.Errorf("a healthy store produced findings, so the sweep would cry wolf on every run:\n%s",
			strings.Join(found, "\n"))
	}

	// And without the declaration the pair is reported, which is what makes the
	// exemption a decision rather than a default.
	if found := storetest.Sweep([]*resource.Resource{server, address}, nil, nil); len(found) == 0 {
		t.Error("an undeclared pair sharing an address was not reported; the strict reading is the default")
	}
}

func TestTheSweepSeesAnIdentifierIssuedTwice(t *testing.T) {
	found := storetest.Sweep([]*resource.Resource{
		res("same", "instance/server", map[string]any{"name": "a"}),
		res("same", "instance/ip", map[string]any{"address": "203.0.113.1"}),
	}, nil, nil)
	if len(found) == 0 {
		t.Fatal("two resources sharing an identifier swept clean, and the store keys on it")
	}
	if !strings.Contains(found[0], "same") {
		t.Errorf("the finding does not name the identifier: %q", found[0])
	}
}

func TestTheSweepSeesOneAddressAllocatedTwice(t *testing.T) {
	found := storetest.Sweep([]*resource.Resource{
		res("ip1", "instance/ip", map[string]any{"address": "203.0.113.9"}),
		res("ip2", "instance/ip", map[string]any{"address": "203.0.113.9"}),
	}, nil, nil)
	if len(found) == 0 {
		t.Fatal("one address held by two resources of one kind swept clean")
	}
	if !strings.Contains(found[0], "203.0.113.9") {
		t.Errorf("the finding does not name the address: %q", found[0])
	}

	// Nested the way a pack really carries it, since the walk is what makes the
	// sweep independent of attribute names.
	nested := storetest.Sweep([]*resource.Resource{
		res("s1", "instance/server", map[string]any{
			"public_ips": []any{map[string]any{"address": "198.51.100.4"}}}),
		res("s2", "instance/server", map[string]any{
			"public_ips": []any{map[string]any{"address": "198.51.100.4"}}}),
	}, nil, nil)
	if len(nested) == 0 {
		t.Error("a duplicated address nested inside a list swept clean")
	}
}

func TestTheSweepSeesTwoResourcesClaimingOneMachine(t *testing.T) {
	a := res("s1", "instance/server", map[string]any{"name": "a"})
	a.Runtime = map[string]string{"machine": "feint-scw-orphan"}
	b := res("s2", "instance/server", map[string]any{"name": "b"})
	b.Runtime = map[string]string{"machine": "feint-scw-orphan"}

	found := storetest.Sweep([]*resource.Resource{a, b}, nil, nil)
	if len(found) == 0 {
		t.Fatal("two resources naming one runtime machine swept clean: deleting either destroys the other's")
	}
	if !strings.Contains(found[0], "feint-scw-orphan") {
		t.Errorf("the finding does not name the object: %q", found[0])
	}
}

// A nil element is what a hand-edited snapshot produces, and the sweep runs on
// restored stores. Crashing there would make the instrument the outage.
func TestTheSweepSurvivesANilResource(t *testing.T) {
	found := storetest.Sweep([]*resource.Resource{
		nil,
		res("s1", "instance/server", map[string]any{"name": "a"}),
	}, nil, nil)
	if len(found) != 0 {
		t.Errorf("a nil element produced findings: %v", found)
	}
}

// One address on two resources of one kind, one of them gone: not a breach.
//
// The blind reading of this exact store is the false verdict of 2026-08-16 — a
// terminated Vm's address, legitimately reused, reported as a double
// allocation, and a correct allocator fix reverted on the strength of it. The
// liveness vocabulary stays the pack's: it arrives as the predicate, and nil
// keeps the strict reading, which is the loud failure direction for a pack
// that forgets to pass one.
func TestAGoneResourcesAddressIsNotShared(t *testing.T) {
	dead := res("vm-dead", "vm", map[string]any{"PrivateIp": "10.100.1.4"})
	dead.State = "terminated"
	live := res("vm-live", "vm", map[string]any{"PrivateIp": "10.100.1.4"})
	both := []*resource.Resource{dead, live}
	gone := func(r *resource.Resource) bool { return r.State == "terminated" }

	if found := storetest.Sweep(both, gone, nil); len(found) != 0 {
		t.Errorf("an address inherited from a gone resource was reported shared:\n%s",
			strings.Join(found, "\n"))
	}

	// Both halves: without the pack's word, the strict reading still bites —
	// and two living holders stay a breach whatever predicate is passed.
	if found := storetest.Sweep(both, nil, nil); len(found) == 0 {
		t.Error("with no liveness predicate the sweep stopped seeing the shared address at all")
	}
	dead.State = "running"
	if found := storetest.Sweep(both, gone, nil); len(found) == 0 {
		t.Error("two living resources share an address and the predicate excused one of them")
	}
}

// One address handed to two resources is a finding whatever their kinds (#210).
//
// The invariant used to be keyed by kind, so it compared machines with machines
// and never a machine with anything else — while its own header promised the
// general case. A probe proved the blindness: two live resources of different
// kinds carrying one address, zero findings.
//
// That is worse than a missing control. This sweep is the barrage's invariant on
// all three packs, and a green barrage was being read as "no address is shared".
// The same file's liveness blindness had already produced a false verdict that
// got a correct fix reverted (#208).
func TestAnAddressSharedAcrossKindsIsReported(t *testing.T) {
	resources := []*resource.Resource{
		{ID: "i-1", Kind: "vm", Attrs: map[string]any{"PrivateIp": "10.0.0.4"}},
		{ID: "lb-1", Kind: "load-balancer", Attrs: map[string]any{"address": "10.0.0.4"}},
	}
	found := storetest.Sweep(resources, nil, nil)
	if len(found) == 0 {
		t.Fatal("two live resources of different kinds hold 10.0.0.4 and the sweep said nothing")
	}
	if !strings.Contains(found[0], "10.0.0.4") ||
		!strings.Contains(found[0], "i-1") || !strings.Contains(found[0], "lb-1") {
		t.Errorf("the finding must name the address and both holders, got %q", found[0])
	}

	// The accepting half: one resource carrying an address twice is not a
	// collision with itself, and two resources on different addresses are fine.
	clean := []*resource.Resource{
		{ID: "i-1", Kind: "vm", Attrs: map[string]any{
			"PrivateIp": "10.0.0.4", "PublicIp": "203.0.113.4"}},
		{ID: "i-2", Kind: "vm", Attrs: map[string]any{"PrivateIp": "10.0.0.5"}},
	}
	if found := storetest.Sweep(clean, nil, nil); len(found) != 0 {
		t.Errorf("a clean set was reported as sharing: %v", found)
	}
}
