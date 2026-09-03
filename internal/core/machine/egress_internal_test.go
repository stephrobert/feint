package machine

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

// An OVN network announces no gateway, so no machine gets a default route the
// cloud never gave it (#647).
//
// The defect this holds is one a client meets and the control plane never
// shows. Measured 2026-09-03 under --vm incus-ovn: an OVN network hands every
// machine that BOOTS on it a `default via <gateway> proto dhcp`, so a Scaleway
// server with no public address and no Public Gateway reached the Internet here
// — and a server whose gateway attachment had been removed went on reaching it,
// because the next boot's DHCP put back what the driver had taken away.
//
// `none` is the documented way to stop it (Incus network_ovn reference,
// ipv4.dhcp.gateway: "use none to turn off gateway announcement"). The rest of
// DHCP is untouched: the machine still gets its address.
func TestAnOVNNetworkAnnouncesNoGateway(t *testing.T) {
	for _, mode := range []struct {
		name  string
		ovn   bool
		wants bool
	}{
		{name: "ovn", ovn: true, wants: true},
		// Under a bridge the host routes for the machine and the announcement
		// is how it learns: silencing it there would take away a way out the
		// mode does provide.
		{name: "bridge", ovn: false, wants: false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			// The uplink harness of incus_uplink_test.go rather than a second
			// one: an uplink this run already holds, and the network absent so
			// EnsureNetwork creates rather than adopts.
			f := &fakeRuntime{answers: map[string]string{
				"query /1.0/networks/feint-uplink": ourUplinkJSON(
					strconv.Itoa(os.Getpid()), "10.99.0.0/24"),
				"query /1.0/networks?recursion=1": "[]",
			}, fail: map[string]error{
				"query /1.0/networks/fnt-probe": errors.New("Network not found"),
			}}
			d := newFakeDriver(f)
			d.OVN = mode.ovn
			if err := d.EnsureNetwork(context.Background(), NetworkSpec{
				Name: "fnt-probe", CIDR: "10.99.0.0/24", NAT: true,
			}); err != nil {
				t.Fatalf("ensure: %v", err)
			}

			silenced := false
			for _, call := range f.calls {
				line := strings.Join(call, " ")
				if strings.Contains(line, "network create") && strings.Contains(line, "ipv4.dhcp.gateway=none") {
					silenced = true
				}
			}
			if silenced != mode.wants {
				t.Errorf("ipv4.dhcp.gateway=none present=%v, want %v; calls: %v", silenced, mode.wants, f.calls)
			}
		})
	}
}

// An OVN network announces where the private fleet is, so silencing its
// gateway does not silence the VPC with it (#647).
//
// This is the half that the first attempt at #647 got wrong, and the runtime
// leg said so: with ipv4.dhcp.gateway=none alone, two machines of the SAME VPC
// stopped reaching each other ("10.184.0.2 is unreachable within one VPC;
// isolation is separating too much"). The announced default route had been
// carrying the peered subnets too.
//
// Writing the aggregates from outside the guest does not replace the
// announcement, which is why this is asserted on the network rather than on an
// exec: measured 2026-09-03, all three `ip route add` returned nil and all
// three were gone thirty seconds later, flushed by the guest's own DHCP client
// as it configured the interface behind the driver.
func TestAnOVNNetworkAnnouncesItsPrivateRoutes(t *testing.T) {
	for _, mode := range []struct {
		name  string
		ovn   bool
		wants bool
	}{
		{name: "ovn", ovn: true, wants: true},
		// A managed bridge has no router and no peerings: there is nothing of
		// this kind to announce, and the host routes for the machine anyway.
		{name: "bridge", ovn: false, wants: false},
	} {
		t.Run(mode.name, func(t *testing.T) {
			f := &fakeRuntime{answers: map[string]string{
				"query /1.0/networks/feint-uplink": ourUplinkJSON(
					strconv.Itoa(os.Getpid()), "10.99.0.0/24"),
				"query /1.0/networks?recursion=1": "[]",
			}, fail: map[string]error{
				"query /1.0/networks/fnt-probe": errors.New("Network not found"),
			}}
			d := newFakeDriver(f)
			d.OVN = mode.ovn
			if err := d.EnsureNetwork(context.Background(), NetworkSpec{
				Name: "fnt-probe", CIDR: "10.99.0.0/24", NAT: true,
			}); err != nil {
				t.Fatalf("ensure: %v", err)
			}

			// The three aggregates, each via the network's own router, and the
			// router named by its host part alone: the option carries an
			// address, not a CIDR, and 10.99.0.1/24 there is refused.
			want := "ipv4.dhcp.routes=10.0.0.0/8,10.99.0.1,172.16.0.0/12,10.99.0.1,192.168.0.0/16,10.99.0.1"
			announced := false
			for _, call := range f.calls {
				line := strings.Join(call, " ")
				if strings.Contains(line, "network create") && strings.Contains(line, want) {
					announced = true
				}
			}
			if announced != mode.wants {
				t.Errorf("%q present=%v, want %v; calls: %v", want, announced, mode.wants, f.calls)
			}
		})
	}
}

