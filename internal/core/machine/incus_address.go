package machine

import (
	"context"
	"fmt"
	"slices"
	"strings"
)

// How Incus gives a machine a public address. The contract is in address.go.
//
// Two ways to emulate it were measured. A network forward translates the address
// before it reaches the machine, which works, but the translation happens past
// the point where firewall rules apply: a port closed by a security group still
// answered on the public address. Routing the address to the bridge and giving
// it to the machine keeps the rules in force, and matches what the server sees
// upstream. That is what this does.

// RouteAddress implements Router.
//
// The route is carried by the device rather than by the network. Both accept
// ipv4.routes, and the network key is shared by every machine on the bridge: two
// attachments editing it lose each other's addresses, and nothing ties a leftover
// back to the machine that owned it. On the device the route is the interface's
// own, the runtime removes it with the device, and it teaches the runtime an
// address it would otherwise merely transport.
func (d *Incus) RouteAddress(ctx context.Context, spec AddressSpec) error {
	if !safeName.MatchString(spec.Machine) {
		return fmt.Errorf("invalid machine name %q", spec.Machine)
	}
	network, device, err := d.interfaceFor(ctx, spec.Machine, spec.Network)
	if err != nil {
		return err
	}
	if network == "" {
		// A routed NIC (#202): the interface of a machine that joins no
		// emulated network. There is no network whose ownership could vouch for
		// this call, so the question moves to the object actually reconfigured
		// — the instance — and its label answers it. The mechanism is the NIC's
		// own in both driver modes: a routed NIC has no OVN port, so the OVN
		// forward machinery below has nothing to say about it.
		// TestRouteAddressRefusesARoutedMachineTheEmulatorDidNotCreate fails
		// without the ownership half.
		if err := d.mustOwnInstance(ctx, spec.Machine); err != nil {
			return err
		}
		return d.routeOntoRoutedNIC(ctx, spec.Machine, device, spec.Address)
	}
	// The machine answers on this address through a routed NIC, and the pack
	// names a managed network for it: the address moves onto that interface
	// (#548).
	//
	// Why it must move. A routed NIC accepts no security option at all (#337),
	// so a Scaleway server created *with* its flexible IP — created before its
	// private NIC exists, hence a routed NIC (#202) — kept the published
	// address on an interface no rule set could ever cover, beside a private
	// NIC wearing the group. Measured 2026-08-27 and again 2026-08-28 in both
	// driver modes: eth0 routed and bare carrying 203.0.113.2, eth1 on a
	// managed network with security.acls, and a port the group's drop default
	// never opened answering from the station.
	//
	// Why this is where it happens. Until #548 this branch returned nil, and
	// that was the right answer to a different question: routing the same /32
	// at the OVN uplink while the routed NIC still owned the host route died
	// on `Failed to add route {… Dst: 203.0.113.4/32 …}: file exists` — an
	// ERROR over a perfectly correct host, twice in one run (#498). Releasing
	// the address from the device first is what unblocks that same call, so
	// the collision is resolved rather than avoided, and the replay stays
	// idempotent: once migrated, no routed NIC carries the address and this
	// branch is not taken again.
	//
	// The device stays on the instance, and that is the whole trick: removing
	// it unmasks the profile's own eth0 on incusbr0, the operator's default
	// bridge, which this emulator must never put anything on (the second
	// refused remedy in docs/limits.md).
	//
	// TestAPublicAddressMovesOntoTheFilteredNIC and its bridge-mode twin fail
	// without this, and TestMigratingIsIdempotent holds the second call.
	routed, err := d.routedNICCarrying(ctx, spec.Machine, spec.Address)
	if err != nil {
		return err
	}
	if routed != "" {
		// The network is asked about before the address moves, and that order
		// is the answer to what a failure would cost. The release is a real
		// change to one of our own machines; refusing afterwards — which is
		// what the mode branches below do for a network the emulator did not
		// create — would leave the machine having lost the address it
		// answered on, for a route it never got. A network name reaching here
		// comes from the pack's Plan.RouteVia, which is built from stored
		// values a restored snapshot controls.
		// TestAMigrationIsNotStartedForANetworkTheEmulatorDoesNotOwn fails
		// without this.
		if err := d.mustOwn(ctx, network); err != nil {
			return err
		}
		if err := d.releaseFromRoutedNIC(ctx, spec.Machine, routed, spec.Address); err != nil {
			return err
		}
	}
	// OVN NICs take no live route edits, so the address travels as a network
	// forward instead; see routeAddressOVN for the measurements behind it.
	if d.OVN {
		return d.routeAddressOVN(ctx, spec)
	}
	// Never route through a network the emulator did not create: an operator's
	// own bridge is not ours to reconfigure, and a route left on it would
	// survive every sweep.
	if err := d.mustOwn(ctx, network); err != nil {
		return err
	}

	if err := d.setDeviceRoutes(ctx, spec.Machine, device, spec.Address, true); err != nil {
		return err
	}
	// The machine answers on it. A /32 on purpose: the address is a route to
	// this machine, not a subnet it belongs to.
	if _, err := d.run(ctx, "exec", spec.Machine, "--",
		"ip", "address", "add", spec.Address+"/32", "dev", device); err != nil {
		if !addressAlreadyThere(err) {
			return fmt.Errorf("give %s to %s: %w", spec.Address, spec.Machine, err)
		}
	}
	return nil
}

