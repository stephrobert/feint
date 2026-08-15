package machine

import (
	"context"
	"fmt"
	"net/netip"
	"strings"
)

// A machine carries the addresses its provider's API publishes, and no others
// (#202).
//
// What this replaces. A machine with no emulated network to join used to be put
// on DefaultMachineNetwork, a managed bridge, and it took an address there that
// no API describes. On top of that the public address was routed onto the same
// interface, so a Scaleway server carried two or three addresses where the real
// one carries a single routed public address and `private_ip: none` — measured
// against a real account, twice, empty before and after.
//
// The bridge was doing two jobs and only the first was written down: give the
// routed /32 an interface the emulator owns, and provide NAT'd outbound so
// cloud-init could install an ssh daemon. #203 removed the second job by
// building images that already carry the daemon, which is what makes this
// possible at all.
//
// What replaces it is Incus's `routed` NIC: an address handed to the instance
// with a host route to it and no L2 segment underneath — the same shape Scaleway
// itself reports as `routed_ipv4`. One interface, one address, nothing invented.
//
// TestAMachineWithNoNetworkCarriesOnlyItsPublicAddress fails without this.

// routedNextHop is the link-local gateway a routed NIC reaches the host through.
//
// Link-local and shared by every machine on purpose: it is the address of the
// host end of each machine's own veth, so two machines using the same number are
// not in conflict — they are on different point-to-point links. This is the
// value Incus's own routed-NIC guide uses.
const routedNextHop = "169.254.0.1"

// routedNetworkConfig renders the netplan a machine on a routed NIC needs.
//
// The guest must be told: a routed NIC hands the kernel a static address and
// Incus's generated config says `dhcp` regardless, so without this the interface
// comes up with nothing on it. Measured — the address was declared on the device
// and the guest carried none.
//
// Every address is parsed before it is rendered, and an unparseable one is a
// refusal rather than an escape. This file renders a structured format by
// concatenation, which the cloud-init templates already show is a way to hand
// somebody a key in the document; the answer here is that nothing but a valid IP
// can reach the rendering at all.
func routedNetworkConfig(addresses []string) (string, error) {
	if len(addresses) == 0 {
		return "", fmt.Errorf("a routed interface needs at least one address")
	}
	var b strings.Builder
	b.WriteString("network:\n  version: 2\n  ethernets:\n    eth0:\n      addresses:\n")
	for _, address := range addresses {
		parsed, err := netip.ParseAddr(address)
		if err != nil {
			return "", fmt.Errorf("routed address %q: %w", address, err)
		}
		if !parsed.Is4() {
			return "", fmt.Errorf("routed address %q is not IPv4", address)
		}
		// A /32 on purpose: the address is a route to this machine, not a
		// subnet it belongs to.
		fmt.Fprintf(&b, "        - %s/32\n", parsed.String())
	}
	// 0.0.0.0/0 rather than the `default` keyword, and this is measured: Debian
	// 12's cloud-init rejects `to: default` with "Address default is not a valid
	// ip address", aborts, and never reaches its ssh module — so the host keys a
	// feint image deliberately does not carry are never regenerated and sshd
	// cannot start. The failure reads as "this image has no ssh daemon", which is
	// the opposite of what happened. The same wording fixed Alpine, whose
	// symptom looked unrelated.
	//
	// on-link because the next hop is not inside a /32.
	fmt.Fprintf(&b, "      routes:\n        - to: 0.0.0.0/0\n          via: %s\n          on-link: true\n",
		routedNextHop)
	// Every interface attached afterwards still comes up on its own.
	//
	// Without this the Scaleway path breaks, and the conformance suite caught it
	// on the first run: a server boots with only its public address, a private
	// NIC is attached later — which is the ordinary Scaleway order, `POST
	// /servers/{id}/private_nics` comes after the create — and the guest's
	// config named eth0 alone, so eth1 came up carrying nothing while the API
	// published an address for it.
	//
	// A match rather than a list, because the number of private NICs is the
	// client's business: a server with two of them is ordinary on Scaleway.
	b.WriteString("    attached:\n      match:\n        name: \"eth[1-9]\"\n      dhcp4: true\n")
	return b.String(), nil
}

// routedDevice is the `incus config device add` argument list for a NIC that
// carries addresses and joins no network.
//
// No `parent`, and that is measured rather than an omission: Incus accepts a
// routed NIC without one, the instance starts, and the host route to the address
// is created all the same. Naming a parent would mean this driver deciding which
// of the operator's interfaces to anchor to — host knowledge it has no business
// holding, and one more thing to be wrong about on a machine with two uplinks or
// none.
//
// A routed NIC has no L2 segment, which is exactly why two machines on routed
// NICs cannot reach each other unless something routes between them.
func routedDevice(name string, addresses []string) ([]string, error) {
	if len(addresses) == 0 {
		return nil, fmt.Errorf("a routed interface needs at least one address")
	}
	for _, address := range addresses {
		if _, err := netip.ParseAddr(address); err != nil {
			return nil, fmt.Errorf("routed address %q: %w", address, err)
		}
	}
	return []string{
		"config", "device", "add", name, "eth0", "nic",
		"nictype=routed",
		"ipv4.address=" + strings.Join(addresses, ","),
		"ipv4.host_address=" + routedNextHop,
	}, nil
}

// rootPool answers the storage pool the operator's own default profile uses for
// its root disk, because a machine created with --no-profiles has to name one
// and this driver has no business inventing it.
//
// "default" when the profile cannot be read: it is the name `incus admin init
// --minimal` creates, so it is the right guess on the installation this project
// documents, and a wrong one fails loudly at create rather than silently later.
func (d *Incus) rootPool(ctx context.Context) string {
	out, err := d.run(ctx, "profile", "device", "get", "default", "root", "pool")
	if err != nil {
		return "default"
	}
	if pool := strings.TrimSpace(string(out)); pool != "" {
		return pool
	}
	return "default"
}
