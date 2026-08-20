package machine

import (
	"context"
	"strings"
	"testing"
)

// A routed NIC is the interface of every machine that joins no emulated
// network (#202): a Scaleway server with only its public address, every
// Exoscale instance. Until #337 the address paths could not see it — it has no
// `network` key — so the API reported an elastic IP attached while nothing put
// it on the machine, and the poweron replay of a launch address died on
// "machine has no network interface". These tests hold the recognition, the
// mechanism, and its two measured costs: the ownership question has no network
// to be asked of, and a live ipv4.routes edit re-plugs the device.

// routedOnly is the machine the Scaleway and Exoscale ssh suites boot: one
// routed NIC carrying the published address, no network anywhere.
const routedOnly = `{
  "expanded_devices": {
    "eth0": {"type": "nic", "nictype": "routed",
             "ipv4.address": "203.0.113.7", "ipv4.host_address": "169.254.0.1"}
  },
  "devices": {
    "eth0": {"type": "nic", "nictype": "routed",
             "ipv4.address": "203.0.113.7", "ipv4.host_address": "169.254.0.1"}
  }
}`

// TestRouteAddressReachesARoutedNIC holds the whole mechanism: the routed NIC
// is selected although it sits on no network, the address lands in the
// device's own ipv4.routes — the one route key a routed NIC accepts, measured
// on Incus 7.2 — the re-plug that edit causes is repaired, and the guest is
// handed the address.
func TestRouteAddressReachesARoutedNIC(t *testing.T) {
	for name, ovn := range map[string]bool{"bridge mode": false, "ovn mode": true} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRuntime{answers: map[string]string{
				"/1.0/instances/vm": routedOnly,
			}}
			d := newFakeDriver(f)
			d.OVN = ovn

			if err := d.RouteAddress(context.Background(), AddressSpec{
				Machine: "vm", Address: "192.0.2.2",
			}); err != nil {
				t.Fatalf("route: %v", err)
			}

			if got := f.matching("ipv4.routes=192.0.2.2/32"); len(got) != 1 ||
				!strings.Contains(got[0], "config device set vm eth0") {
				t.Fatalf("the address must land in the routed NIC's own ipv4.routes, got:\n%s",
					strings.Join(f.commands(), "\n"))
			}
			// The routed mechanism is the same in both modes: a routed NIC has
			// no OVN port, so nothing external and no uplink route.
			for _, wrong := range []string{"ipv4.routes.external", "network set"} {
				if got := f.matching(wrong); len(got) != 0 {
					t.Errorf("the OVN machinery has nothing to say about a routed NIC, got %v", got)
				}
			}
			// The edit re-plugged the device (measured: the guest interface
			// comes back down and bare), so the repair must run: link up, the
			// launch address back, the default route back through the NIC's
			// own next hop.
			for _, must := range []string{
				"exec vm -- ip link set eth0 up",
				"exec vm -- ip address add 203.0.113.7/32 dev eth0",
				"exec vm -- ip route add default via 169.254.0.1 dev eth0",
				"exec vm -- ip address add 192.0.2.2/32 dev eth0",
			} {
				if len(f.matching(must)) == 0 {
					t.Errorf("missing %q in:\n%s", must, strings.Join(f.commands(), "\n"))
				}
			}
		})
	}
}

// TestRouteAddressLeavesALaunchAddressAlone holds the no-op half, which is
// what the poweron replay depends on: an address already in ipv4.address rode
// the launch — the guest's netplan declares it, the host route exists — and
// editing the device again would re-plug a working interface at every boot.
func TestRouteAddressLeavesALaunchAddressAlone(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/vm": routedOnly,
	}}
	d := newFakeDriver(f)

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "vm", Address: "203.0.113.7",
	}); err != nil {
		t.Fatalf("replaying the launch address must succeed, got %v", err)
	}
	for _, wrong := range []string{"config device set", "ip address add", "ip link"} {
		if got := f.matching(wrong); len(got) != 0 {
			t.Errorf("a launch address must not be touched again, got %v", got)
		}
	}
}

// TestRouteAddressRefusesARoutedMachineTheEmulatorDidNotCreate is the
// ownership half. On a managed network mustOwn vouches for the network; a
// routed NIC has none, so the question moves to the instance — whose name
// arrives from Resource.Runtime, restored verbatim by PUT /_feint/state and
// `feint snapshot load`. Without the check, a crafted snapshot would route an
// emulated address into any container on the host.
func TestRouteAddressRefusesARoutedMachineTheEmulatorDidNotCreate(t *testing.T) {
	const foreign = "production-database"
	f := &fakeRuntime{answers: map[string]string{
		// The label is absent: the operator's instance, not ours.
		"config get " + foreign: "",
		"/1.0/instances/":       routedOnly,
	}}
	d := newFakeDriver(f)

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: foreign, Address: "192.0.2.2",
	}); err == nil {
		t.Fatal("an address was routed into an instance the emulator never created")
	}
	for _, command := range f.commands() {
		if !strings.Contains(command, foreign) {
			continue
		}
		// Inspecting and reading the label are the questions themselves.
		if strings.HasPrefix(command, "config get ") || strings.HasPrefix(command, "query ") {
			continue
		}
		t.Errorf("a command reached the operator's own instance: %s", command)
	}
}

// TestUnrouteAddressRepairsARoutedNIC holds the withdrawal in both driver
// modes. Removing the entry from ipv4.routes re-plugs the device like adding
// one does, so the repair must follow — without it the machine loses the
// address the API still publishes, not only the one being taken back.
func TestUnrouteAddressRepairsARoutedNIC(t *testing.T) {
	const carrying = `{
  "expanded_devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.7",
             "ipv4.host_address": "169.254.0.1", "ipv4.routes": "192.0.2.2/32"}
  },
  "devices": {
    "eth0": {"type": "nic", "nictype": "routed", "ipv4.address": "203.0.113.7",
             "ipv4.host_address": "169.254.0.1", "ipv4.routes": "192.0.2.2/32"}
  }
}`
	for name, ovn := range map[string]bool{"bridge mode": false, "ovn mode": true} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRuntime{answers: map[string]string{
				"/1.0/instances/vm":                     carrying,
				"config device get vm eth0 ipv4.routes": "192.0.2.2/32",
			}}
			d := newFakeDriver(f)
			d.OVN = ovn

			if err := d.UnrouteAddress(context.Background(), "vm", "192.0.2.2"); err != nil {
				t.Fatalf("unroute: %v", err)
			}
			cleared := false
			for _, cmd := range f.matching("config device set vm eth0 ipv4.routes=") {
				if strings.HasSuffix(cmd, "ipv4.routes=") {
					cleared = true
				}
			}
			if !cleared {
				t.Fatalf("the route must be taken off the device, got:\n%s",
					strings.Join(f.commands(), "\n"))
			}
			for _, must := range []string{
				"exec vm -- ip link set eth0 up",
				"exec vm -- ip address add 203.0.113.7/32 dev eth0",
			} {
				if len(f.matching(must)) == 0 {
					t.Errorf("the re-plug must be repaired, missing %q in:\n%s",
						must, strings.Join(f.commands(), "\n"))
				}
			}
		})
	}
}
