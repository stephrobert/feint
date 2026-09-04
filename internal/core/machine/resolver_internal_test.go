package machine

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

// An OVN network hands its machines a resolver without ever announcing one in
// the lease (#660, and the half of it that #684 closed).
//
// #660 measured the first half: the uplink's address, announced as the DNS
// server, is also the source the station dials from, so the guest's
// RoutesToDNS= laid an on-link /32 towards it, more specific than the
// aggregates, and the reply to the station died on the switch. Its answer was
// to announce a public resolver instead. The network's gateway was rejected as
// the alternative, and re-measured on 2026-09-04 with the check made
// independent of the others: nothing answers DNS there, `getent` returns
// nothing on all three shapes.
//
// #684 measured the second half, once the interfaces were managed at all. THE
// SAME MECHANISM BREAKS A PUBLIC RESOLVER, for the same reason: a name server
// in the lease gets an on-link /32, and 1.1.1.1 is not on the subnet either.
//
//	1.1.1.1 dev eth0 proto dhcp scope link src 10.188.0.5 metric 100
//	ping 1.1.1.1 -> unreachable; resolvectl query -> No route to host
//	ip route del 1.1.1.1 dev eth0 -> reachable
//	resolvectl dns eth0 1.1.1.1 -> getent hosts deb.debian.org answers
//
// So no resolver is announced in the lease at all. The one the network stands
// for reaches the guest through resolvectl (setGuestResolver), which sets a
// name server and lays no route.

// resolverProbe is the uplink harness of the egress tests: an uplink this run
// holds on 10.99.0.0/24 (gateway 10.99.0.1), and the network absent so
// EnsureNetwork creates rather than adopts.
func resolverProbe() *fakeRuntime {
	return &fakeRuntime{answers: map[string]string{
		"query /1.0/networks/feint-uplink": ourUplinkJSON(strconv.Itoa(os.Getpid()), "10.99.0.0/24"),
		"query /1.0/networks?recursion=1":  "[]",
	}, fail: map[string]error{
		"query /1.0/networks/fnt-probe": errors.New("Network not found"),
	}}
}

func networkCreateLine(f *fakeRuntime) string {
	for _, call := range f.calls {
		line := strings.Join(call, " ")
		if strings.Contains(line, "network create fnt-probe") {
			return line
		}
	}
	return ""
}

// TestAnOVNNetworkLaysNoRouteTowardsItsResolver holds the whole property: no
// name server is put in the lease, by either mode, so no guest lays an on-link
// route towards one. A bridge never did — its dnsmasq is the resolver there,
// on-link by construction, and the collision does not exist.
func TestAnOVNNetworkLaysNoRouteTowardsItsResolver(t *testing.T) {
	for _, mode := range []struct {
		name string
		ovn  bool
	}{{name: "ovn", ovn: true}, {name: "bridge", ovn: false}} {
		t.Run(mode.name, func(t *testing.T) {
			f := resolverProbe()
			d := newFakeDriver(f)
			d.OVN = mode.ovn
			d.UplinkCIDR = "10.99.0.0/24"
			if err := d.EnsureNetwork(context.Background(), NetworkSpec{
				Name: "fnt-probe", CIDR: "10.99.1.0/24", NAT: true,
			}); err != nil {
				t.Fatalf("ensure: %v", err)
			}
			line := networkCreateLine(f)
			if line == "" {
				t.Fatalf("no network create; calls: %v", f.calls)
			}
			if strings.Contains(line, "dns.nameservers") {
				t.Errorf("a name server is in the lease, which is the on-link /32 that costs the machine its way out (#684): %s", line)
			}
			// And the accepting half, or a create that stopped configuring
			// anything at all would pass: the routes ARE announced, and they
			// are what a guest needs from the lease.
			if mode.ovn && !strings.Contains(line, "ipv4.dhcp.routes=") {
				t.Errorf("the OVN network announces no routes either, so this test is measuring an empty create: %s", line)
			}
		})
	}
}

// TestAResolverThatIsTheUplinkIsRefused: the field cannot put the collision
// back. The value the uplink was given is the value refused, derived once
// (uplinkGateway), and no network is created.
func TestAResolverThatIsTheUplinkIsRefused(t *testing.T) {
	f := resolverProbe()
	d := newFakeDriver(f)
	d.OVN = true
	d.UplinkCIDR = "10.99.0.0/24"
	d.Resolver = "10.99.0.1"
	err := d.EnsureNetwork(context.Background(), NetworkSpec{Name: "fnt-probe", CIDR: "10.99.1.0/24", NAT: true})
	if err == nil {
		t.Fatal("a resolver on the uplink's address was accepted")
	}
	for _, want := range []string{"10.99.0.1", "uplink", "#660"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal does not name %q: %v", want, err)
		}
	}
	if networkCreateLine(f) != "" {
		t.Errorf("the network was created despite the refusal: %v", f.calls)
	}
}

// TestTheResolverIsAField: a station without Internet, or with a resolver of
// its own, says so, and the guests get what it said — through resolvectl now,
// not through the lease.
func TestTheResolverIsAField(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"ip -o link show dev": "2: eth1: <BROADCAST,MULTICAST,UP>\n",
	}}
	d := newFakeDriver(f)
	d.OVN = true
	d.Resolver = "192.0.2.53"

	d.settleGuestInterface(context.Background(), "srv", "eth1")

	if i := indexOfCall(f, "resolvectl dns eth1 192.0.2.53"); i < 0 {
		t.Errorf("the guest was not given the resolver the operator named:\n%s",
			strings.Join(f.commands(), "\n"))
	}
	if i := indexOfCall(f, "resolvectl dns eth1 "+DefaultResolver); i >= 0 {
		t.Errorf("the default was set beside the operator's value:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}

// TestASettleGivesTheInterfaceItsResolver: the default reaches the guest, after
// the reload rather than before, since a reload drops the runtime setting.
func TestASettleGivesTheInterfaceItsResolver(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"ip -o link show dev": "2: eth1: <BROADCAST,MULTICAST,UP>\n",
	}}
	d := newFakeDriver(f)
	d.OVN = true

	d.settleGuestInterface(context.Background(), "srv", "eth1")

	set := indexOfCall(f, "resolvectl dns eth1 "+DefaultResolver)
	reload := indexOfCall(f, "networkctl reload")
	if set < 0 {
		t.Fatalf("the interface was never given a resolver:\n%s", strings.Join(f.commands(), "\n"))
	}
	if reload < 0 || set < reload {
		t.Fatalf("the resolver was set at %d, before the reload at %d that drops it:\n%s",
			set, reload, strings.Join(f.commands(), "\n"))
	}
}

// A managed bridge resolves through its own dnsmasq, on-link and in the lease.
// Setting a resolver there would override a working one with a public address
// the guest may not be able to reach at all.
func TestABridgeGuestKeepsTheResolverItsLeaseCarries(t *testing.T) {
	f := &fakeRuntime{answers: map[string]string{
		"ip -o link show dev": "2: eth1: <BROADCAST,MULTICAST,UP>\n",
	}}
	d := newFakeDriver(f)
	d.OVN = false

	d.settleGuestInterface(context.Background(), "srv", "eth1")

	if i := indexOfCall(f, "resolvectl dns"); i >= 0 {
		t.Errorf("a bridge guest had its lease's resolver overridden:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}
