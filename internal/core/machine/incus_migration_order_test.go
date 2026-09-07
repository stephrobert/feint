package machine

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

// A routed interface whose address moved is taken away from the guest's own
// network stack before the driver writes it (#742); incus_unmanaged.go carries
// the measurement. The tests here hold the hot door, releaseFromRoutedNIC;
// incus_routed_boot_test.go holds the boot door.

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

// What `networkctl status eth0` answers on the images measured, at the three
// moments that matter: managed by the unit netplan rendered, still busy after
// the reload that carried the unmanaged drop-in, and let go of.
const (
	managedByNetworkd   = "● 2: eth0\n             Network File: /run/systemd/network/10-netplan-eth0.network\n                   State: routable (configured)\n"
	stillConfiguring    = "● 2: eth0\n             Network File: /run/systemd/network/10-netplan-eth0.network\n                   State: routable (configuring)\n"
	letGoByNetworkd     = "● 2: eth0\n             Network File: n/a\n                   State: off (unmanaged)\n"
	noNetworkctlAtAll   = "incus exec: Error: networkctl: not found"
	statusReadsToSettle = 3
)

// migrationRuntime is a running machine on the hot door. Before the device
// edit the guest names the unit that manages eth0; after `networkctl reload`
// it answers (configuring) for statusReadsToSettle-1 reads and (unmanaged)
// from then on, which is networkd finishing what the driver asked of it. The
// re-plugged eth0 answers with the launch address on it, the write networkd
// would have made before it was told to let go, so the release has something
// to take off. unit is what the guest answers before the reload; empty means
// an image with no networkctl at all.
func migrationRuntime(status, unit string) *fakeRuntime {
	f := &fakeRuntime{answers: map[string]string{
		"network get fnt-368798629f8 user." + LabelKey:      "feint\n",
		"network get fnt-368798629f8 ipv4.address":          "10.30.1.1/24\n",
		"network get " + DefaultUplinkName + " ipv4.routes": "",
	}}
	released := false
	reloaded := false
	statusReads := 0
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		key := strings.Join(args, " ")
		switch {
		case args[0] == "list":
			return []byte(`[{"name":"srv","status":"` + status + `","state":{"network":{}}}]`), nil, true
		case strings.Contains(key, "networkctl reload"):
			reloaded = true
			return nil, nil, true
		case strings.Contains(key, "networkctl status eth0"):
			if unit == "" {
				return nil, errors.New(noNetworkctlAtAll), true
			}
			if !reloaded {
				return []byte(unit), nil, true
			}
			statusReads++
			if statusReads < statusReadsToSettle {
				return []byte(stillConfiguring), nil, true
			}
			return []byte(letGoByNetworkd), nil, true
		case args[0] == "query" && strings.HasSuffix(key, "/1.0/instances/srv"):
			if released {
				return []byte(migratedRoutedAndPrivate), nil, true
			}
			return []byte(routedAndPrivate), nil, true
		case strings.HasPrefix(key, "config device set srv eth0 ipv4.address="):
			released = true
			return nil, nil, true
		case strings.Contains(key, "addr show dev eth0"):
			return []byte("2: eth0    inet 203.0.113.4/32 scope global eth0\n"), nil, true
		}
		return nil, nil, false
	}
	return f
}

// statusReadsBetween counts the `networkctl status eth0` asks the driver made
// between two positions of the command log.
func statusReadsBetween(f *fakeRuntime, from, to int) int {
	n := 0
	for i, cmd := range f.commands() {
		if i > from && (to < 0 || i < to) && strings.Contains(cmd, "networkctl status eth0") {
			n++
		}
	}
	return n
}

