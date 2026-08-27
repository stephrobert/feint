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

// TestReRoutingAnAddressARoutedNICAlreadyCarriesTouchesNothing: the address is
// delivered, so the replay emits nothing at all — and in particular never asks
// the uplink to carry a /32 the host already routes.
func TestReRoutingAnAddressARoutedNICAlreadyCarriesTouchesNothing(t *testing.T) {
	for name, ovn := range map[string]bool{"bridge mode": false, "ovn mode": true} {
		t.Run(name, func(t *testing.T) {
			f := &fakeRuntime{answers: map[string]string{
				"/1.0/instances/srv": routedAndPrivate,
			}}
			d := newFakeDriver(f)
			d.OVN = ovn

			if err := d.RouteAddress(context.Background(), AddressSpec{
				Machine: "srv", Address: "203.0.113.4", Network: "fnt-368798629f8",
			}); err != nil {
				t.Fatalf("replaying a delivered address must be a no-op, got: %v", err)
			}

			for _, forbidden := range []string{
				"network set " + DefaultUplinkName,
				"ipv4.routes.external",
				"config device set srv eth1",
			} {
				if got := f.matching(forbidden); len(got) != 0 {
					t.Errorf("the replay reconfigured a machine that already answers on the address:\n%s",
						strings.Join(got, "\n"))
				}
			}
		})
	}
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
