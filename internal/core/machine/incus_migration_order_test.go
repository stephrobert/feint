package machine

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The two writers of a routed interface, held to one order on the hot path as
// well as after a boot (#742).
//
// The guest's own netplan, rendered once at the first boot (routedNetworkConfig),
// declares eth0 with the launch address and the default route through
// 169.254.0.1, and systemd-networkd re-applies it every time the interface
// appears: at a boot, and at the re-plug an ipv4.address edit causes. The
// driver's migration (#548) edited the device, repaired the interface at once,
// then deleted the moved address, and so raced networkd on a link it had just
// re-plugged. Measured 2026-09-07 on platform-web-0 of the Scaleway example
// stack under `--vm incus-ovn`, the same reboot verb producing two different
// "before" shapes on two runs:
//
//	networkd first, then the driver's del:  eth0 bare, no default route
//	the driver's del first, then networkd:  eth0 203.0.113.4/32 + default via 169.254.0.1
//
// while the restart path, which waits for the guest to lay the interface
// before it writes (#674), always produced eth0 bare + default via 169.254.0.1,
// polled at half-second intervals: netplan at t+0.5 s after the container was
// back, the driver's release at t+2.2 s, the driver's default route at t+2.8 s.
// So the reboot comparison of #671 found a difference on every run, in one
// direction or the other, and the difference was never the reboot's.

// migratedRoutedAndPrivate is routedAndPrivate after the device edit: eth0
// routed and addressless, eth1 carrying the external route (migratedRouted is
// the same shape for another machine's address). The fixture the migration
// reads BEFORE its edit is routedAndPrivate; this is what the runtime answers
// after it, so a repair that re-reads the device sees what the device holds.
const migratedRoutedAndPrivate = `{
  "devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.host_address": "169.254.0.1"},
    "eth1": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.30.1.10",
             "ipv4.routes.external": "203.0.113.4/32"}
  },
  "expanded_devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.host_address": "169.254.0.1"},
    "eth1": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.30.1.10",
             "ipv4.routes.external": "203.0.113.4/32"}
  }
}`

// migrationRuntime is a running machine whose guest lays eth0 from its own
// config only on the third read: the two reads before that are the window
// in which networkd has not written yet, which is exactly where the old order
// deleted an address that was not there and repaired a link networkd was
// about to overwrite.
func migrationRuntime(status string, laysAfter int) *fakeRuntime {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-368798629f8 user." + LabelKey:      "feint\n",
		"network get fnt-368798629f8 ipv4.address":          "10.30.1.1/24\n",
		"network get " + DefaultUplinkName + " ipv4.routes": "",
	}}
	released := false
	reads := 0
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		key := strings.Join(args, " ")
		switch {
		case args[0] == "list":
			return []byte(`[{"name":"srv","status":"` + status + `","state":{"network":{}}}]`), nil, true
		case args[0] == "query" && strings.HasSuffix(key, "/1.0/instances/srv"):
			if released {
				return []byte(migratedRoutedAndPrivate), nil, true
			}
			return []byte(routedAndPrivate), nil, true
		case strings.HasPrefix(key, "config device set srv eth0 ipv4.address="):
			released = true
			return nil, nil, true
		case strings.Contains(key, "addr show dev eth0"):
			reads++
			if reads >= laysAfter {
				return []byte("2: eth0    inet 203.0.113.4/32 scope global eth0\n"), nil, true
			}
			return []byte(""), nil, true
		}
		return nil, nil, false
	}
	return f
}

// TestAHotMigrationWaitsForTheGuestBeforeTakingTheStaleAddressOff: after the
// device edit re-plugs eth0, the driver reads the interface until the guest's
// own config has laid it, takes the moved address off, and only then lays the
// default route — the order reconcileRoutedNICs already holds after a boot.
// Without the wait, the third read never happens and the deletion finds
// nothing; with the repair before the release, the default route the driver
// lays is the one the deletion takes away.
func TestAHotMigrationWaitsForTheGuestBeforeTakingTheStaleAddressOff(t *testing.T) {
	f := migrationRuntime("Running", 3)
	d := newFakeDriver(f)
	d.OVN = true
	d.routePoll = time.Millisecond
	d.routeBudget = time.Second

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "srv", Address: "203.0.113.4", Network: "fnt-368798629f8",
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	release := step(f, "config device set srv eth0 ipv4.address=")
	del := step(f, "ip address del 203.0.113.4/32 dev eth0")
	route := step(f, "ip route add default via 169.254.0.1 dev eth0")
	reads := f.matching("addr show dev eth0")
	switch {
	case release < 0:
		t.Fatalf("the address was never released from the routed NIC:\n%s", strings.Join(f.commands(), "\n"))
	case del < 0:
		t.Fatalf("the moved address was never taken off the guest's routed interface, so the guest "+
			"answers ARP for an address the host no longer routes to it:\n%s", strings.Join(f.commands(), "\n"))
	case len(reads) < 3:
		t.Fatalf("the driver read eth0 %d time(s) and did not wait for the guest's own config to lay it "+
			"(#742): its writes race networkd's on a link it just re-plugged:\n%s",
			len(reads), strings.Join(f.commands(), "\n"))
	case del < release:
		t.Errorf("the address was taken off the guest before the device let go of it:\n%s",
			strings.Join(f.commands(), "\n"))
	case route >= 0 && route < del:
		t.Errorf("the default route was laid before the stale address was taken off, and the deletion "+
			"takes the route with it (#742):\n%s", strings.Join(f.commands(), "\n"))
	case route < 0:
		t.Errorf("the routed interface was left without its default route through %s:\n%s",
			routedNextHop, strings.Join(f.commands(), "\n"))
	}
}

// TestAColdMigrationDoesNotWaitForTheGuest: the ordinary Terraform order edits
// the device of a stopped machine. There is no guest to wait for, nothing to
// take off, and a wait that polled a stopped machine to its budget would hold
// every cold attach for a minute and a half. The device is set, and the next
// boot's netplan plus the restart path (reconcileRoutedNICs) do the rest.
func TestAColdMigrationDoesNotWaitForTheGuest(t *testing.T) {
	f := migrationRuntime("Stopped", 3)
	d := newFakeDriver(f)
	d.OVN = true
	d.routePoll = time.Millisecond
	d.routeBudget = 50 * time.Millisecond

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "srv", Address: "203.0.113.4", Network: "fnt-368798629f8",
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if step(f, "config device set srv eth0 ipv4.address=") < 0 {
		t.Fatalf("the address was never released from the routed NIC:\n%s", strings.Join(f.commands(), "\n"))
	}
	if got := f.matching("addr show dev eth0"); len(got) != 0 {
		t.Errorf("a stopped machine was polled %d time(s) for an interface no guest will lay:\n%s",
			len(got), strings.Join(f.commands(), "\n"))
	}
	// The device set settles the interface it set and the OVN half of the move
	// runs its own guest step, both tolerant of a stopped machine as they
	// always were; what must not run is the routed interface's reconciliation,
	// which is the driver writing addresses and routes on eth0.
	var touched []string
	for _, cmd := range f.matching("exec srv") {
		if !strings.Contains(cmd, "eth0") {
			continue
		}
		for _, write := range []string{"ip address", "ip route", "ip link"} {
			if strings.Contains(cmd, write) {
				touched = append(touched, cmd)
				break
			}
		}
	}
	if len(touched) != 0 {
		t.Errorf("a stopped machine was told to reconfigure a routed interface it does not have up:\n%s",
			strings.Join(touched, "\n"))
	}
}
