package machine

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"net/netip"
	"strings"
)

// OVN is the mode where emulated subnets are separate by construction.
//
// Two managed bridges on one host are routed to each other, and every attempt
// to separate them with reject rules eventually leaked (docs/limits.md keeps
// the measurements). An OVN network is a logical network with its own router:
// another OVN network is simply not on it, so isolation is what you get by
// not asking for a connection, instead of what you build out of rules. Two
// networks that must reach each other are peered, which is the runtime's own
// notion (`network peer`), and exactly the shape of a VPC that routes between
// its subnets.
//
// The mode costs a prerequisite: ovn-central, ovn-host and an Open vSwitch
// wired to the local northbound socket. The bridge mode therefore stays the
// default; nothing here runs unless the driver was built with OVN set.
//
// Everything below was measured on Incus 7.2 with OVN 24.03 before being
// relied on; where a behaviour comes from the Incus source at v7.2.0, the
// comment says which file.

const (
	// DefaultUplinkName is the bridge OVN networks reach the outside through.
	// One uplink for every emulated network: the uplink is plumbing, not an
	// emulated resource, and it carries the label so a sweep removes it.
	DefaultUplinkName = "feint-uplink"
	// DefaultUplinkCIDR is the block the uplink carries. Deliberately obscure
	// for the same reason the conformance suite picks 10.181.0.0/24: a
	// collision with a block already routed on the operator's host makes the
	// create fail, and failing is better than capturing someone's traffic.
	DefaultUplinkCIDR = "10.209.83.0/24"
)

// delegateRoute adds one block to the uplink's ipv4.routes.
//
// An OVN network must sit inside its uplink's routes, and Incus says so in terms
// that name the network rather than the setting: "Uplink network doesn't contain
// 10.191.4.0/24 in its routes". Since a client picks its own address plan, and
// it is never the uplink's own /24, the block has to be delegated as the network
// is created.
//
// One block at a time, never a blanket range. Delegating 10.0.0.0/8 up front
// looks tidier and fails: Incus turns each route into a real route on the host,
// and a /8 collides with whatever already lives in that space — measured on a
// machine whose own bridge sat at 10.108.0.0/24, where the whole uplink then
// refused to come up.
//
// Idempotent by construction: a block already delegated is left alone, so
// creating the same network twice does not grow the list.
func (d *Incus) delegateRoute(ctx context.Context, block string) error {
	name := d.uplinkName()
	out, err := d.run(ctx, "network", "get", name, "ipv4.routes")
	if err != nil {
		return fmt.Errorf("read routes on uplink %s: %w", name, err)
	}

	current := strings.TrimSpace(string(out))
	routes := []string{}
	if current != "" {
		routes = strings.Split(current, ",")
	}
	for _, route := range routes {
		if strings.TrimSpace(route) == block {
			return nil
		}
	}
	routes = append(routes, block)

	if _, err := d.run(ctx, "network", "set", name, "ipv4.routes="+strings.Join(routes, ",")); err != nil {
		return fmt.Errorf("delegate %s to uplink %s: %w", block, name, err)
	}
	return nil
}

// uplinkName returns the uplink network name, allowing an override.
func (d *Incus) uplinkName() string {
	if d.Uplink != "" {
		return d.Uplink
	}
	return DefaultUplinkName
}

func (d *Incus) uplinkCIDR() string {
	if d.UplinkCIDR != "" {
		return d.UplinkCIDR
	}
	return DefaultUplinkCIDR
}