// addressAlreadyThere reads "the address is already on the interface" out of an
// `ip address add` failure, which the contract promises on a second call.
//
// Two wordings, because two implementations of `ip` are in play and only one of
// them was known. iproute2, on Debian, Ubuntu and the RHEL family, says
// "RTNETLINK answers: File exists". Busybox, which is the `ip` an Alpine image
// ships, says "Error: ipv4: Address already assigned." — measured on
// images:alpine/3.21/cloud rather than recalled.
//
// Matching only the first turned a re-route of an Alpine machine into a hard
// failure, on the idempotent path the contract says must succeed. A third
// runtime would need its own line here, and the failure would at least name the
// address rather than hide.
// TestARepeatedRouteIsNotAFailure fails without this.
func addressAlreadyThere(err error) bool {
	said := strings.ToLower(err.Error())
	return strings.Contains(said, "file exists") ||
		strings.Contains(said, "address already assigned")
}

// routedNICCarrying names the machine's own routed NIC that delivers this
// address, empty when none does.
//
// ipv4.address and nothing else, because that is the key the launch writes and
// the key the host route follows: routedDevice builds the device from the
// addresses the pack promised, and Incus installs one host route per entry. A
// route key would be the wrong question — ipv4.routes on a routed NIC is where
// routeOntoRoutedNIC puts an address added after the launch, and that path is
// already idempotent on its own terms.
//
// Devices the instance owns, never the expanded set: a NIC inherited from a
// profile belongs to the profile, and an address on it is not one this emulator
// promised.
//
// Sorted, so a machine with two routed NICs carrying one address — which
// nothing produces today — names the same one on every read rather than
// whichever the map handed over.
func (d *Incus) routedNICCarrying(ctx context.Context, machine, address string) (string, error) {
	devices, err := d.instanceDevices(ctx, machine)
	if err != nil {
		return "", fmt.Errorf("inspect %s: %w", machine, err)
	}
	names := make([]string, 0, len(devices.own))
	for name := range devices.own {
		names = append(names, name)
	}
	slices.Sort(names)
	for _, name := range names {
		cfg := devices.own[name]
		if cfg["type"] != "nic" || cfg["nictype"] != "routed" {
			continue
		}
		if routeListContains(cfg["ipv4.address"], address) {
			return name, nil
		}
	}
	return "", nil
}

