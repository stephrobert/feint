package scaleway

import (
	"context"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
	"github.com/stephrobert/feint/internal/core/store"
)

// egressPack is a pack over an empty store, with the clock and the identifiers
// pinned the way every test in this package pins them.
func egressPack() *Pack {
	var seq int
	env := &emulator.Env{
		Store: store.New(),
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string { seq++; return "00000000-0000-4000-8000-00000000000" + string(rune('0'+seq)) },
	}
	return New(env)
}

// serverOn stores a server, a private network with a backing runtime network,
// and the NIC that joins them.
func serverOn(p *Pack, networkName string) *resource.Resource {
	now := p.env.Now()
	server := resource.New("srv-1", kindServer, resource.Tenant{Provider: Name, Zone: "fr-par-1"}, "running", now)
	server.Attrs = map[string]any{"name": "web"}
	p.env.Store.Put(server)

	pn := resource.New("pn-1", kindPrivateNetwork, resource.Tenant{Provider: Name, Zone: "fr-par"}, "available", now)
	pn.Runtime = map[string]string{runtimeNetworkKey: networkName}
	p.env.Store.Put(pn)

	nic := resource.New("nic-1", kindPrivateNIC, resource.Tenant{Provider: Name, Zone: "fr-par-1"}, "available", now)
	nic.Runtime = map[string]string{runtimePrivateNetworkKey: "pn-1", runtimeServerKey: "srv-1"}
	nic.Attrs = map[string]any{"server_id": "srv-1", "private_network_id": "pn-1"}
	p.env.Store.Put(nic)
	return server
}

// A server holding a public address KEEPS the route that address carries, and
// egress asks for nothing (#660, correcting #647).
//
// #647 had this return the server's private network, on the reasoning that a
// public address rides the network its NIC is on. True of a machine whose only
// NIC is the routed one; false the moment it also joins a Private Network, and
// the example stacks caught it on the first scheduled night: platform-web-0
// served 443 inside its machine and answered nothing at its published address.
//
// Measured 2026-09-04 under --vm incus-ovn, a server with a public address and
// one private NIC:
//
//	default via 10.77.0.1 dev eth0        <- the private gateway
//	eth0 carries 10.77.0.2/24 and 203.0.113.2/32
//	from the station: nothing
//
// The reply was leaving by a door the request had not come in through. The same
// server without the private NIC keeps `default via 169.254.0.1`, which
// RouteAddress lays for the routed address, and answers. So the route has an
// owner already, and the only correct thing for egress to do is nothing.
//
// Which is why NoEgress exists: an empty network name means "leave it alone"
// here and "take it away" for the server below, and one string could not say
// both.
func TestAServerWithAPublicAddressKeepsTheRouteItsAddressCarries(t *testing.T) {
	p := egressPack()
	server := serverOn(p, "fnt-web")

	// With neither address nor gateway: no network, and the route is refused.
	if got := p.egressNetworkOf(server); got != "" {
		t.Fatalf("a server with no address and no gateway leaves through %q, want nothing", got)
	}
	if !p.hasNoWayOut(server) {
		t.Error("a server with no address and no gateway is not refused its route")
	}

	address := resource.New("ip-1", kindIP, resource.Tenant{Provider: Name, Zone: "fr-par-1"}, "available", p.env.Now())
	address.Attrs = map[string]any{"address": "203.0.113.2", "server": map[string]any{"id": "srv-1"}}
	p.env.Store.Put(address)

	if got := p.egressNetworkOf(server); got != "" {
		t.Errorf("a server holding a public address asks for egress through %q, and asking for any "+
			"network here replaces the route its own address carries", got)
	}
	// And it is NOT refused: the difference between the two empty answers.
	if p.hasNoWayOut(server) {
		t.Error("a server holding a public address is refused its default route, which takes away " +
			"the route RouteAddress laid and leaves it unreachable at its published address")
	}
}