// ensureUplink creates the uplink bridge OVN networks attach to, once.
//
// An OVN network refuses to exist without an uplink to allocate its router
// address from (ipv4.ovn.ranges), so this runs before the first network. An
// existing network under the name is only reused when it carries the
// emulator's label: the operator's own bridges are never conscripted.
func (d *Incus) ensureUplink(ctx context.Context) error {
	name := d.uplinkName()
	if out, err := d.run(ctx, "query", "/1.0/networks/"+name); err == nil {
		var existing struct {
			Type   string            `json:"type"`
			Config map[string]string `json:"config"`
		}
		if err := json.Unmarshal(out, &existing); err != nil {
			return fmt.Errorf("decode uplink %s: %w", name, err)
		}
		if existing.Type != "bridge" || existing.Config["user."+LabelKey] == "" {
			return fmt.Errorf("network %s exists and is not the emulator's uplink; refusing to reuse it", name)
		}
		return nil
	} else if !isNotFound(err) {
		return fmt.Errorf("inspect uplink %s: %w", name, err)
	}

	prefix, err := netip.ParsePrefix(d.uplinkCIDR())
	if err != nil {
		return fmt.Errorf("parse uplink CIDR %q: %w", d.uplinkCIDR(), err)
	}
	dhcp, ovn, err := uplinkRanges(prefix)
	if err != nil {
		return err
	}
	gateway := fmt.Sprintf("%s/%d", prefix.Masked().Addr().Next(), prefix.Bits())
	// NAT on: outbound traffic from an OVN network is first SNATed to the
	// uplink by the OVN router, then to the world by the host. Without the
	// second hop the emulated machines lose outbound access, which is what
	// their NAT-enabled subnets promise.
	if _, err := d.run(ctx, "network", "create", name,
		"ipv4.address="+gateway,
		"ipv4.nat=true",
		"ipv6.address=none",
		"ipv4.dhcp.ranges="+dhcp,
		"ipv4.ovn.ranges="+ovn,
		"user."+LabelKey+"=feint"); err != nil {
		return fmt.Errorf("create uplink %s (%s): %w", name, gateway, err)
	}
	return nil
}

// uplinkRanges splits an uplink block into the two ranges Incus wants: DHCP
// for anything bridged directly, ipv4.ovn.ranges for the router addresses OVN
// networks draw. They must not overlap, or the same address is handed out
// twice, so the block is cut in half after the gateway.
func uplinkRanges(prefix netip.Prefix) (dhcp, ovn string, err error) {
	if !prefix.Addr().Is4() {
		return "", "", fmt.Errorf("uplink block %s is not IPv4", prefix)
	}
	if prefix.Bits() > 28 {
		return "", "", fmt.Errorf("uplink block %s is too small, need a /28 or larger", prefix)
	}
	base := binary.BigEndian.Uint32(func() []byte { b := prefix.Masked().Addr().As4(); return b[:] }())
	size := uint32(1) << (32 - prefix.Bits())

	at := func(offset uint32) netip.Addr {
		var b [4]byte
		binary.BigEndian.PutUint32(b[:], base+offset)
		return netip.AddrFrom4(b)
	}
	// .0 is the network, .1 the gateway, the top address the broadcast.
	dhcp = fmt.Sprintf("%s-%s", at(2), at(size/2-1))
	ovn = fmt.Sprintf("%s-%s", at(size/2), at(size-2))
	return dhcp, ovn, nil
}

// ---- Peering ----------------------------------------------------------------

// NativeIsolation implements Peerer.
func (d *Incus) NativeIsolation() bool { return d.OVN }

// PeerNetworks implements Peerer.
//
// Reconciled, not appended: peers the caller no longer names are removed, so
// a VPC that stops routing between its subnets actually separates them again.
// A peering only carries traffic once both networks declare it, so both
// halves are created here; the second call for the same pair finds its work
// done and does nothing.
func (d *Incus) PeerNetworks(ctx context.Context, network string, peers []string) error {
	if !d.OVN {
		// Bridges on one host are routed together already; there is nothing to
		// join, and IsolateNetwork is what keeps them apart.
		return nil
	}
	if !safeName.MatchString(network) {
		return fmt.Errorf("invalid network name %q", network)
	}
	// Both ends are checked: a peering joins two networks, so naming one of the
	// operator's on either side would route the emulator's traffic into it.
	if !ownedNetwork(network) {
		return fmt.Errorf("refusing to peer network %q: not one the emulator created", network)
	}
	desired := make(map[string]bool, len(peers))
	for _, peer := range peers {
		if !safeName.MatchString(peer) {
			return fmt.Errorf("invalid network name %q", peer)
		}
		if !ownedNetwork(peer) {
			return fmt.Errorf("refusing to peer with network %q: not one the emulator created", peer)
		}
		desired[peer] = true
	}

	existing, err := d.networkPeers(ctx, network)
	if err != nil {
		return err
	}
	for _, row := range existing {
		// An empty target is the wreck of a peering whose other half is gone;
		// it is as stale as one pointing at a network no longer reachable.
		if row.Target != "" && desired[row.Target] {
			continue
		}
		if _, err := d.run(ctx, "network", "peer", "delete", network, row.Name); err != nil && !isNotFound(err) {
			return fmt.Errorf("remove stale peering %s of %s: %w", row.Name, network, err)
		}
	}
	for _, peer := range peers {
		if err := d.ensurePeerHalf(ctx, network, peer); err != nil {
			return err
		}
		if err := d.ensurePeerHalf(ctx, peer, network); err != nil {
			return err
		}
	}
	// And the half a peering does not provide. Peering says what may reach this
	// network; nothing said what may not, and the uplink this driver creates
	// routes every one of its OVN subnets to every other. The rule set is
	// applied here rather than in each pack, because a control copied into three
	// packs is a control one pack forgets and a fourth never has.
	return d.isolateOVN(ctx, network, peers)
}

