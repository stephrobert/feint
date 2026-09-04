package machine

import (
	"net/netip"
	"slices"
	"strings"
)

// What a machine actually carries, read back from the runtime and normalised.
//
// The types are declared beside the derivation (#667) because the claims a
// plan makes are answered against them; the reading itself — the Observer
// half a driver implements, and the parsers behind it — is #668's, and nothing
// in this file reaches a runtime.
//
// Normalised means: what does not compare never enters a Shape. No metric, no
// `proto`, no IPv6, no loopback, no link-local, no ordering. A Shape carrying
// them would turn every legitimate difference between two boots — a lease
// renewed, a kernel ordering its table another way — into a broken verdict,
// and a verifier that cries wolf is switched off within the week.

// Shape is what the runtime reports about one machine.
type Shape struct {
	// Interfaces are the machine's own NIC devices, by the name the guest
	// gives them. Profile devices, which are the operator's, are not in it.
	Interfaces map[string]Interface
	// Routes is the guest's IPv4 table minus what does not compare: the
	// connected routes an address implies, the link-local ones the kernel
	// lays, and every attribute past `via` and `dev`. The default route is
	// the entry whose Dst is 0.0.0.0/0.
	Routes []Route
	// Doors answers, per source address, which door a reply sent from that
	// address leaves by — `ip route get <probe> from <address>`. Filled only
	// for the addresses a claim asked about.
	Doors map[netip.Addr]Route
	// Gateways is the gateway, with its mask, of every emulated network the
	// machine has an interface on, read by the observer so that the claims
	// stay pure: Expect never queries. A network absent here was not read,
	// and a claim that needs it answers Unreadable rather than guessing.
	Gateways map[string]netip.Prefix
}

// Interface is one NIC as the guest sees it.
type Interface struct {
	// Network is the emulated network the device sits on, empty for a routed
	// NIC, which sits on none (#202).
	Network string
	// Routed is the routed-NIC kind: an address handed to the guest with a
	// host route and no L2 segment underneath.
	Routed bool
	// Addresses are the global IPv4 the interface carries, with their masks.
	Addresses []netip.Prefix
	// RuleSets are the rule sets bound to the device (security.acls), sorted.
	RuleSets []string
}

// Route is one entry of the guest's table, reduced to what compares.
type Route struct {
	// Dst is the destination block; 0.0.0.0/0 for the default route.
	Dst netip.Prefix
	// Via is the next hop, the zero Addr for a connected route.
	Via netip.Addr
	// Dev is the guest's name for the interface the route leaves by.
	Dev string
}

// String renders a route the way a verdict cites it: "via 10.0.0.1 dev eth0",
// or "dev eth0" for a connected one.
func (r Route) String() string {
	if !r.Via.IsValid() {
		return "dev " + r.Dev
	}
	return "via " + r.Via.String() + " dev " + r.Dev
}

// defaultDst is the destination of the default route.
var defaultDst = netip.MustParsePrefix("0.0.0.0/0")

// defaultRoute answers the shape's default route, and whether it has one.
func (s Shape) defaultRoute() (Route, bool) {
	for _, r := range s.Routes {
		if r.Dst == defaultDst {
			return r, true
		}
	}
	return Route{}, false
}

// interfaceOn answers the name of the interface on a network, empty when the
// machine has none there. Name order, so two reads answer the same one.
func (s Shape) interfaceOn(network string) string {
	for _, name := range s.interfaceNames() {
		if s.Interfaces[name].Network == network {
			return name
		}
	}
	return ""
}

// interfaceNames is the interfaces in name order: a map has no order, and a
// verdict that named a different interface on two reads of one machine would
// be reporting the iteration rather than the machine.
func (s Shape) interfaceNames() []string {
	names := make([]string, 0, len(s.Interfaces))
	for name := range s.Interfaces {
		names = append(names, name)
	}
	slices.Sort(names)
	return names
}

// carriedBy renders what the named interfaces carry, for a broken verdict:
// "eth0: 10.30.1.10/24, 203.0.113.2/32; eth1: nothing".
func (s Shape) carriedBy(names []string) string {
	parts := make([]string, 0, len(names))
	for _, name := range names {
		addresses := s.Interfaces[name].Addresses
		if len(addresses) == 0 {
			parts = append(parts, name+": nothing")
			continue
		}
		rendered := make([]string, 0, len(addresses))
		for _, p := range addresses {
			rendered = append(rendered, p.String())
		}
		parts = append(parts, name+": "+strings.Join(rendered, ", "))
	}
	if len(parts) == 0 {
		return "no interface"
	}
	return strings.Join(parts, "; ")
}

// routesTo renders the entries of the table towards a block, for a broken
// verdict; "none" when the table holds nothing for it.
func (s Shape) routesTo(dst netip.Prefix) string {
	var found []string
	for _, r := range s.Routes {
		if r.Dst == dst {
			found = append(found, r.String())
		}
	}
	if len(found) == 0 {
		return "none"
	}
	return strings.Join(found, ", ")
}
