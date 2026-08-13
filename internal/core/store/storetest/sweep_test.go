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

	if found := storetest.Sweep([]*resource.Resource{server, address, network, volA, volB}); len(found) != 0 {
		t.Errorf("a healthy store produced findings, so the sweep would cry wolf on every run:\n%s",
			strings.Join(found, "\n"))
	}
}

func TestTheSweepSeesAnIdentifierIssuedTwice(t *testing.T) {
	found := storetest.Sweep([]*resource.Resource{
		res("same", "instance/server", map[string]any{"name": "a"}),
		res("same", "instance/ip", map[string]any{"address": "203.0.113.1"}),
	})
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
	})
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
	})
	if len(nested) == 0 {
		t.Error("a duplicated address nested inside a list swept clean")
	}
}

func TestTheSweepSeesTwoResourcesClaimingOneMachine(t *testing.T) {
	a := res("s1", "instance/server", map[string]any{"name": "a"})
	a.Runtime = map[string]string{"machine": "feint-scw-orphan"}
	b := res("s2", "instance/server", map[string]any{"name": "b"})
	b.Runtime = map[string]string{"machine": "feint-scw-orphan"}

	found := storetest.Sweep([]*resource.Resource{a, b})
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
	})
	if len(found) != 0 {
		t.Errorf("a nil element produced findings: %v", found)
	}
}
