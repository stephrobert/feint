package exoscale

import (
	"context"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/resource"
)

// Exoscale's security groups stop at the public interface, and this pack has
// to say so (#574).
//
// Upstream's Private Network Overview is explicit: "Security group rules do
// not apply to traffic inside private networks." From a344f8d to 2026-08-27
// this pack handed the runtime a machine-wide binding, so the `default`
// group's rule set — no ingress rule, therefore a drop default — landed on the
// membership NIC and two instances of one private network could not reach each
// other. Measured under `--vm incus`: 0/10 probes with the rule set on the
// NIC, 10/10 with it stripped off by hand.
//
// The assertion is on what the pack declares to the shared layer, because that
// is where the fact belongs — the two other packs do filter their private
// NICs, so this is a field on the attachment, not a rule of the layer.
func TestALateNetworkAttachLeavesThePrivateInterfaceUnfiltered(t *testing.T) {
	driver := newFirewallDriver()
	p := sequencedPack(driver)
	group := storedSecurityGroup(p, "web", []any{map[string]any{
		"id": "r1", "flow-direction": "ingress", "protocol": "tcp",
		"network": "0.0.0.0/0", "start-port": 443, "end-port": 443,
	}})
	inst := storedInstance(p, "192.0.2.7", group.ID)
	p.start(context.Background(), inst)
	p.env.Store.Put(inst)
	machineName := inst.Runtime["machine"]

	r := httptest.NewRequest("POST", "/v2/private-network",
		strings.NewReader(`{"name":"back","start-ip":"10.90.2.20","end-ip":"10.90.2.200","netmask":"255.255.255.0"}`))
	p.createPrivateNetwork(httptest.NewRecorder(), r)
	pns := p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name})
	if len(pns) != 1 {
		t.Fatalf("%d private networks, want 1", len(pns))
	}
	backing := pns[0].Runtime[runtimeNetworkKey]
	if backing == "" {
		t.Fatal("the private network has no backing network, so this test would assert about nothing")
	}

	ar := httptest.NewRequest("POST", "/v2/private-network/"+pns[0].ID+":attach",
		strings.NewReader(`{"instance":{"id":"`+inst.ID+`"}}`))
	ar.SetPathValue("id", pns[0].ID)
	p.attachInstanceToPrivateNetwork(httptest.NewRecorder(), ar)

	binding, applied := driver.applied[machineName]
	if !applied {
		t.Fatal("the attach applied no firewall binding at all")
	}
	if !slices.Contains(binding.Unfiltered, backing) {
		t.Fatalf("the private network %s is not declared out of the groups' scope: %+v", backing, binding)
	}
	// The accepting half, and the reason this is a scope rather than a
	// switch: the machine still wears its group, so whatever interface the
	// groups *do* cover keeps being filtered.
	if len(binding.Names) == 0 {
		t.Error("the instance wears a restrictive group and the binding names no rule set at all")
	}
}