// announcedPrivateRoutes takes a CIDR and must answer addresses: a pair whose
// gateway still carries its prefix is refused by Incus, and the refusal
// arrives at network create time, which is the worst place to learn it.
func TestTheAnnouncedRoutesNameTheirGatewayWithoutItsPrefix(t *testing.T) {
	got := announcedPrivateRoutes("10.42.0.1/24")
	if strings.Contains(got, "/24") {
		t.Errorf("announcedPrivateRoutes kept a prefix on its gateway: %q", got)
	}
	for _, block := range privateAggregates {
		if !strings.Contains(got, block+",10.42.0.1") {
			t.Errorf("announcedPrivateRoutes(%q) misses %s via the router: %q", "10.42.0.1/24", block, got)
		}
	}
}

// The default route is laid for a machine the plan entitles to one, and taken
// away from a machine it does not (#647).
//
// Both halves, because a driver that laid one for everybody would satisfy the
// first and be exactly the defect: the emulator more permissive than the cloud,
// in the direction that teaches a client its fleet can be provisioned when the
// real one cannot.
func TestADefaultRouteIsLaidOnlyForAMachineEntitledToOne(t *testing.T) {
	// Two interfaces, and the one that matters is not the first: eth0 is the
	// bastion's shape — a NIC on another network, which #548 leaves carrying no
	// address — so a lookup that takes any NIC would answer it and the route
	// would ride an interface the plan never named.
	devices := `{"devices":{` +
		`"eth0":{"type":"nic","network":"fnt-other"},` +
		`"eth1":{"type":"nic","network":"fnt-net"}},"expanded_devices":{}}`

	t.Run("entitled", func(t *testing.T) {
		f := &fakeRuntime{answers: map[string]string{
			// `network get <net> ipv4.address` is what networkGateway asks,
			// not the JSON of the network: the gateway address is read as a
			// bare prefix.
			"network get fnt-net ipv4.address":   "172.16.0.1/22",
			"query /1.0/instances/feint-scw-one": devices,
		}}
		d := &Incus{runner: f.run, OVN: true}
		if err := d.RouteEgress(context.Background(), "feint-scw-one", "fnt-net"); err != nil {
			t.Fatalf("route egress: %v", err)
		}
		laid := ""
		for _, call := range f.calls {
			line := strings.Join(call, " ")
			if strings.Contains(line, "route replace default") {
				laid = line
			}
		}
		if laid == "" {
			t.Fatalf("no default route was laid: %v", f.calls)
		}
		// Through the network's own gateway, and on the device that carries
		// that network — not on whatever interface came first. The bastion's
		// eth0 is the counter-example: #548 takes the address off it and it
		// keeps a default route out of an interface with no address.
		if !strings.Contains(laid, "via 172.16.0.1") || !strings.Contains(laid, "dev eth1") {
			t.Errorf("the route is %q, want it via 172.16.0.1 on eth1", laid)
		}
		// `replace` and not `add`: the routed NIC's own default route is
		// already there, and `add` answers "file exists" over it.
		if !strings.Contains(laid, "replace") {
			t.Errorf("the route is added rather than replaced: %q", laid)
		}
	})

	t.Run("not entitled", func(t *testing.T) {
		f := &fakeRuntime{}
		d := &Incus{runner: f.run, OVN: true}
		if err := d.DropEgress(context.Background(), "feint-scw-one"); err != nil {
			t.Fatalf("drop egress: %v", err)
		}
		dropped := false
		for _, call := range f.calls {
			if strings.Contains(strings.Join(call, " "), "route del default") {
				dropped = true
			}
		}
		if !dropped {
			t.Fatalf("nothing took the default route away: %v", f.calls)
		}
	})

	t.Run("a bridge lays none", func(t *testing.T) {
		f := &fakeRuntime{}
		d := &Incus{runner: f.run, OVN: false}
		if err := d.RouteEgress(context.Background(), "feint-scw-one", "fnt-net"); err != nil {
			t.Fatalf("route egress under a bridge: %v", err)
		}
		if len(f.calls) != 0 {
			t.Errorf("a bridge-mode driver touched the machine: %v", f.calls)
		}
	})
}
