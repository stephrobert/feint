package scaleway

import (
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

// A server holding a public address reaches the Internet, through the network
// its own NIC is on (#647).
//
// The rule is Scaleway's and the emulator had none of it: measured under
// --vm incus-ovn, a bastion with a public address and three private NICs kept
// its default route out of eth0 — the interface #548 takes the address OFF —
// and could not reach anything, while every SSH hop through it worked. A fleet
// tool could discover the machine and never provision it.
func TestAServerWithAPublicAddressLeavesThroughItsOwnNetwork(t *testing.T) {
	p := egressPack()
	server := serverOn(p, "fnt-web")

	if got := p.egressNetworkOf(server); got != "" {
		t.Fatalf("a server with no address and no gateway leaves through %q, want nothing", got)
	}

	address := resource.New("ip-1", kindIP, resource.Tenant{Provider: Name, Zone: "fr-par-1"}, "available", p.env.Now())
	address.Attrs = map[string]any{"address": "203.0.113.2", "server": map[string]any{"id": "srv-1"}}
	p.env.Store.Put(address)

	if got := p.egressNetworkOf(server); got != "fnt-web" {
		t.Errorf("a server holding a public address leaves through %q, want fnt-web", got)
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

	attach(true)
	if got := p.egressNetworkOf(server); got != "fnt-web" {
		t.Errorf("a gateway pushing the default route gave %q, want fnt-web", got)
	}

	// And it is taken back with the attachment, which is what the driver's
	// DropEgress needs to be told.
	p.env.Store.Delete(Name, kindGatewayNetwork, "gn-1")
	if got := p.egressNetworkOf(server); got != "" {
		t.Errorf("the attachment is gone and the server still leaves through %q", got)
	}
}
