package machine

import (
	"context"
	"strings"
	"testing"
	"time"
)

// The routed NIC has two writers, in a fixed order (#674): the guest's own
// boot-time config lays it at every boot with the addresses the launch pinned,
// and the restart path reconciles it to the device once the guest has done so.
//
// Both halves are measured. The guest must lay it: a first attempt at #674
// took eth0 out of the guest's config and had the driver lay it, and on
// 2026-09-04 under `--vm incus-ovn` (Ubuntu jammy, the ssh suite) that left
// systemd-networkd managing nothing, systemd-networkd-wait-online `activating`
// to its 120 s ceiling, and sshd started at 181 s of uptime, 90 s after the
// suite gave up; main's driver logged in within twenty seconds. And the
// driver must reconcile: the same day, a server whose private NIC had taken
// its address (#548) still declared `203.0.113.3/32` on eth0 in its netplan
// while the device pinned nothing, and an API reboot put the address back on
// eth0 beside the one on eth1.

// launchRouted is a machine created with one public address and no network:
// the shape #202 gives it, as the launch leaves the device.
const launchRouted = `{
  "devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.7", "ipv4.host_address": "169.254.0.1"}
  },
  "expanded_devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.7", "ipv4.host_address": "169.254.0.1"}
  }
}`

// migratedRouted is the same machine after #548 moved its address onto a
// private NIC that arrived hot: the routed device pins nothing, and the
// address is routed onto eth1.
const migratedRouted = `{
  "devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.host_address": "169.254.0.1"},
    "eth1": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.30.1.10", "ipv4.routes.external": "203.0.113.7/32"}
  },
  "expanded_devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.host_address": "169.254.0.1"},
    "eth1": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.30.1.10", "ipv4.routes.external": "203.0.113.7/32"}
  }
}`

// TestTheRoutedNetplanDeclaresTheLaunchAddress: the guest's boot-time config
// names eth0, its launch address as a /32 and the on-link default through the
// link-local next hop, and the stanza for interfaces attached later. The
// interface is managed by the guest, or its boot waits on nothing.
func TestTheRoutedNetplanDeclaresTheLaunchAddress(t *testing.T) {
	f := &fakeRuntime{}
	startScript(f)
	d := newFakeDriver(f)

	if _, err := d.Start(context.Background(), Spec{
		Name: "srv", Image: "alpine:3.21", PublicAddresses: []string{"203.0.113.7"},
	}); err != nil {
		t.Fatalf("start: %v", err)
	}
	inits := f.matching("init ")
	if len(inits) != 1 {
		t.Fatalf("expected one init, got:\n%s", strings.Join(f.commands(), "\n"))
	}
	_, cfg, found := strings.Cut(inits[0], "cloud-init.network-config=")
	if !found {
		t.Fatalf("no network-config was handed to the guest: %s", inits[0])
	}
	for _, want := range []string{"    eth0:", "        - 203.0.113.7/32", "via: 169.254.0.1", "on-link: true", `name: "eth[1-9]"`} {
		if !strings.Contains(cfg, want) {
			t.Errorf("the guest's boot-time config lacks %q, so its network stack manages nothing and its boot waits:\n%s", want, cfg)
		}
	}
}

// TestAFirstBootLeavesTheRoutedNICToTheGuestsOwnConfig: the first door is the
// guest's. No address is written to eth0 by the driver at the first boot, and
// nothing is taken off it.
func TestAFirstBootLeavesTheRoutedNICToTheGuestsOwnConfig(t *testing.T) {
	f := &fakeRuntime{}
	firstBootScript(f, launchRouted)
	d := ovnDriver(f)

	if _, err := d.Start(context.Background(), Spec{
		Name: "srv", Image: "alpine:3.21", PublicAddresses: []string{"203.0.113.7"},
	}); err != nil {
		t.Fatalf("start a new machine: %v", err)
	}
	for _, forbidden := range []string{"ip address add", "ip address del", "ip route add default"} {
		if got := f.matching(forbidden); len(got) != 0 {
			t.Errorf("the first boot wrote the routed NIC the guest's own config lays (%q):\n%s", forbidden, strings.Join(got, "\n"))
		}
	}
}

// TestARestartReconcilesTheRoutedNICToItsDevice is the measurement of
// 2026-09-04 as a test: the device pins nothing on eth0 (the address migrated
// to eth1), the guest's own config laid 203.0.113.7 on eth0 anyway, and the
// restart takes it off — and keeps the door, the default route through the
// link-local next hop, which is what a public address answers through (#660).
func TestARestartReconcilesTheRoutedNICToItsDevice(t *testing.T) {
	f := &fakeRuntime{}
	restartedInstanceCarrying(f, migratedRouted, "203.0.113.7")
	d := ovnDriver(f)

	if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "ubuntu:22.04"}); err != nil {
		t.Fatalf("start an existing machine: %v", err)
	}
	if len(f.matching("exec srv -- ip address del 203.0.113.7/32 dev eth0")) == 0 {
		t.Errorf("the restart left the migrated address on the routed NIC, where the guest's config put it back:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	if got := f.matching("ip address add 203.0.113.7/32 dev eth0"); len(got) != 0 {
		t.Errorf("the restart put the migrated address back on the routed NIC:\n%s", strings.Join(got, "\n"))
	}
	if len(f.matching("exec srv -- ip route add default via 169.254.0.1 dev eth0")) == 0 {
		t.Errorf("the restart took the door away with the address:\n%s", strings.Join(f.commands(), "\n"))
	}
}

