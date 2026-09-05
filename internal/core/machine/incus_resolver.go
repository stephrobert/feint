package machine

import (
	"context"
	"strings"
	"time"
)

// reloadGuestNetwork makes the guest's network stack take charge of the
// interfaces it has, so that what the network announces reaches the guest
// (#684).
//
// WHY A GUEST NEEDS TO BE TOLD. A network announces its resolver over DHCP
// (dns.nameservers, #660) and its routes over option 121
// (announcedPrivateRoutes). Both only reach a guest whose network stack manages
// the interface. Measured 2026-09-04 under `--vm incus-ovn`, Ubuntu jammy,
// three shapes read side by side:
//
//	shape                                    links               resolvectl dns
//	private NIC present at the launch        eth0 configured     1.1.1.1
//	public address, NIC plugged after        both unmanaged      none
//	no public address, NIC plugged after     eth0 unmanaged      none
//
// The issue that reported this said the second shape was the broken one and the
// third worked. The third was broken too, and reading all three side by side is
// what showed the discriminator is not the public address at all.
//
// The units are not missing: cloud-init ran `netplan generate` (0.082 s in the
// guest's own log) and /run/systemd/network holds 10-netplan-eth0.network and
// 10-netplan-attached.network. systemd-networkd is active and enabled. It has
// simply never been told to read them, so it manages nothing that appeared or
// changed after it started, and `networkctl renew` answers "Interface eth1 is
// not managed by systemd-networkd" on an interface carrying a DHCP address.
//
// One `networkctl reload` turned `unmanaged` into `configured` on both shapes
// and brought the announced resolver with it, on the same reading. That is what
// this does.
//
// A RELOAD IS NOT FREE, WHICH IS WHY IT HAS ONE CALLER. It reconfigures every
// link it manages, and that flushes what this driver laid by hand: on the same
// measurement a pinned address came back as a DHCP lease and a hand-written
// default route was gone. So it belongs exactly where the interface has just
// been taken away and there is nothing to lose — after a device set, see
// setDevice — and nowhere else. Two earlier placements, on the attach path and
// on the restart path, were removed for that reason: they ran before the
// migration that undoes their work, and the measurement showed the shape still
// broken.
//
// BEST EFFORT, DELIBERATELY. An image without systemd-networkd — Alpine, which
// this emulator serves — has no networkctl at all, and a machine whose guest
// cannot be told is a machine that still boots, still carries its addresses and
// still answers. So a failure is reported to the caller's discretion rather than
// failing the boot: the caller logs it. What must never happen is the reverse,
// a machine refused because its distribution spells networking differently.
//
// TestAGuestWithoutNetworkctlIsNotARefusal and
// TestAGuestThatRefusesTheReloadIsReported hold the two halves of the guard;
// setDevice below is the only caller, and TestADeviceSetGivesTheGuestBackItsInterface
// is what fails when it stops calling.
func (d *Incus) reloadGuestNetwork(ctx context.Context, machine string) error {
	if _, err := d.run(ctx, "exec", machine, "--", "networkctl", "reload"); err != nil {
		// A stopped guest has no stack to tell, and attaching to one is the
		// ordinary Terraform order — attach, then power on — where the boot
		// path does this instead. Reporting it would send an operator looking
		// for a problem that is not one, which is the lesson configureGuestAddress
		// above it already carries.
		if isNotRunning(err) || isNotFound(err) || guestHasNoNetworkd(err) {
			return nil
		}
		return err
	}
	return nil
}

// waitForGuestLink waits for an interface to EXIST inside the guest, which is
// not the same question as waitForGuestInterface's — that one waits for it to
// carry an address, and here the address is what the reload is meant to bring.
//
// `incus config device add` returns before the guest's kernel has created the
// link, and a reload that runs in that gap reloads a configuration for an
// interface that is not there yet: the link appears afterwards, managed by
// nobody, which is the state this whole file exists to end. Measured
// 2026-09-04 under `--vm incus-ovn`: with the reload fired immediately after
// the device add, a machine holding a public address still came up with eth0
// and eth1 both `unmanaged` and no resolver, while the same code on a machine
// whose NIC became eth0 worked — the difference being how long the guest took
// to show the link.
//
// Short and bounded, because this is a wait for a kernel to publish a link and
// not for a lease: when it expires the reload runs anyway, since a reload with
// nothing to reload costs one command and refusing to boot over it would cost
// the machine.
func (d *Incus) waitForGuestLink(ctx context.Context, machine, device string) {
	iface, err := d.guestInterface(ctx, machine, device)
	if err != nil || iface == "" {
		return
	}
	poll := d.routePoll
	if poll <= 0 {
		poll = guestLinkPoll
	}
	deadline := time.Now().Add(guestLinkWait)
	for {
		_, err := d.run(ctx, "exec", machine, "--", "ip", "-o", "link", "show", "dev", iface)
		if err == nil {
			return
		}
		// A stopped machine has no link to wait for, and attaching to one is the
		// ordinary Terraform order — attach, then power on. Waiting out the
		// budget there would charge every cold attach ten seconds for nothing,
		// which is how this wait first showed up: a package of unit tests went
		// from four seconds to four minutes.
		if isNotRunning(err) || isNotFound(err) {
			return
		}
		if time.Now().After(deadline) {
			return
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(poll):
		}
	}
}

