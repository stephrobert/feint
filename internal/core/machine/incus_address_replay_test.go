package machine

import (
	"context"
	"strings"
	"testing"
)

// The replay of a machine's promised public addresses is documented as
// idempotent, and #498 is where it was not.
//
// The shape is every Scaleway server's: created before its private NIC exists,
// so the public address rides a routed NIC (#202); once the NIC is there,
// Plan.RouteVia names the private network, and the replay a poweron or a reboot
// runs asks for the same /32 through the OVN NIC. Measured twice in one run on
// 2026-08-27 — once on the reboot, once on the poweron — each time an ERROR on
// a host that ended up perfectly correct:
//
//	set routes of uplink feint-uplink: incus network: Error: Failed to add
//	route {… Dst: 203.0.113.4/32 …}: file exists
//
// The host route was the routed NIC's own, installed with the device.
//
// #498 answered that by leaving the address where it was, and #548 measured
// what that cost: the routed NIC takes no security option, so the published
// address answered on an interface no group covered. The address moves now,
// and the property this file holds moved with it — from "a delivered address
// is left alone" to "a delivered address is moved once, and every replay after
// that writes nothing". The collision is what the move resolves: releasing the
// address from the device withdraws the host route, which is what the uplink
// write needed.

// routedAndPrivate is that machine: a routed NIC carrying the public address,
// and one OVN NIC on a network the emulator owns.
const routedAndPrivate = `{
  "devices": {
    "eth0": {"type": "nic", "nictype": "routed",
             "ipv4.address": "203.0.113.4", "ipv4.host_address": "169.254.0.1"},
    "eth1": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.30.1.10"}
  },
  "expanded_devices": {
    "eth0": {"type": "nic", "nictype": "routed",
             "ipv4.address": "203.0.113.4", "ipv4.host_address": "169.254.0.1"},
    "eth1": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.30.1.10"}
  }
}`

// TestAPublicAddressMovesOntoTheFilteredNIC: the migration, in the order the
// runtime requires — release first, so the host route the routed device owns
// is gone before the uplink is asked to carry the same /32.
func TestAPublicAddressMovesOntoTheFilteredNIC(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":                                routedAndPrivate,
		"network get fnt-368798629f8 user." + LabelKey:      "feint\n",
		"network get fnt-368798629f8 ipv4.address":          "10.30.1.1/24\n",
		"network get " + DefaultUplinkName + " ipv4.routes": "",
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "srv", Address: "203.0.113.4", Network: "fnt-368798629f8",
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	release := step(f, "config device set srv eth0 ipv4.address=")
	uplink := step(f, "network set "+DefaultUplinkName+" ipv4.routes=203.0.113.4/32")
	external := step(f, "config device set srv eth1 ipv4.routes.external=203.0.113.4/32")
	switch {
	case release < 0:
		t.Fatalf("the address was never released from the routed NIC:\n%s", strings.Join(f.commands(), "\n"))
	case uplink < 0:
		t.Fatalf("the uplink was never given the route:\n%s", strings.Join(f.commands(), "\n"))
	case external < 0:
		t.Fatalf("the filtered NIC never got the address:\n%s", strings.Join(f.commands(), "\n"))
	case release > uplink:
		t.Errorf("the uplink was asked for the /32 while the routed NIC still owned the host route, "+
			"which is the `file exists` of #498:\n%s", strings.Join(f.commands(), "\n"))
	case uplink > external:
		t.Errorf("the NIC's external route was set before the uplink carried it, and the runtime "+
			"validates the one against the other:\n%s", strings.Join(f.commands(), "\n"))
	}

	// The device stays. Removing it is the second refused remedy: it unmasks
	// the profile's own eth0 on incusbr0, the operator's default bridge.
	if got := f.matching("config device remove"); len(got) != 0 {
		t.Errorf("the routed device was removed, which unmasks the profile NIC on the host bridge:\n%s",
			strings.Join(got, "\n"))
	}
}