// TestARestartKeepsTheAddressTheRoutedNICStillPins is the accepting half: the
// device still pins 203.0.113.4, the guest laid it, nothing is taken off.
func TestARestartKeepsTheAddressTheRoutedNICStillPins(t *testing.T) {
	f := &fakeRuntime{}
	restartedInstance(f, routedPlusPrivate)
	d := ovnDriver(f)

	if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "ubuntu:22.04"}); err != nil {
		t.Fatalf("start an existing machine: %v", err)
	}
	if got := f.matching("ip address del"); len(got) != 0 {
		t.Errorf("the restart took off an address the device still pins:\n%s", strings.Join(got, "\n"))
	}
}

// TestARestartWaitsForTheGuestToLayItsRoutedNICBeforeReconciling: the order is
// the mechanism. The guest's config lays eth0 a moment after `incus start`
// returns; a reconciliation that read the interface before that would find it
// bare, take nothing off, and the stale address would land afterwards. The
// guest here answers bare twice and then carries the migrated address; the
// removal must come after that.
func TestARestartWaitsForTheGuestToLayItsRoutedNICBeforeReconciling(t *testing.T) {
	f := &fakeRuntime{}
	restartedInstanceCarrying(f, migratedRouted, "203.0.113.7")
	inner := f.hook
	reads := 0
	f.hook = func(n int, args []string) ([]byte, error, bool) {
		if args[0] == "exec" && strings.Contains(strings.Join(args, " "), "-o addr show dev eth0") {
			reads++
			if reads <= 2 {
				return []byte(""), nil, true
			}
		}
		return inner(n, args)
	}
	d := ovnDriver(f)
	d.routePoll = time.Millisecond

	if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "ubuntu:22.04"}); err != nil {
		t.Fatalf("start an existing machine: %v", err)
	}
	if len(f.matching("exec srv -- ip address del 203.0.113.7/32 dev eth0")) == 0 {
		t.Errorf("the reconciliation read the routed NIC before the guest had laid it, and took nothing off (%d reads):\n%s",
			reads, strings.Join(f.commands(), "\n"))
	}
}

// TestARestartStillReconcilesWhenTheGuestNeverLaysItsRoutedNIC: the wait is
// bounded, and when it expires the device is still put on the interface and
// the wait is reported — a routed NIC with nothing on it is what an operator
// will ask about, and a start that hangs is not an answer.
func TestARestartStillReconcilesWhenTheGuestNeverLaysItsRoutedNIC(t *testing.T) {
	f := &fakeRuntime{}
	restartedInstanceCarrying(f, launchRouted, "")
	d := ovnDriver(f)
	d.routePoll = time.Millisecond
	d.routeBudget = 5 * time.Millisecond

	err := d.restoreGuestNetwork(context.Background(), "srv")
	if err == nil {
		t.Fatal("a guest that never laid its routed NIC was not reported")
	}
	for _, want := range []string{"srv", "eth0"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the report does not name %q: %v", want, err)
		}
	}
	if len(f.matching("exec srv -- ip address add 203.0.113.7/32 dev eth0")) == 0 {
		t.Errorf("the expired wait left the routed NIC bare instead of laying the device on it:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	if got := f.matching("ip address del"); len(got) != 0 {
		t.Errorf("nothing was carried and something was taken off:\n%s", strings.Join(got, "\n"))
	}
}

// TestARestartLeavesAForeignRoutedNICAlone: ownership at the device. The
// driver names its routed NIC routedDeviceName; one an operator added by hand
// under another name is theirs, and no command names it.
func TestARestartLeavesAForeignRoutedNICAlone(t *testing.T) {
	const foreign = `{
	  "devices": {
	    "wan0": {"type": "nic", "nictype": "routed", "ipv4.address": "198.51.100.9", "ipv4.host_address": "169.254.0.1"}
	  },
	  "expanded_devices": {
	    "wan0": {"type": "nic", "nictype": "routed", "ipv4.address": "198.51.100.9", "ipv4.host_address": "169.254.0.1"}
	  }
	}`
	f := &fakeRuntime{}
	restartedInstance(f, foreign)
	d := ovnDriver(f)

	if _, err := d.Start(context.Background(), Spec{Name: "srv", Image: "ubuntu:22.04"}); err != nil {
		t.Fatalf("start: %v", err)
	}
	if got := f.matching("wan0"); len(got) != 0 {
		t.Errorf("the restart reconfigured a routed NIC the driver did not add:\n%s", strings.Join(got, "\n"))
	}
}