// guestLinkPoll and guestLinkWait bound that wait. Seconds, not the ninety of
// guestRouteWait: a link either appears within a few seconds of the device add
// or something else is wrong, and this wait sits on the hot attach path that a
// client is blocked on.
const (
	guestLinkPoll = 500 * time.Millisecond
	guestLinkWait = 10 * time.Second
)

// guestHasNoNetworkd tells an image that does not run systemd-networkd from a
// guest that does and refused.
//
// The strings are the shell's and systemd's, not Incus's, because the command
// runs inside the guest: a missing binary is the shell's "not found" (127), and
// a systemd that is not running answers about its bus. Anything else is a real
// refusal and travels back to the caller.
func guestHasNoNetworkd(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, known := range []string{
		"not found",                 // busybox and dash: networkctl: not found
		"no such file or directory", // exec of an absent path
		"failed to connect to bus",  // systemd is not the init of this guest
		"unit systemd-networkd",     // the unit is not there to reload
		"could not be found",        // systemctl's wording for the same
	} {
		if strings.Contains(msg, known) {
			return true
		}
	}
	return false
}

// setDevice changes a device's configuration and gives the guest back the
// interface that change takes away (#684).
//
// WHAT A DEVICE SET DOES TO A RUNNING MACHINE. It re-plugs the NIC. Read from
// the guest's own journal, 2026-09-04 under `--vm incus-ovn`, on a server whose
// private NIC arrived hot and took the published address with it (#548):
//
//	16:40:29 veth3bbc3686: Interface name change detected, renamed to eth1.
//	16:40:29 eth1: Re-configuring with /run/systemd/network/10-netplan-attached.network
//	16:40:29 eth1: DHCPv4 address 10.190.0.3/24
//	16:40:30 eth0: Link DOWN … vethf64c989f: renamed to eth0.
//	16:40:32 eth1: Link DOWN … DHCP lease lost
//	16:40:43 vethcdbe2acf: Interface name change detected, renamed to eth1.
//
// The guest's stack did its work at 16:40:29 and was correct. Then the address
// migration set both devices, each set replaced the veth, and after the last
// one no line says networkd configured anything: `networkctl status eth1`
// answers `Network File: n/a` and `State: routable (unmanaged)` on an interface
// whose matching unit is on disk. So the resolver the network announces never
// arrives, on the one shape a platform team deploys most often.
//
// This is why the fix is here and not in Attach. Attach was the first guess and
// it is wrong: a reload placed there runs BEFORE the migration that undoes it,
// and two rounds of measurement showed the shape still broken. What needs the
// reload is not an attachment, it is every device set — and there are eight of
// them, which is exactly the count that guarantees a per-caller fix gets
// forgotten by the ninth.
//
// TestADeviceSetGivesTheGuestBackItsInterface fails without this.
func (d *Incus) setDevice(ctx context.Context, machine, device string, settings ...string) ([]byte, error) {
	args := append([]string{"config", "device", "set", machine, device}, settings...)
	out, err := d.run(ctx, args...)
	if err != nil {
		return out, err
	}
	d.settleGuestInterface(ctx, machine, device)
	return out, nil
}

// settleGuestInterface waits for an interface to come back and tells the guest
// to read its configuration again.
//
// Two callers, deliberately, because two different commands take an interface
// away and both were measured doing it:
//
//   - a device SET re-plugs the NIC (setDevice above, and the guest journal it
//     quotes);
//   - a device ADD on a running machine gives the guest an interface its stack
//     enumerated before any configuration existed for it. Measured 2026-09-04:
//     with the set covered and the add not, the machine holding a public
//     address resolved and the machine without one did not, which is the same
//     defect wearing the other shape.
//
// The link is re-created, so waiting for it to exist is the ordering, not
// politeness: a reload fired into the gap reloads a configuration for an
// interface that is not there yet. On a stopped machine both calls are no-ops,
// which is the ordinary attach-then-power-on order.
//
// TestADeviceSetGivesTheGuestBackItsInterface and
// TestAHotAttachGivesTheGuestItsNewInterface fail without this, one per caller.
func (d *Incus) settleGuestInterface(ctx context.Context, machine, device string) {
	d.waitForGuestLink(ctx, machine, device)
	if err := d.reloadGuestNetwork(ctx, machine); err != nil {
		d.logger().Warn("the guest was not told to read its network configuration",
			"machine", machine, "device", device, "error", err)
	}
	d.setGuestResolver(ctx, machine, device)
}