// releaseFromRoutedNIC takes one address off a routed NIC without taking the
// NIC off the instance — step one of the migration RouteAddress performs, and
// the only one of three attempted remedies that Incus 7.2 accepts on a running
// instance (docs/limits.md carries the two refusals).
//
// What it costs and what it restores. Setting ipv4.address on a live routed
// NIC re-plugs the device, exactly as an ipv4.routes edit does, so the guest
// interface comes back down and bare; repairRoutedInterface puts back what the
// device still declares, which is every address but the one being moved. The
// explicit delete afterwards covers the case where the edit did *not* re-plug
// — a stopped machine, a runtime that updates in place — because an address
// left inside the guest on an interface the host no longer routes is a machine
// answering ARP for something nothing delivers.
//
// Ownership before shape, and this call is why: safeName has said the name
// could be a command argument, never that the instance is ours, and this
// reconfigures an instance's devices from a name a restored snapshot controls.
// RouteAddress's routed branch asks the same question for the same reason; the
// managed branch below asks it of the network instead, which says nothing
// about the instance whose device this edits.
// TestMigrationRefusesAnInstanceTheEmulatorDidNotCreate fails without it.
func (d *Incus) releaseFromRoutedNIC(ctx context.Context, machine, device, address string) error {
	if err := d.mustOwnInstance(ctx, machine); err != nil {
		return err
	}
	devices, err := d.instanceDevices(ctx, machine)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", machine, err)
	}
	kept := make([]string, 0, 2)
	for _, entry := range splitList(devices.own[device]["ipv4.address"]) {
		if entry != address {
			kept = append(kept, entry)
		}
	}
	if _, err := d.run(ctx, "config", "device", "set", machine, device,
		"ipv4.address="+strings.Join(kept, ",")); err != nil {
		return fmt.Errorf("release %s from %s/%s: %w", address, machine, device, err)
	}
	if err := d.repairRoutedInterface(ctx, machine, device); err != nil {
		return err
	}
	// Tolerant on purpose: the re-plug usually took it, a stopped machine
	// holds no live address, and "cannot find" is the outcome asked for.
	_, _ = d.run(ctx, "exec", machine, "--", "ip", "address", "del", address+"/32", "dev", device)
	return nil
}

// mustOwn refuses to touch a network the emulator did not create. The label is
// written by EnsureNetwork, so this is exact rather than a guess on the name.
func (d *Incus) mustOwn(ctx context.Context, network string) error {
	out, err := d.run(ctx, "network", "get", network, "user."+LabelKey)
	if err != nil {
		return fmt.Errorf("read the owner of network %s: %w", network, err)
	}
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("network %s was not created by the emulator; refusing to route through it", network)
	}
	return nil
}

// mustOwnInstance refuses to touch an instance the emulator did not create.
//
// The label is written by Start, through Binding's labels, so this is exact
// rather than a guess on the name. It exists because UnrouteAddress takes its
// machine name straight from a pack's stored resource, without passing through
// Binding, so the prefix check that protects Stop and Remove does not cover it.
//
// A missing instance is not an error here: the caller treats "gone" as "nothing
// left to undo", and that decision belongs to it.
func (d *Incus) mustOwnInstance(ctx context.Context, machine string) error {
	out, err := d.run(ctx, "config", "get", machine, "user."+LabelKey)
	if err != nil {
		return err
	}
	if strings.TrimSpace(string(out)) == "" {
		return fmt.Errorf("instance %s was not created by the emulator; refusing to touch it", machine)
	}
	return nil
}

// UnrouteAddress implements Router.
//
// A machine that no longer exists took its device, and its route with it, so
// there is nothing left to undo and that is not an error. Anything else is
// reported: an address that stays routed shadows the next allocation of it.
//
// The route is looked for on every interface rather than on the first one:
// RouteAddress places it on the network the pack names, and the machine may
// carry several. Undoing on "the first interface in name order" removed the
// route only when the guess happened to match, and left it in place otherwise.
func (d *Incus) UnrouteAddress(ctx context.Context, machine, address string) error {
	if machine != "" {
		if !safeName.MatchString(machine) {
			return fmt.Errorf("invalid machine name %q", machine)
		}
		// Checked before either half runs, because both reconfigure the instance
		// and this name comes from a stored resource, which a restored snapshot
		// controls. A missing instance is not refused: there is no instance left
		// to damage, and the OVN half still has an uplink route to withdraw.
		if err := d.mustOwnInstance(ctx, machine); err != nil && !isNotFound(err) {
			return err
		}
	}
	// The OVN half mirrors this one on the NIC's external routes; the uplink
	// route it also holds is removed even when the machine is already gone.
	if d.OVN {
		return d.unrouteAddressOVN(ctx, machine, address)
	}
	if machine == "" {
		return nil
	}
	devices, err := d.instanceDevices(ctx, machine)
	if err != nil {
		if isNotFound(err) {
			return nil
		}
		return fmt.Errorf("inspect %s: %w", machine, err)
	}

	route := address + "/32"
	for device, cfg := range devices.own {
		if cfg["type"] != "nic" || !routeListContains(cfg["ipv4.routes"], route) {
			continue
		}
		// The route is what must go: it is the host-side path to the address,
		// and it is what shadows the next allocation.
		if err := d.setDeviceRoutes(ctx, machine, device, address, false); err != nil {
			return err
		}
		if cfg["nictype"] == "routed" {
			// The edit re-plugged the device (measured on 7.2: any ipv4.routes
			// change on a live routed NIC removes and re-adds it), so the
			// address went with the old interface and there is nothing left to
			// delete — but everything else the interface carried went too.
			// TestUnrouteAddressRepairsARoutedNIC fails without the repair.
			if err := d.repairRoutedInterface(ctx, machine, device); err != nil {
				return err
			}
			continue
		}
		// Dropping the address inside the guest is tolerant on purpose: a
		// stopped machine holds no live address, an image without `ip` never
		// took it, and "cannot find" means it is already gone. With the route
		// removed above, nothing reaches the guest on it either way.
		_, _ = d.run(ctx, "exec", machine, "--", "ip", "address", "del", route, "dev", device)
	}
	return nil
}

