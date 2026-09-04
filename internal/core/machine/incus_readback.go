package machine

import (
	"context"
	"fmt"
	"net/netip"
	"slices"
	"strings"
)

// How Incus reads back what a machine carries (#668). The contract is in
// readback.go; what this file adds is the reading and the three parsers.
//
// Everything below is a read: one `query` for the devices, one `network get`
// per emulated network the machine sits on, and three execs into the guest —
// the addresses, the table, and one `route get` per door a claim asks about.
// Nothing here changes the host.
//
// No `-j`, deliberately. iproute2 renders JSON on request and busybox's `ip`,
// which is the one an Alpine image ships, does not — so the text is parsed,
// the way guestAddresses already parses `ip -4 -o addr show`. The three
// formats are stable across the two implementations because both print the
// same keywords in the same order (`via`, `dev`, `scope`), and the parsers
// scan for keywords rather than positions for exactly that reason.
// TestTheParsersReadBothImplementationsOfIp holds the pairs.

// linkLocal is the block the kernel lays its own routes in and a routed NIC
// reaches the host through. Neither compares: a route or an address here is
// the runtime's plumbing, never the plan's.
var linkLocal = netip.MustParsePrefix("169.254.0.0/16")

// Observe implements Observer.
//
// Own devices only, and that is a control rather than tidiness: a profile
// device is the operator's — their default bridge, their address — and a
// verifier that read it would judge what no plan ever claimed. The same rule
// keeps `network get` off a network the emulator did not derive: the gateway
// of an operator's bridge is theirs, and a claim needing it answers Unreadable
// rather than reading it. TestObserveReadsOnlyTheMachinesOwnNICs and
// TestObserveReadsNoForeignNetwork fail without the two halves.
func (d *Incus) Observe(ctx context.Context, machine string) (Shape, error) {
	if !safeName.MatchString(machine) {
		return Shape{}, fmt.Errorf("invalid machine name %q", machine)
	}
	devices, err := d.instanceDevices(ctx, machine)
	if err != nil {
		return Shape{}, fmt.Errorf("inspect %s: %w", machine, err)
	}
	shape := Shape{Interfaces: map[string]Interface{}, Gateways: map[string]netip.Prefix{}}
	names := make([]string, 0, len(devices.own))
	for device, cfg := range devices.own {
		if cfg["type"] == "nic" {
			names = append(names, device)
		}
	}
	slices.Sort(names)
	for _, device := range names {
		cfg := devices.own[device]
		// The guest's name for it: the same as the device's on a container,
		// the kernel's PCI name on a virtual machine (guestInterface).
		iface, err := d.guestInterface(ctx, machine, device)
		if err != nil {
			return Shape{}, err
		}
		shape.Interfaces[iface] = Interface{
			Network:  cfg["network"],
			Routed:   cfg["nictype"] == "routed",
			RuleSets: sortedList(cfg["security.acls"]),
		}
		network := cfg["network"]
		if network == "" || !ownedNetwork(network) {
			continue
		}
		if _, read := shape.Gateways[network]; read {
			continue
		}
		gateway, err := d.networkGateway(ctx, network)
		if err != nil {
			return Shape{}, err
		}
		shape.Gateways[network] = gateway
	}

	out, err := d.run(ctx, "exec", machine, "--", "ip", "-4", "-o", "addr", "show")
	if err != nil {
		return Shape{}, fmt.Errorf("read the addresses of %s: %w", machine, err)
	}
	for iface, addresses := range parseAddresses(out) {
		entry, ours := shape.Interfaces[iface]
		if !ours {
			// lo, or an interface behind a device this emulator does not own.
			continue
		}
		entry.Addresses = addresses
		shape.Interfaces[iface] = entry
	}

	out, err = d.run(ctx, "exec", machine, "--", "ip", "-4", "route", "show")
	if err != nil {
		return Shape{}, fmt.Errorf("read the routes of %s: %w", machine, err)
	}
	shape.Routes = parseRoutes(out)
	return shape, nil
}

// Door implements Observer.
func (d *Incus) Door(ctx context.Context, machine string, from, to netip.Addr) (Route, error) {
	if !safeName.MatchString(machine) {
		return Route{}, fmt.Errorf("invalid machine name %q", machine)
	}
	out, err := d.run(ctx, "exec", machine, "--",
		"ip", "-4", "route", "get", to.String(), "from", from.String())
	if err != nil {
		if strings.Contains(strings.ToLower(err.Error()), "network is unreachable") {
			// A fact about the machine — it has no route there — rather than
			// a failure to read it. The zero Route says so.
			return Route{}, nil
		}
		return Route{}, fmt.Errorf("read the door of %s from %s: %w", machine, from, err)
	}
	route, err := parseDoor(out)
	if err != nil {
		return Route{}, fmt.Errorf("read the door of %s from %s: %w", machine, from, err)
	}
	route.Dst = netip.PrefixFrom(to, to.BitLen())
	return route, nil
}