type peerRow struct {
	Name   string
	Target string
}

func (d *Incus) networkPeers(ctx context.Context, network string) ([]peerRow, error) {
	out, err := d.run(ctx, "query", "/1.0/networks/"+network+"/peers?recursion=1")
	if err != nil {
		return nil, fmt.Errorf("list peers of %s: %w", network, err)
	}
	var raw []struct {
		Name          string `json:"name"`
		TargetNetwork string `json:"target_network"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode peers of %s: %w", network, err)
	}
	rows := make([]peerRow, 0, len(raw))
	for _, r := range raw {
		rows = append(rows, peerRow{Name: r.Name, Target: r.TargetNetwork})
	}
	return rows, nil
}

// ensurePeerHalf declares one direction of a peering, under the target's name
// so the pair is recognisable from either side. Racing another declaration of
// the same half is the success case arriving first.
func (d *Incus) ensurePeerHalf(ctx context.Context, from, to string) error {
	rows, err := d.networkPeers(ctx, from)
	if err != nil {
		return err
	}
	for _, row := range rows {
		if row.Target == to {
			return nil
		}
	}
	if _, err := d.run(ctx, "network", "peer", "create", from, to, to); err != nil &&
		!strings.Contains(strings.ToLower(err.Error()), "already exists") {
		return fmt.Errorf("peer %s with %s: %w", from, to, err)
	}
	return nil
}

// ---- Public addresses -------------------------------------------------------

// routeAddressOVN gives a machine a public address through the NIC's external
// routes, which is the bridge path's ipv4.routes translated into OVN's terms.
//
// ipv4.routes.external is what the runtime designed for this: with the
// uplink's default ovn.ingress_mode of l2proxy it becomes a stateless
// dnat_and_snat entry, and OVN then answers ARP requests for the address on
// the uplink (driver_ovn.go at v7.2.0, the l2proxy comment) and delivers
// packets carrying the public destination address, exactly Scaleway's
// routed-IP mode. A network forward was measured first and rejected: its load
// balancer VIP is only announced by a burst of gratuitous ARPs at creation
// time, so the address answered when probed within seconds and went dark
// afterwards — a nondeterminism this emulator cannot ship.
//
// The cost is a re-plug: the route keys are not live-updatable on an OVN NIC
// (UpdatableFields in nic_ovn.go at v7.2.0), so the device is removed and
// re-added, and the guest loses the interface's addresses. The repair below
// puts them back; the observable effect is a brief interface bounce at attach
// time, which real clouds also produce when reshaping a live NIC.
func (d *Incus) routeAddressOVN(ctx context.Context, spec AddressSpec) error {
	if !safeName.MatchString(spec.Machine) {
		return fmt.Errorf("invalid machine name %q", spec.Machine)
	}
	network, device, err := d.interfaceFor(ctx, spec.Machine, spec.Network)
	if err != nil {
		return err
	}
	if err := d.mustOwn(ctx, network); err != nil {
		return err
	}
	devices, err := d.instanceDevices(ctx, spec.Machine)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", spec.Machine, err)
	}

	// The uplink's route list is what the runtime validates external routes
	// against, and what routes the host's own traffic towards the uplink.
	if err := d.setUplinkRoute(ctx, spec.Address, true); err != nil {
		return err
	}

	route := spec.Address + "/32"
	if !routeListContains(devices.own[device]["ipv4.routes.external"], route) {
		merged := appendRoute(devices.own[device]["ipv4.routes.external"], route)
		if _, err := d.run(ctx, "config", "device", "set", spec.Machine, device,
			"ipv4.routes.external="+merged); err != nil {
			return fmt.Errorf("route %s to %s/%s: %w", spec.Address, spec.Machine, device, err)
		}
		if err := d.repairGuestInterface(ctx, spec.Machine, network, device); err != nil {
			return err
		}
	}

	// The machine answers on it, a /32 because the address is a route to this
	// machine, not a subnet it belongs to. Already-there is the second call's
	// success, in whichever wording the guest's `ip` uses (addressAlreadyThere:
	// busybox does not say "file exists", and Alpine ships busybox).
	if _, err := d.run(ctx, "exec", spec.Machine, "--",
		"ip", "address", "add", route, "dev", device); err != nil &&
		!addressAlreadyThere(err) {
		return fmt.Errorf("give %s to %s: %w", spec.Address, spec.Machine, err)
	}
	return nil
}

// unrouteAddressOVN takes a public address back: the mirror of the OVN half
// of RouteAddress, with the same re-plug and the same repair.
func (d *Incus) unrouteAddressOVN(ctx context.Context, machine, address string) error {
	route := address + "/32"
	if machine != "" {
		devices, err := d.instanceDevices(ctx, machine)
		if err != nil && !isNotFound(err) {
			return fmt.Errorf("inspect %s: %w", machine, err)
		}
		for device, cfg := range devices.own {
			if cfg["type"] != "nic" || !routeListContains(cfg["ipv4.routes.external"], route) {
				continue
			}
			kept := removeRoute(cfg["ipv4.routes.external"], route)
			if _, err := d.run(ctx, "config", "device", "set", machine, device,
				"ipv4.routes.external="+kept); err != nil {
				return fmt.Errorf("unroute %s from %s/%s: %w", address, machine, device, err)
			}
			// The re-plug already dropped every guest address, including the
			// public one; the repair puts back only what stays.
			if err := d.repairGuestInterface(ctx, machine, cfg["network"], device); err != nil {
				return err
			}
		}
	}
	return d.setUplinkRoute(ctx, address, false)
}

// repairGuestInterface restores what a device re-plug cost the guest: the
// address the interface carried, the link state, and the routes. Measured on
// 7.2: any change to a non-live-updatable key removes and re-adds the NIC,
// and the new interface comes up bare, with no DHCP client watching it.
//
// Two kinds of address, one repair. A pinned one is read off the device key.
// A DHCP-owned one used to be declared unrepairable here, which turned a hot
// route edit into a machine with no address at all — measured: RUNNING, guest
// bare, sshd unreachable. The runtime itself records what the interface
// carried (volatile.<device>.last_state.ip_addresses), the re-plug keeps the
// NIC's hwaddr, and OVN's IPAM ties the address to that MAC — so restoring
// the recorded address statically restores the port's own reservation, and
// the lease's default route comes back with it.
// TestAHotRouteEditRepairsADHCPInterface fails without the second kind.
func (d *Incus) repairGuestInterface(ctx context.Context, machine, network, device string) error {
	devices, err := d.instanceDevices(ctx, machine)
	if err != nil {
		return fmt.Errorf("inspect %s after re-plug: %w", machine, err)
	}
	address := devices.own[device]["ipv4.address"]
	leased := false
	if address == "" {
		address = d.lastKnownAddress(ctx, machine, device)
		leased = true
	}
	if address == "" {
		// The interface never carried an address; there is nothing to put back.
		return nil
	}
	gateway, err := d.networkGateway(ctx, network)
	if err != nil {
		return err
	}
	if err := d.configureGuestAddress(ctx, machine, device,
		fmt.Sprintf("%s/%d", address, gateway.Bits())); err != nil {
		return err
	}
	if leased {
		// The default route died with the lease, and nothing renews either.
		// Restored only for the leased case: a pinned private NIC never had
		// one, and inventing it would route a machine the control plane
		// declared isolated.
		if _, err := d.run(ctx, "exec", machine, "--",
			"ip", "route", "add", "default", "via", gateway.Addr().String(), "dev", device); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "file exists") {
			return fmt.Errorf("restore the default route of %s: %w", machine, err)
		}
	}
	// The routes towards the peered subnets died with the interface too.
	return d.installGuestPrivateRoutes(ctx, machine, network, device)
}

// lastKnownAddress is the IPv4 the runtime last saw on an interface, from the
// volatile key Incus maintains per device. Empty when it never carried one.
func (d *Incus) lastKnownAddress(ctx context.Context, machine, device string) string {
	out, err := d.run(ctx, "config", "get", machine, "volatile."+device+".last_state.ip_addresses")
	if err != nil {
		return ""
	}
	for _, field := range strings.Split(strings.TrimSpace(string(out)), ",") {
		if addr, err := netip.ParseAddr(strings.TrimSpace(field)); err == nil && addr.Is4() {
			return addr.String()
		}
	}
	return ""
}

// networkGateway reads a network's gateway address, with its mask, from the
// runtime ("10.181.0.1/24").
func (d *Incus) networkGateway(ctx context.Context, network string) (netip.Prefix, error) {
	out, err := d.run(ctx, "network", "get", network, "ipv4.address")
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("read the block of %s: %w", network, err)
	}
	prefix, err := netip.ParsePrefix(strings.TrimSpace(string(out)))
	if err != nil {
		return netip.Prefix{}, fmt.Errorf("parse the block of %s: %w", network, err)
	}
	return prefix, nil
}

// privateAggregates is where a guest's traffic towards other emulated subnets
// is sent: to the OVN router, which holds the peerings. The three RFC 1918
// blocks rather than the peered subnets themselves, because peers appear and
// disappear with every network create and delete, and enumerating them would
// turn each change into an exec into every machine of the network. The
// aggregate is safe: the NIC's own connected route is more specific and still
// wins, and a destination the router has no peering for dies at the router,
// which is exactly where the isolation verdict belongs.
var privateAggregates = []string{"10.0.0.0/8", "172.16.0.0/12", "192.168.0.0/16"}

// installGuestPrivateRoutes points the guest's private traffic at the OVN
// router. "file exists" is a previous call's work still standing.
func (d *Incus) installGuestPrivateRoutes(ctx context.Context, machine, network, device string) error {
	gateway, err := d.networkGateway(ctx, network)
	if err != nil {
		return err
	}
	for _, block := range privateAggregates {
		if _, err := d.run(ctx, "exec", machine, "--",
			"ip", "route", "add", block, "via", gateway.Addr().String(), "dev", device); err != nil &&
			!strings.Contains(strings.ToLower(err.Error()), "file exists") {
			return fmt.Errorf("route %s of %s via %s: %w", block, machine, gateway.Addr(), err)
		}
	}
	return nil
}

// appendRoute and removeRoute edit a comma-separated route list, preserving
// the entries they are not about.
func appendRoute(routes, route string) string {
	kept := removeRoute(routes, route)
	if kept == "" {
		return route
	}
	return kept + "," + route
}

func removeRoute(routes, route string) string {
	kept := make([]string, 0, 4)
	for _, existing := range strings.Split(routes, ",") {
		existing = strings.TrimSpace(existing)
		if existing != "" && existing != route {
			kept = append(kept, existing)
		}
	}
	return strings.Join(kept, ",")
}

// setUplinkRoute adds or removes one /32 from the uplink's ipv4.routes, which
// is what makes the host route the address towards OVN. The key is shared by
// every routed address, hence the lock: two attachments editing it without one
// lose each other's routes.
func (d *Incus) setUplinkRoute(ctx context.Context, address string, add bool) error {
	d.uplinkMu.Lock()
	defer d.uplinkMu.Unlock()

	name := d.uplinkName()
	out, err := d.run(ctx, "network", "get", name, "ipv4.routes")
	if err != nil {
		// No uplink means nothing routes the address anyway; for a removal
		// that is the outcome asked for.
		if !add && isNotFound(err) {
			return nil
		}
		return fmt.Errorf("read routes of uplink %s: %w", name, err)
	}

	route := address + "/32"
	kept := make([]string, 0, 4)
	for _, existing := range strings.Split(strings.TrimSpace(string(out)), ",") {
		existing = strings.TrimSpace(existing)
		if existing != "" && existing != route {
			kept = append(kept, existing)
		}
	}
	if add {
		kept = append(kept, route)
	}
	if _, err := d.run(ctx, "network", "set", name, "ipv4.routes="+strings.Join(kept, ",")); err != nil {
		return fmt.Errorf("set routes of uplink %s: %w", name, err)
	}
	return nil
}