// routeOntoRoutedNIC gives one more address to a machine whose interface is a
// routed NIC (#337).
//
// A routed NIC has no `network` key, so until #337 it was invisible to
// interfaceFor and every address routed after the launch died on "machine has
// no network interface": the Exoscale ssh suite's elastic IP was reported
// attached by the API while nothing put it on the machine.
//
// The mechanism is the NIC's own. `ipv4.routes` is accepted on a routed NIC —
// measured on Incus 7.2, cold and live, the same key every security option is
// refused on — and installs the host route next to the ones the launch's
// ipv4.address list created. Two more measured facts shape the branches:
//
//   - an address already in ipv4.address rode the launch: the guest's netplan
//     declares it and the host route exists, so the replay is a no-op. This is
//     what lets poweron replay every published address through here without
//     re-plugging the interface at each boot.
//   - a live ipv4.routes edit re-plugs the device: the veth is torn down and
//     rebuilt, and the guest interface comes back down and bare. The repair is
//     not optional; without it the machine loses every address it had,
//     including the one the API published at create.
//
// TestRouteAddressReachesARoutedNIC and TestRouteAddressLeavesALaunchAddressAlone
// fail without this.
func (d *Incus) routeOntoRoutedNIC(ctx context.Context, machine, device, address string) error {
	devices, err := d.instanceDevices(ctx, machine)
	if err != nil {
		return fmt.Errorf("inspect %s: %w", machine, err)
	}
	cfg := devices.own[device]
	if routeListContains(cfg["ipv4.address"], address) {
		return nil
	}
	route := address + "/32"
	if !routeListContains(cfg["ipv4.routes"], route) {
		if _, err := d.run(ctx, "config", "device", "set", machine, device,
			"ipv4.routes="+appendRoute(cfg["ipv4.routes"], route)); err != nil {
			return fmt.Errorf("route %s to %s/%s: %w", address, machine, device, err)
		}
		if err := d.repairRoutedInterface(ctx, machine, device); err != nil {
			return err
		}
	}
	// The machine answers on it. A /32 on purpose: the address is a route to
	// this machine, not a subnet it belongs to. A stopped machine takes no
	// exec; its device key is set cold, and the poweron replay lands here again
	// with the machine running.
	if _, err := d.run(ctx, "exec", machine, "--",
		"ip", "address", "add", route, "dev", device); err != nil &&
		!addressAlreadyThere(err) && !isNotRunning(err) {
		return fmt.Errorf("give %s to %s: %w", address, machine, err)
	}
	return nil
}