// The same migration in bridge mode, where the address travels as ipv4.routes
// on the managed device instead. The mode was never measured on this shape
// until #548, and it is the poorer population of the two.
func TestAPublicAddressMovesOntoTheFilteredNICInBridgeMode(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":                           routedAndPrivate,
		"network get fnt-368798629f8 user." + LabelKey: "feint\n",
		"config device get srv eth1 ipv4.routes":       "",
	}}
	d := newFakeDriver(f)

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "srv", Address: "203.0.113.4", Network: "fnt-368798629f8",
	}); err != nil {
		t.Fatalf("migrate: %v", err)
	}

	release := step(f, "config device set srv eth0 ipv4.address=")
	routes := step(f, "config device set srv eth1 ipv4.routes=203.0.113.4/32")
	switch {
	case release < 0:
		t.Fatalf("the address was never released from the routed NIC:\n%s", strings.Join(f.commands(), "\n"))
	case routes < 0:
		t.Fatalf("the filtered NIC never got the route:\n%s", strings.Join(f.commands(), "\n"))
	case release > routes:
		t.Errorf("the bridged NIC was given the /32 while the routed NIC still owned the host route:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	if len(f.matching("ip address add 203.0.113.4/32 dev eth1")) == 0 {
		t.Errorf("the guest was never given the address on its filtered interface:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// TestMigratingIsIdempotent is #498's property, restated for a machine that has
// already moved: the routed NIC carries nothing, the filtered one carries
// everything, and a replay writes not one key. Without it the poweron and the
// reboot would each re-plug a live NIC for nothing.
func TestMigratingIsIdempotent(t *testing.T) {
	// The same machine after the migration: eth0 routed and addressless, eth1
	// carrying the external route.
	const migrated = `{
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
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":                                migrated,
		"network get fnt-368798629f8 user." + LabelKey:      "feint\n",
		"network get fnt-368798629f8 ipv4.address":          "10.30.1.1/24\n",
		"network get " + DefaultUplinkName + " ipv4.routes": "203.0.113.4/32\n",
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "srv", Address: "203.0.113.4", Network: "fnt-368798629f8",
	}); err != nil {
		t.Fatalf("replay: %v", err)
	}
	for _, forbidden := range []string{
		"network set " + DefaultUplinkName,
		"config device set srv eth0",
		"config device set srv eth1",
	} {
		if got := f.matching(forbidden); len(got) != 0 {
			t.Errorf("the replay reconfigured a machine that already answers on the address:\n%s",
				strings.Join(got, "\n"))
		}
	}
}

// TestMigrationRefusesAnInstanceTheEmulatorDidNotCreate: the name comes from
// Resource.Runtime, which a restored snapshot controls verbatim, and this path
// reconfigures the instance's devices. safeName says the name is well formed,
// never that the instance is ours; the label the emulator itself wrote says
// that, and nothing is issued against an instance that does not carry it.
func TestMigrationRefusesAnInstanceTheEmulatorDidNotCreate(t *testing.T) {
	for name, ovn := range map[string]bool{"bridge mode": false, "ovn mode": true} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRuntime{answers: map[string]string{
				"/1.0/instances/production-database":           routedAndPrivate,
				"network get fnt-368798629f8 user." + LabelKey: "feint\n",
				// The instance carries no label of ours, which is what the
				// fake's default answer would otherwise supply.
				"config get production-database user." + LabelKey: "\n",
			}}
			d := newFakeDriver(f)
			d.OVN = ovn

			err := d.RouteAddress(context.Background(), AddressSpec{
				Machine: "production-database", Address: "203.0.113.4", Network: "fnt-368798629f8",
			})
			if err == nil {
				t.Fatalf("the migration was accepted on an instance the emulator did not create")
			}
			for _, cmd := range f.commands() {
				if strings.HasPrefix(cmd, "config device set production-database") ||
					strings.HasPrefix(cmd, "exec production-database") {
					t.Errorf("the driver reconfigured the operator's own instance: %q", cmd)
				}
			}
		})
	}
}

// TestAMigrationIsNotStartedForANetworkTheEmulatorDoesNotOwn: the move is a
// real change to one of our own machines, and the network it moves the address
// *to* is named by the pack's plan, which is built from stored values a
// restored snapshot controls. Refusing after the release — which is what the
// route itself does for a foreign network — would leave the machine having
// lost the address it answered on, for a route it never got.
func TestAMigrationIsNotStartedForANetworkTheEmulatorDoesNotOwn(t *testing.T) {
	for name, ovn := range map[string]bool{"bridge mode": false, "ovn mode": true} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRuntime{answers: map[string]string{
				"/1.0/instances/srv": routedAndPrivate,
				// The network carries no label of ours.
				"network get fnt-368798629f8 user." + LabelKey: "\n",
			}}
			d := newFakeDriver(f)
			d.OVN = ovn

			if err := d.RouteAddress(context.Background(), AddressSpec{
				Machine: "srv", Address: "203.0.113.4", Network: "fnt-368798629f8",
			}); err == nil {
				t.Fatal("the migration was accepted through a network the emulator did not create")
			}
			if got := f.matching("config device set srv eth0"); len(got) != 0 {
				t.Errorf("the address was taken off the machine before the refusal:\n%s",
					strings.Join(got, "\n"))
			}
		})
	}
}

// step is the position of the first command containing substr, -1 when none
// does: the migration's verdict is an order, not a set. Its neighbour indexOf
// compares whole commands, which cannot express "the device set, whatever it
// set the key to".
func step(f *fakeRuntime, substr string) int {
	for i, cmd := range f.commands() {
		if strings.Contains(cmd, substr) {
			return i
		}
	}
	return -1
}

// The accepting half, without which the guard above would be a refusal rather
// than an idempotence: an address no routed NIC carries still travels the OVN
// path in full — the uplink first, because Incus validates ipv4.routes.external
// against it, then the device.
func TestAnAddressNoRoutedNICCarriesStillTravelsTheOVNPath(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":                                routedAndPrivate,
		"network get fnt-368798629f8 user." + LabelKey:      "feint\n",
		"network get fnt-368798629f8 ipv4.address":          "10.30.1.1/24\n",
		"network get " + DefaultUplinkName + " ipv4.routes": "",
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "srv", Address: "203.0.113.9", Network: "fnt-368798629f8",
	}); err != nil {
		t.Fatalf("route: %v", err)
	}

	for _, must := range []string{
		"network set " + DefaultUplinkName + " ipv4.routes=203.0.113.9/32",
		"config device set srv eth1 ipv4.routes.external=203.0.113.9/32",
	} {
		if len(f.matching(must)) == 0 {
			t.Errorf("missing %q in:\n%s", must, strings.Join(f.commands(), "\n"))
		}
	}
}

// And a machine with no routed NIC at all — every Outscale Vm, every server
// whose only interface is its private one — is not affected by the question.
func TestAMachineWithNoRoutedNICIsRoutedAsBefore(t *testing.T) {
	const privateOnly = `{
	  "devices": {
	    "eth0": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.30.1.10"}
	  },
	  "expanded_devices": {
	    "eth0": {"type": "nic", "network": "fnt-368798629f8", "ipv4.address": "10.30.1.10"}
	  }
	}`
	f := &fakeRuntime{answers: map[string]string{
		"/1.0/instances/srv":                                privateOnly,
		"network get fnt-368798629f8 user." + LabelKey:      "feint\n",
		"network get fnt-368798629f8 ipv4.address":          "10.30.1.1/24\n",
		"network get " + DefaultUplinkName + " ipv4.routes": "",
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "srv", Address: "203.0.113.4", Network: "fnt-368798629f8",
	}); err != nil {
		t.Fatalf("route: %v", err)
	}
	if len(f.matching("config device set srv eth0 ipv4.routes.external=203.0.113.4/32")) == 0 {
		t.Errorf("the address did not reach the private NIC:\n%s", strings.Join(f.commands(), "\n"))
	}
}
