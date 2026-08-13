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
	// OVN NICs take no live route edits, so the address travels as a network
	// forward instead; see routeAddressOVN for the measurements behind it.
	if d.OVN {
		return d.routeAddressOVN(ctx, spec)
	}
	if !safeName.MatchString(spec.Machine) {
		return fmt.Errorf("invalid machine name %q", spec.Machine)
	}
	network, device, err := d.interfaceFor(ctx, spec.Machine, spec.Network)
	if err != nil {
		return err
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
		// Dropping the address inside the guest is tolerant on purpose: a
		// stopped machine holds no live address, an image without `ip` never
		// took it, and "cannot find" means it is already gone. With the route
		// removed above, nothing reaches the guest on it either way.
		_, _ = d.run(ctx, "exec", machine, "--", "ip", "address", "del", route, "dev", device)
	}
	return nil
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

// interfaceFor returns the network to route through and the device on it.
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
	names := make([]string, 0, len(devices.own))
	for candidate, config := range devices.own {
		if config["type"] == "nic" && config["network"] != "" {
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
