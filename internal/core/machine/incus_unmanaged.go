package machine

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// A routed interface whose address has moved (#548) is one the guest's own
// network stack must stop writing (#742).
//
// The guest's netplan, rendered once by cloud-init at the first boot
// (routedNetworkConfig), declares eth0 with the launch address and the default
// route through the link-local next hop, and systemd-networkd re-applies it
// every time the link appears: at a boot, and at the re-plug an ipv4.address
// edit causes. Once the address has moved onto the filtered NIC, that config
// is stale, and every order the driver takes against it is a race: measured
// on 2026-09-07 at 100 ms across a hot attach, the re-plugged link answers
// `Network File: n/a, State: off (unmanaged)` for 150 ms before networkd
// matches it and lays the launch address, and the driver's `ip address del`
// wins only when it lands after that write. On the author's station it did;
// on the runner it did not (run 34124737760), and the reboot comparison of
// #671 reported the difference. Whether the runner's shape came from that
// window or from a second, reload-triggered write cannot be told from its log,
// and it does not matter: while networkd owns eth0's configuration, the
// driver deleting what networkd lays wins sometimes, and "sometimes" is what
// the comparison measures. #696 wrote the lesson for the resolver — a value
// networkd owns cannot be held outside networkd's configuration — and this is
// the same lesson for the address.
//
// So the driver tells networkd, in networkd's own configuration, to let go:
// a drop-in beside the unit netplan rendered for the interface, which #702
// already writes for the resolver at the same path, carrying
// `[Link] Unmanaged=yes`; a reload; and then a wait for the STATE networkd
// reports for the link, `(unmanaged)`, rather than for any effect. From then
// on nobody but the driver writes eth0, at the re-plug and at every boot after
// it, since netplan regenerates the unit under /run and the drop-in under /etc
// applies to it (#702 measured that across a container restart).
//
// What this changes is who writes the interface, not what is written: the
// shape stays an addressless routed interface carrying the next hop and the
// default route through it (repairRoutedInterface), which is the shape this
// driver produced after a boot before #742, discussable, and decided by
// #695's outbound half and the ADR of #726 rather than here. A guest with no
// networkd (Alpine, an ifupdown Debian) has no unit to name and nothing to
// let go of, and is left alone.

// unmanagedDropIn is the file's name beside the resolver's feint-dns.conf.
const unmanagedDropIn = "feint-unmanaged.conf"

// unmanageGuestInterface takes an interface away from systemd-networkd, and
// reports whether networkd had it. Nothing is asked of a guest that names no
// unit for the interface: no networkd, or a link nobody manages.
func (d *Incus) unmanageGuestInterface(ctx context.Context, machine, device string) (bool, error) {
	iface, err := d.guestInterface(ctx, machine, device)
	if err != nil {
		return false, err
	}
	unit := d.guestNetworkUnit(ctx, machine, iface)
	if unit == "" {
		return false, nil
	}
	dropIn := "/etc/systemd/network/" + unit + ".d/" + unmanagedDropIn
	script := "mkdir -p " + shellQuote(dropIn[:strings.LastIndex(dropIn, "/")]) +
		" && printf '[Link]\\nUnmanaged=yes\\n' > " + shellQuote(dropIn)
	if _, err := d.run(ctx, "exec", machine, "--", "sh", "-c", script); err != nil {
		if isNotRunning(err) || isNotFound(err) {
			return false, nil
		}
		return false, fmt.Errorf("tell the guest's network stack to let go of %s/%s: %w", machine, iface, err)
	}
	if err := d.reloadGuestNetwork(ctx, machine); err != nil {
		return false, fmt.Errorf("the guest was not told to read that %s/%s is nobody's: %w", machine, iface, err)
	}
	if err := d.waitForGuestLinkUnmanaged(ctx, machine, iface); err != nil {
		return false, err
	}
	return true, nil
}

// waitForGuestLinkUnmanaged blocks until networkd reports the link as one it
// does not manage. A state the driver itself provoked, so the wait is short in
// the ordinary case and bounded in every other; an image without networkctl
// answers once and is not waited for.
func (d *Incus) waitForGuestLinkUnmanaged(ctx context.Context, machine, iface string) error {
	deadline := time.Now().Add(d.resolverBudget())
	for {
		state, err := d.readGuestLinkState(ctx, machine, iface)
		if err == nil && strings.Contains(state, "(unmanaged)") {
			return nil
		}
		if err != nil && (isNotRunning(err) || guestHasNoNetworkd(err)) {
			return nil
		}
		if time.Now().After(deadline) {
			if err == nil {
				err = fmt.Errorf("networkd still reports %q", state)
			}
			return fmt.Errorf("wait for the guest's network stack to let go of %s/%s: %w", machine, iface, err)
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(d.resolverInterval()):
		}
	}
}

// readGuestLinkState is the `State:` line of `networkctl status <iface>`,
// which carries networkd's own setup state for the link in parentheses:
// (pending), (configuring), (configured), (unmanaged).
func (d *Incus) readGuestLinkState(ctx context.Context, machine, iface string) (string, error) {
	out, err := d.run(ctx, "exec", machine, "--", "networkctl", "status", iface)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(out), "\n") {
		if _, after, found := strings.Cut(line, "State:"); found {
			return strings.TrimSpace(after), nil
		}
	}
	return "", nil
}

// routedDeviceCarriesNothing: a routed device that pins no address and routes
// none is one whose address moved (#548), or that never had one.
func routedDeviceCarriesNothing(cfg map[string]string) bool {
	return len(splitList(cfg["ipv4.address"])) == 0 && len(splitList(cfg["ipv4.routes"])) == 0
}