// sortedList reads a comma-separated device value into a sorted list.
func sortedList(value string) []string {
	out := splitList(value)
	slices.Sort(out)
	return out
}

// parseAddresses reads `ip -4 -o addr show`: one address per line, the
// interface second, the CIDR after `inet`, and the scope after `scope`. Only a
// global address enters the Shape; link-local and host scopes are the
// kernel's own.
//
//	iproute2  2: eth0    inet 10.30.1.10/24 brd 10.30.1.255 scope global eth0\       valid_lft forever preferred_lft forever
//	busybox   2: eth0    inet 10.30.1.10/24 scope global eth0
func parseAddresses(out []byte) map[string][]netip.Prefix {
	found := map[string][]netip.Prefix{}
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) < 4 {
			continue
		}
		iface := strings.TrimSuffix(fields[1], ":")
		var cidr, scope string
		for i, field := range fields {
			if i+1 >= len(fields) {
				break
			}
			switch field {
			case "inet":
				cidr = fields[i+1]
			case "scope":
				scope = fields[i+1]
			}
		}
		if cidr == "" || scope != "global" {
			continue
		}
		prefix, err := netip.ParsePrefix(cidr)
		if err != nil || !prefix.Addr().Is4() || linkLocal.Contains(prefix.Addr()) {
			continue
		}
		found[iface] = append(found[iface], prefix)
	}
	return found
}

// parseRoutes reads `ip -4 route show` down to what compares: a destination,
// a next hop and a device. Everything else on the line — proto, src, metric,
// onlink, linkdown — is dropped, and so is every line with no `via`: a
// connected route is implied by the address that created it, and comparing
// it would compare the address twice.
//
//	iproute2  default via 10.30.1.1 dev eth0 proto dhcp src 10.30.1.10 metric 100
//	          10.30.1.0/24 dev eth0 proto kernel scope link src 10.30.1.10
//	busybox   default via 10.30.1.1 dev eth0  metric 100
//	          10.0.0.0/8 via 10.30.1.1 dev eth0
func parseRoutes(out []byte) []Route {
	var routes []Route
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		var dst netip.Prefix
		switch first := fields[0]; first {
		case "default":
			dst = defaultDst
		case "unreachable", "blackhole", "prohibit", "throw", "local", "broadcast", "multicast", "anycast", "nat":
			continue
		default:
			parsed, err := parseDestination(first)
			if err != nil {
				continue
			}
			dst = parsed
		}
		via, dev := nextHop(fields)
		if !via.IsValid() || dev == "" {
			continue
		}
		if dst != defaultDst && linkLocal.Contains(dst.Addr()) {
			continue
		}
		routes = append(routes, Route{Dst: dst.Masked(), Via: via, Dev: dev})
	}
	return routes
}

// parseDoor reads the first line of `ip -4 route get <to> from <from>`:
//
//	iproute2  10.209.83.1 from 203.0.113.2 via 169.254.0.1 dev eth0 uid 0
//	busybox   10.209.83.1 from 203.0.113.2 via 169.254.0.1 dev eth0  src 203.0.113.2
//
// and a connected answer names the device alone. The device is required: an
// answer with none is not a door.
func parseDoor(out []byte) (Route, error) {
	for _, line := range strings.Split(string(out), "\n") {
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		via, dev := nextHop(fields)
		if dev == "" {
			return Route{}, fmt.Errorf("no device in the answer %q", strings.TrimSpace(line))
		}
		return Route{Via: via, Dev: dev}, nil
	}
	return Route{}, fmt.Errorf("an empty answer")
}

// parseDestination reads a route's destination: a block, or a bare address
// the kernel prints for a host route.
func parseDestination(field string) (netip.Prefix, error) {
	if strings.Contains(field, "/") {
		prefix, err := netip.ParsePrefix(field)
		if err != nil {
			return netip.Prefix{}, err
		}
		if !prefix.Addr().Is4() {
			return netip.Prefix{}, fmt.Errorf("%s is not IPv4", field)
		}
		return prefix, nil
	}
	addr, err := netip.ParseAddr(field)
	if err != nil {
		return netip.Prefix{}, err
	}
	if !addr.Is4() {
		return netip.Prefix{}, fmt.Errorf("%s is not IPv4", field)
	}
	return netip.PrefixFrom(addr, addr.BitLen()), nil
}

// nextHop scans a route line for its `via` and `dev`, wherever they sit.
func nextHop(fields []string) (via netip.Addr, dev string) {
	for i, field := range fields {
		if i+1 >= len(fields) {
			break
		}
		switch field {
		case "via":
			if addr, err := netip.ParseAddr(fields[i+1]); err == nil {
				via = addr
			}
		case "dev":
			dev = fields[i+1]
		}
	}
	return via, dev
}