// TestAHotMigrationTellsNetworkdToLetGoBeforeTheRePlug: the drop-in is
// written and networkd reloaded BEFORE the device edit that re-plugs eth0, so
// the new link is nobody's from its first moment; the driver then reads
// networkd's state until it says so, and only after that takes the stale
// address off and lays the default route. Written the other way — asked after
// the re-plug — the same guest answers `n/a` for the 150 ms before networkd
// matches the new link, which reads like "nobody manages this", and the driver
// wrote first in exactly that window on the runner (#742).
func TestAHotMigrationTellsNetworkdToLetGoBeforeTheRePlug(t *testing.T) {
	f := migrationRuntime("Running", managedByNetworkd)
	d := newFakeDriver(f)
	d.OVN = true
	d.resolverPoll = time.Millisecond
	d.resolverWait = time.Second

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "srv", Address: "203.0.113.4", Network: "fnt-368798629f8",
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	dropIn := step(f, unmanagedDropIn)
	set := step(f, "config device set srv eth0 ipv4.address=")
	del := step(f, "ip address del 203.0.113.4/32 dev eth0")
	route := step(f, "ip route add default via 169.254.0.1 dev eth0")
	reload := -1
	for i, cmd := range f.commands() {
		if i > dropIn && strings.Contains(cmd, "networkctl reload") {
			reload = i
			break
		}
	}
	switch {
	case set < 0:
		t.Fatalf("the address was never released from the routed NIC:\n%s", strings.Join(f.commands(), "\n"))
	case dropIn < 0:
		t.Fatalf("networkd was never told to let go of eth0, so it lays the launch config on the re-plugged link "+
			"whenever it wins the race (#742):\n%s", strings.Join(f.commands(), "\n"))
	case !strings.Contains(f.commands()[dropIn], "Unmanaged=yes"):
		t.Fatalf("the drop-in does not say Unmanaged=yes: %s", f.commands()[dropIn])
	case dropIn > set:
		t.Errorf("networkd was told to let go only after the re-plug, inside the window where the new link "+
			"answers n/a and networkd is about to write it (#742):\n%s", strings.Join(f.commands(), "\n"))
	case reload < 0 || reload > set:
		t.Errorf("the drop-in was written and networkd was not told to read it before the re-plug:\n%s",
			strings.Join(f.commands(), "\n"))
	case statusReadsBetween(f, reload, set) < statusReadsToSettle:
		t.Errorf("the driver went on before networkd reported the link unmanaged (%d state read(s) after the "+
			"reload): a state it provoked, not waited for:\n%s",
			statusReadsBetween(f, reload, set), strings.Join(f.commands(), "\n"))
	case del < 0:
		t.Errorf("the moved address was never taken off the guest's routed interface:\n%s",
			strings.Join(f.commands(), "\n"))
	case del < set:
		t.Errorf("the address was taken off the guest before the device let go of it:\n%s",
			strings.Join(f.commands(), "\n"))
	case route < 0:
		t.Errorf("the routed interface was left without its default route through %s:\n%s",
			routedNextHop, strings.Join(f.commands(), "\n"))
	case route < del:
		t.Errorf("the default route was laid before the stale address was taken off, and the deletion "+
			"takes the route with it:\n%s", strings.Join(f.commands(), "\n"))
	}
}

// TestAGuestWithoutNetworkdIsLeftAloneByTheMigration: an image with no
// networkd (Alpine, an ifupdown Debian) lays its interfaces at boot and never
// again, so nobody is going to write the re-plugged link, there is no unit to
// write a drop-in beside, and the driver takes off what the re-plug left and
// lays its routes without asking anything else of the guest.
func TestAGuestWithoutNetworkdIsLeftAloneByTheMigration(t *testing.T) {
	f := migrationRuntime("Running", "")
	d := newFakeDriver(f)
	d.OVN = true
	d.resolverPoll = time.Millisecond
	d.resolverWait = 20 * time.Millisecond

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "srv", Address: "203.0.113.4", Network: "fnt-368798629f8",
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if got := f.matching(unmanagedDropIn); len(got) != 0 {
		t.Errorf("a drop-in was written for a guest that has no networkd to read it:\n%s", strings.Join(got, "\n"))
	}
	if step(f, "ip address del 203.0.113.4/32 dev eth0") < 0 {
		t.Errorf("the moved address was never taken off the guest's routed interface:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	if step(f, "ip route add default via 169.254.0.1 dev eth0") < 0 {
		t.Errorf("the routed interface was left without its default route through %s:\n%s",
			routedNextHop, strings.Join(f.commands(), "\n"))
	}
}

// TestAColdMigrationDoesNotWaitForTheGuest: the ordinary Terraform order edits
// the device of a stopped machine. There is no guest to tell, nothing to take
// off, and nothing to wait for. The device is set, and the next boot's restart
// path (reconcileRoutedNICs) does the rest, drop-in included.
func TestAColdMigrationDoesNotWaitForTheGuest(t *testing.T) {
	f := migrationRuntime("Stopped", managedByNetworkd)
	d := newFakeDriver(f)
	d.OVN = true
	d.resolverPoll = time.Millisecond
	d.resolverWait = 20 * time.Millisecond
	d.routePoll = time.Millisecond
	d.routeBudget = 20 * time.Millisecond

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "srv", Address: "203.0.113.4", Network: "fnt-368798629f8",
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}
	if step(f, "config device set srv eth0 ipv4.address=") < 0 {
		t.Fatalf("the address was never released from the routed NIC:\n%s", strings.Join(f.commands(), "\n"))
	}
	if got := f.matching(unmanagedDropIn); len(got) != 0 {
		t.Errorf("a stopped machine was written a drop-in:\n%s", strings.Join(got, "\n"))
	}
	// The device set settles the interface it set and the OVN half of the move
	// runs its own guest step, both tolerant of a stopped machine as they
	// always were; what must not run is the routed interface's reconciliation,
	// which is the driver reading and writing addresses and routes on eth0.
	var touched []string
	for _, cmd := range f.matching("exec srv") {
		if !strings.Contains(cmd, "eth0") {
			continue
		}
		for _, write := range []string{"ip address", "ip route", "ip link", "addr show"} {
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
