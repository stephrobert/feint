package machine

import (
	"context"
	"errors"
	"net/netip"
	"os"
	"strconv"
	"strings"
	"testing"
)

// An OVN network names, in its lease, the one address a guest may safely lay
// an on-link route towards: its own gateway (#660, #684, #697).
//
// The invariant is the guest's. systemd-networkd's RoutesToDNS= lays an
// on-link /32 towards every DNS server a lease names, and a /32 towards an
// address that is not on the segment is dead — the guest ARPs for it on the
// switch and nobody answers. Three values were measured against it:
//
//   - the uplink's address, which Incus names when nobody else does (#660,
//     and #697 again through that fallback): it is also the source the
//     station dials from, so the reply to a published address died on the
//     switch;
//   - a public resolver (#684): 1.1.1.1 is not on the segment either,
//
//	1.1.1.1 dev eth0 proto dhcp scope link src 10.188.0.5 metric 100
//	ping 1.1.1.1 -> unreachable; resolvectl query -> No route to host
//
//   - nothing (#693): not "no route" — Incus falls back to the host's
//     resolvers, under OVN the uplink, and #660's dead /32 came back through
//     somebody else's default. Four red scheduled nights on platform-web-0
//     (#697).
//
// The gateway is on the segment by construction, so the /32 towards it is
// live and harmless. Nothing answers DNS there, and that stopped mattering
// with #694 and #696: the name server the guest uses reaches it through a
// networkd drop-in and resolvectl, ahead of the lease's, and lays no route.

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

// networkCreateCall is the create as argv, for the assertions that read one
// option's value rather than grep the line.
func networkCreateCall(f *fakeRuntime) []string {
	for _, call := range f.calls {
		if strings.Contains(strings.Join(call, " "), "network create fnt-probe") {
			return call
		}
	}
	return nil
}

func networkCreateLine(f *fakeRuntime) string {
	return strings.Join(networkCreateCall(f), " ")
}

// announcedNameServers reads the name servers a create puts in the lease:
// none when the option is absent or empty, and the two are deliberately not
// told apart — both make Incus fall back to the host's resolvers, the empty
// key measured on #694.
func announcedNameServers(call []string) []string {
	var servers []string
	for _, arg := range call {
		value, ok := strings.CutPrefix(arg, "dns.nameservers=")
		if !ok {
			continue
		}
		for _, server := range strings.Split(value, ",") {
			if server = strings.TrimSpace(server); server != "" {
				servers = append(servers, server)
			}
		}
	}
	return servers
}

// TestAnOVNNetworkLaysNoRouteTowardsItsResolver holds the invariant rather
// than one value of it: every name server the lease names is on the network's
// own segment, and the lease names one — an absent option is the uplink by
// Incus's fallback, which is the four red nights of #697 and not a safer
// state. A bridge names none itself: its dnsmasq is the resolver there,
// on-link by construction, and the collision does not exist.
func TestAnOVNNetworkLaysNoRouteTowardsItsResolver(t *testing.T) {
	const block = "10.99.1.0/24"
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
				Name: "fnt-probe", CIDR: block, NAT: true,
			}); err != nil {
				t.Fatalf("ensure: %v", err)
			}
			call := networkCreateCall(f)
			if call == nil {
				t.Fatalf("no network create; calls: %v", f.calls)
			}
			servers := announcedNameServers(call)
			if !mode.ovn {
				if len(servers) > 0 {
					t.Errorf("a bridge network names %v in its lease, over the dnsmasq that answers on-link there: %v", servers, call)
				}
				return
			}
			if len(servers) == 0 {
				t.Fatalf("the OVN network names no resolver in its lease, so Incus names the uplink's address for it and every guest lays a dead on-link /32 towards the station's own source (#697): %v", call)
			}
			segment := netip.MustParsePrefix(block)
			for _, server := range servers {
				addr, err := netip.ParseAddr(server)
				if err != nil {
					t.Errorf("the lease names %q, which is not an address: %v", server, err)
					continue
				}
				if !segment.Contains(addr) {
					t.Errorf("the lease names %s, outside %s: the on-link /32 RoutesToDNS= lays towards it is dead, whether the address is the uplink's (#660) or a public resolver's (#684)", server, segment)
				}
			}
			// And the accepting half, or a create that stopped configuring
			// anything at all would pass: the routes ARE announced, and they
			// are what a guest needs from the lease.
			if !strings.Contains(strings.Join(call, " "), "ipv4.dhcp.routes=") {
				t.Errorf("the OVN network announces no routes either, so this test is measuring an empty create: %v", call)
			}
		})
	}
}

// TestAResolverThatIsTheUplinkIsRefused: the field cannot name the uplink.
// Since #697 the field no longer reaches the lease — the lease names the
// gateway, the field goes through the drop-in and resolvectl, which lay no
// route — so what this protects has moved: it is the guard against the
// announcement coming back through the field, the way #660's first fix put
// `dns.nameservers=<resolver>` in the lease, and against a guest being
// pointed, by any door, at the one address that is the station's own source
// towards it. The value the uplink was given is the value refused, derived
// once (uplinkGateway), and no network is created.
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
		"resolvectl dns eth1": "Link 2 (eth1): " + DefaultResolver + "\n",
	}}
	d := newFakeDriver(f)
	d.OVN = true
	d.Resolver = "192.0.2.53"
	f.answers["resolvectl dns eth1"] = "Link 2 (eth1): 192.0.2.53\n"

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
		"resolvectl dns eth1": "Link 2 (eth1): " + DefaultResolver + "\n",
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
		"resolvectl dns eth1": "Link 2 (eth1): " + DefaultResolver + "\n",
	}}
	d := newFakeDriver(f)
	d.OVN = false

	d.settleGuestInterface(context.Background(), "srv", "eth1")

	if i := indexOfCall(f, "resolvectl dns"); i >= 0 {
		t.Errorf("a bridge guest had its lease's resolver overridden:\n%s",
			strings.Join(f.commands(), "\n"))
	}
}