// A private server's only way out is a Public Gateway that pushes the default
// route, and it goes when the attachment goes (#647).
//
// The cloud's own words, from the document upstream:sync vendors —
// public-gateway-v2.yml, push_default_route: "Enabling the default route also
// enables masquerading." A gateway that masquerades without pushing the route
// is a gateway whose clients were told to route another way, and the emulator
// has no business deciding for them: that is why the flag is read rather than
// the attachment's mere existence.
func TestAPushedDefaultRouteIsTheOnlyWayOutForAPrivateServer(t *testing.T) {
	p := egressPack()
	server := serverOn(p, "fnt-web")

	attach := func(pushed bool) {
		gn := resource.New("gn-1", kindGatewayNetwork, resource.Tenant{Provider: Name, Zone: "fr-par-1"},
			"ready", p.env.Now())
		gn.Attrs = map[string]any{
			"gateway_id":         "gw-1",
			"private_network_id": "pn-1",
			"push_default_route": pushed,
			"masquerade_enabled": true,
		}
		p.env.Store.Put(gn)
	}

	// Masquerading without pushing: no way out, because the gateway did not
	// offer one. This is the half that keeps the emulator from being more
	// permissive than the cloud.
	attach(false)
	if got := p.egressNetworkOf(server); got != "" {
		t.Errorf("a gateway that pushes no default route gave a way out through %q", got)
	}
	if !p.hasNoWayOut(server) {
		t.Error("a gateway that pushes no default route is not read as no way out, so the route is left to whatever DHCP laid")
	}

	attach(true)
	if got := p.egressNetworkOf(server); got != "fnt-web" {
		t.Errorf("a gateway pushing the default route gave %q, want fnt-web", got)
	}
	if p.hasNoWayOut(server) {
		t.Error("a gateway pushing the default route still leaves the server refused one")
	}

	// A public address beside that gateway owns the route (#660): the answer
	// is to leave it alone, not to replace it with the gateway's, and the
	// server is not refused one either. Then the address goes, and the
	// gateway's network is the way out again — which proves the address, and
	// not something else, was the reason.
	address := resource.New("ip-1", kindIP, resource.Tenant{Provider: Name, Zone: "fr-par-1"}, "available", p.env.Now())
	address.Attrs = map[string]any{"address": "203.0.113.2", "server": map[string]any{"id": "srv-1"}}
	p.env.Store.Put(address)
	if got := p.egressNetworkOf(server); got != "" {
		t.Errorf("a server holding a public address beside a pushing gateway asks for egress through %q, "+
			"which replaces the route its address carries", got)
	}
	if p.hasNoWayOut(server) {
		t.Error("a server holding a public address beside a pushing gateway is refused its route")
	}
	p.env.Store.Delete(Name, kindIP, "ip-1")
	if got := p.egressNetworkOf(server); got != "fnt-web" {
		t.Errorf("the address is gone and the gateway's way out did not come back: %q", got)
	}

	// And it is taken back with the attachment, which is what the driver's
	// DropEgress needs to be told.
	p.env.Store.Delete(Name, kindGatewayNetwork, "gn-1")
	if got := p.egressNetworkOf(server); got != "" {
		t.Errorf("the attachment is gone and the server still leaves through %q", got)
	}
	if !p.hasNoWayOut(server) {
		t.Error("the attachment is gone and the server is still not refused its route")
	}
}

// A replay reaches the machines on THAT network and nobody else (#678).
//
// The guard against an empty identifier is not defensive tidiness: a detach of a
// half-built attachment hands one, and an empty identifier matches every NIC
// that has not joined a network yet — so an unrelated platform would be
// reconfigured by somebody else's gateway.
func TestAReplayVisitsOnlyTheMachinesOnThatNetwork(t *testing.T) {
	p := egressPack()
	serverOn(p, "fnt-web")

	// A NIC that joined nothing, which is what the empty-identifier guard is
	// about: without it, an empty id matches this one and an unrelated machine
	// is reconfigured by somebody else's gateway. A fixture without such a NIC
	// cannot tell — the first version of this test had none and the mutation
	// stayed green.
	orphan := resource.New("nic-orphan", kindPrivateNIC,
		resource.Tenant{Provider: Name, Zone: "fr-par-1"}, "available", p.env.Now())
	orphan.Runtime = map[string]string{runtimeServerKey: "srv-1"}
	p.env.Store.Put(orphan)

	if got := p.replayEgressOn(context.Background(), "pn-1"); got != 1 {
		t.Errorf("the replay visited %d machine(s) on their own network, want 1", got)
	}
	if got := p.replayEgressOn(context.Background(), "pn-somebody-else"); got != 0 {
		t.Errorf("the replay visited %d machine(s) on another network's gesture, want 0", got)
	}
	if got := p.replayEgressOn(context.Background(), ""); got != 0 {
		t.Errorf("an empty network identifier visited %d machine(s), want 0: "+
			"a detach of a half-built attachment hands one, and it matches every NIC that joined nothing", got)
	}
}