// setGuestResolver gives the interface the name server its network stands for,
// without letting the guest lay a route towards it (#684, and the half of #660
// that stayed open).
//
// A DHCP-announced name server is answered by systemd-networkd with an on-link
// /32 towards it (RoutesToDNS=, on by default). For a resolver that does not
// live on the subnet — and a public one never does — that route is dead, and it
// takes the machine's way out with it: measured, `ping 1.1.1.1` unreachable and
// `resolvectl query` answering `No route to host` on a machine whose default
// route was laid and whose next hop answered. Deleting that single route
// restored both.
//
// resolvectl sets a name server and lays nothing, which is the whole reason it
// is used here rather than the lease. Measured on the same machines: with the
// /32 gone and the resolver set this way, `getent hosts deb.debian.org` answers.
//
// After the reload, deliberately: a reload reconfigures the link and drops the
// runtime DNS setting with it.
//
// Best effort, like the reload above and for the same reason: an image without
// systemd-resolved has no resolvectl, and a machine that cannot be told still
// boots and still answers.
//
// TestASettleGivesTheInterfaceItsResolver fails without this.
func (d *Incus) setGuestResolver(ctx context.Context, machine, device string) {
	if !d.OVN {
		// A managed bridge runs its own resolver on the gateway and announces
		// it in the lease, which the guest reaches on-link because it IS
		// on-link. Nothing to add, and adding it would override a working one.
		return
	}
	iface, err := d.guestInterface(ctx, machine, device)
	if err != nil || iface == "" {
		return
	}
	// Bounded retry, and the distinction it rests on is measured. Right after a
	// start, systemd inside the guest is still coming up and resolvectl answers
	// `Failed to connect to bus` — which reads exactly like an image that has no
	// systemd-resolved at all, and was swallowed as such: on 2026-09-04, a
	// rebooted machine came back with its default route and no name server, and
	// nothing in the log said why, because the refusal was the tolerated one.
	//
	// So a bus that is not there yet is retried and an absent binary is not:
	// Alpine answers `not found` once and for good, and waiting on it would
	// charge every Alpine boot the whole budget.
	// TestAResolverWaitsForTheGuestsBusButNotForAMissingBinary fails without
	// this.
	deadline := time.Now().Add(guestResolverWait)
	for {
		_, err := d.run(ctx, "exec", machine, "--", "resolvectl", "dns", iface, d.resolver())
		if err == nil {
			// A command that succeeded is not a resolver that HOLDS, and on a
			// restart it does not: measured 2026-09-04, a rebooted machine came
			// back with its default route and no name server, no error
			// anywhere, because networkd finished configuring the link after
			// this call and cleared what it had set.
			//
			// A read-back with a retry was written and REMOVED: it costs the
			// whole budget on every path whose confirmation is late, and it
			// took this package's unit suite from 6 s to 297 s while still
			// leaving the shape broken, because runtime state is the wrong
			// place for a value networkd owns. The durable answer is a networkd
			// drop-in inside the guest, and it is followed as its own defect
			// rather than bolted on here.
			return
		}
		// The transient case is recognised FIRST, and that ordering is the
		// test: systemd's bus refusal is spelled `Failed to connect to bus: No
		// such file or directory`, which the permanent matcher below also
		// accepts. Asked in the other order, every retry was skipped and the
		// wait never happened.
		if guestBusNotReady(err) {
			if time.Now().After(deadline) {
				d.logger().Warn("the guest's resolver service never answered, so it has no name server",
					"machine", machine, "device", device, "resolver", d.resolver(), "error", err)
				return
			}
			select {
			case <-ctx.Done():
				return
			case <-time.After(guestResolverPoll):
			}
			continue
		}
		if isNotRunning(err) || isNotFound(err) || guestHasNoResolvectl(err) {
			return
		}
		{
			d.logger().Warn("the guest was not given the resolver its network stands for",
				"machine", machine, "device", device, "resolver", d.resolver(), "error", err)
			return
		}
	}
}

// guestResolverPoll and guestResolverWait bound that retry. Seconds: this sits
// on a boot a client is waiting on, and a resolved that has not answered in ten
// seconds is not going to.
const (
	guestResolverPoll = 500 * time.Millisecond
	guestResolverWait = 10 * time.Second
)

// guestBusNotReady is the transient half: systemd is there and not listening
// yet.
func guestBusNotReady(err error) bool {
	return strings.Contains(strings.ToLower(err.Error()), "failed to connect to bus")
}

// guestHasNoResolvectl is the permanent half: the binary is absent, which is
// Alpine and every image without systemd-resolved.
func guestHasNoResolvectl(err error) bool {
	msg := strings.ToLower(err.Error())
	for _, known := range []string{"not found", "no such file or directory", "could not be found"} {
		if strings.Contains(msg, known) {
			return true
		}
	}
	return false
}