// repairRoutedInterface restores what an ipv4.routes edit cost the guest of a
// routed NIC. Measured on 7.2: the edit removes and re-adds the device, and
// the new interface comes up down and bare — no address, no default route, and
// no DHCP client to notice, since a routed NIC never had one.
//
// Everything restored is read back from the device itself: the launch
// addresses from ipv4.address, the extra ones from ipv4.routes, the next hop
// from ipv4.host_address. The default route is installed in netplan's own
// on-link shape — a device route to the next hop, then the default through it
// — as two plain commands rather than the `onlink` keyword, so a busybox `ip`
// (Alpine) takes them too.
func (d *Incus) repairRoutedInterface(ctx context.Context, machine, device string) error {
	devices, err := d.instanceDevices(ctx, machine)
	if err != nil {
		return fmt.Errorf("inspect %s after re-plug: %w", machine, err)
	}
	cfg := devices.own[device]
	if _, err := d.run(ctx, "exec", machine, "--", "ip", "link", "set", device, "up"); err != nil {
		if isNotRunning(err) {
			// A cold edit re-plugged nothing: the next boot reads its netplan
			// and the poweron replay hands the guest the routed extras.
			return nil
		}
		return fmt.Errorf("bring %s/%s back up: %w", machine, device, err)
	}
	give := func(route string) error {
		if _, err := d.run(ctx, "exec", machine, "--",
			"ip", "address", "add", route, "dev", device); err != nil && !addressAlreadyThere(err) {
			return fmt.Errorf("restore %s on %s/%s: %w", route, machine, device, err)
		}
		return nil
	}
	for _, address := range splitList(cfg["ipv4.address"]) {
		if err := give(address + "/32"); err != nil {
			return err
		}
	}
	for _, route := range splitList(cfg["ipv4.routes"]) {
		if err := give(route); err != nil {
			return err
		}
	}
	next := cfg["ipv4.host_address"]
	if next == "" {
		next = routedNextHop
	}
	for _, hop := range [][]string{
		{"ip", "route", "add", next, "dev", device},
		{"ip", "route", "add", "default", "via", next, "dev", device},
	} {
		if _, err := d.run(ctx, append([]string{"exec", machine, "--"}, hop...)...); err != nil &&
			!addressAlreadyThere(err) {
			return fmt.Errorf("restore the default route of %s: %w", machine, err)
		}
	}
	return nil
}

// splitList reads a comma-separated device value into its entries.
func splitList(value string) []string {
	out := make([]string, 0, 4)
	for _, entry := range strings.Split(value, ",") {
		if entry = strings.TrimSpace(entry); entry != "" {
			out = append(out, entry)
		}
	}
	return out
}

// routeListContains reports whether a comma-separated ipv4.routes value carries
// the route.
func routeListContains(routes, route string) bool {
	for _, existing := range strings.Split(routes, ",") {
		if strings.TrimSpace(existing) == route {
			return true
		}
	}
	return false
}

// setDeviceRoutes adds or removes one address from a device's ipv4.routes,
// leaving the others alone. The key belongs to one interface of one machine, so
// concurrent work on other machines cannot collide here.
func (d *Incus) setDeviceRoutes(ctx context.Context, machine, device, address string, add bool) error {
	out, err := d.run(ctx, "config", "device", "get", machine, device, "ipv4.routes")
	if err != nil {
		return fmt.Errorf("read routes of %s/%s: %w", machine, device, err)
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

	if _, err := d.run(ctx, "config", "device", "set", machine, device,
		"ipv4.routes="+strings.Join(kept, ",")); err != nil {
		return fmt.Errorf("set routes of %s/%s: %w", machine, device, err)
	}
	return nil
}

// interfaceFor returns the network to route through and the device on it. An
// empty network names a routed NIC, which sits on none.
//
// When the caller names a network, that one is used: a public address belongs
// on the network the server lives on, and only the pack knows which that is.
// With no name, the first interface in name order is taken, which is a guess
// and is documented as one.
func (d *Incus) interfaceFor(ctx context.Context, name, wanted string) (network, device string, err error) {
	devices, err := d.instanceDevices(ctx, name)
	if err != nil {
		return "", "", fmt.Errorf("inspect %s: %w", name, err)
	}

	// Sorted for determinism: a machine with several private NICs must not get
	// its public address on a different one between two runs.
	//
	// A routed NIC counts (#337). It has no `network` key — it is an address
	// with a host route, no L2 segment underneath — and selecting on the key
	// alone made the interface of every machine without an emulated network
	// invisible: "machine has no network interface", on a machine whose one
	// interface was carrying the published address.
	// TestRouteAddressReachesARoutedNIC fails without it.
	names := make([]string, 0, len(devices.own))
	for candidate, config := range devices.own {
		if config["type"] == "nic" && (config["network"] != "" || config["nictype"] == "routed") {
			names = append(names, candidate)
		}
	}
	if len(names) == 0 {
		return "", "", fmt.Errorf("machine %s has no network interface", name)
	}
	slices.Sort(names)
	if wanted != "" {
		for _, candidate := range names {
			if devices.own[candidate]["network"] == wanted {
				return wanted, candidate, nil
			}
		}
		return "", "", fmt.Errorf("machine %s has no interface on network %s", name, wanted)
	}
	device = names[0]
	return devices.own[device]["network"], device, nil
}
