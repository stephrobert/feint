package machine

import (
	"context"
	"strings"
	"testing"
)

// A hot route edit on an OVN NIC re-plugs the device, and the guest comes back
// bare: no address, link down, no DHCP client left to renew anything. The
// repair used to stop at pinned addresses and declare a leased one
// unrepairable, which turned `scw instance ip attach` on a running server into
// a machine with no address at all — measured: RUNNING, guest empty, sshd
// unreachable, while the API said addressed.
//
// The runtime records what the interface carried
// (volatile.<dev>.last_state.ip_addresses), the re-plug keeps the NIC's
// hwaddr, and OVN's IPAM ties the address to that MAC: restoring the recorded
// address statically restores the port's own reservation. Measured by hand on
// Incus 7.2 before being coded: address back, default route back, outbound
// alive, and the public /32 answering from the host.
func TestAHotRouteEditRepairsADHCPInterface(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"query /1.0/instances/srv": `{
			"devices": {"eth0": {"type": "nic", "network": "fnt-default"}},
			"expanded_devices": {"eth0": {"type": "nic", "network": "fnt-default"}}
		}`,
		"network get fnt-default user.":                        "feint",
		"network get fnt-default ipv4.address":                 "10.209.84.1/24",
		"network get " + DefaultUplinkName:                     "",
		"config get srv volatile.eth0.last_state.ip_addresses": "10.209.84.2",
	}}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.RouteAddress(context.Background(), AddressSpec{
		Machine: "srv", Address: "203.0.113.9",
	}); err != nil {
		t.Fatalf("route: %v", err)
	}

	wanted := []string{
		// The re-plug happened: the route key was edited on the device.
		"config device set srv eth0 ipv4.routes.external=203.0.113.9/32",
		// The leased address the interface carried comes back, statically.
		"exec srv -- ip address add 10.209.84.2/24 dev eth0",
		// And the lease's default route with it: without one the guest can
		// answer nothing beyond its own subnet, cloud-init included.
		"exec srv -- ip route add default via 10.209.84.1 dev eth0",
	}
	for _, want := range wanted {
		if len(f.matching(want)) == 0 {
			t.Errorf("missing %q in:\n%s", want, strings.Join(f.commands(), "\n"))
		}
	}
}
