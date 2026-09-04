package machine

import (
	"context"
	"errors"
	"os"
	"strconv"
	"strings"
	"testing"
)

// An OVN network never announces, as its resolver, the address by which the
// station reaches its machines (#660).
//
// Measured on 2026-09-04 under `--vm incus-ovn`: the uplink's address,
// announced as the DNS server, is also the source the station dials from, so
// the guest's RoutesToDNS= laid an on-link /32 towards it, more specific than
// the aggregates, and the reply to the station died on the switch —
// `ip route get 10.209.83.1` answered `dev eth1`; with a public resolver it
// answers `via <gateway> dev eth1`. The network's gateway was rejected as the
// alternative: nothing answers DNS there.

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

// TestAnOVNNetworkAnnouncesAPublicResolverAndNeverTheUplink holds the whole
// property: the announced resolver is the public default, and the uplink's
// address appears nowhere in the announcement. A bridge announces nothing of
// the kind — its dnsmasq is the resolver there, on-link by construction, and
// the collision does not exist.
func TestAnOVNNetworkAnnouncesAPublicResolverAndNeverTheUplink(t *testing.T) {
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
			announced := strings.Contains(line, "dns.nameservers="+DefaultResolver)
			if announced != mode.ovn {
				t.Errorf("dns.nameservers=%s present=%v, want %v: %s", DefaultResolver, announced, mode.ovn, line)
			}
			if strings.Contains(line, "dns.nameservers=10.99.0.1") {
				t.Errorf("the network announces the uplink's own address as its resolver, the dead on-link route of #660: %s", line)
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
// its own, says so, and the network announces what it said.
func TestTheResolverIsAField(t *testing.T) {
	f := resolverProbe()
	d := newFakeDriver(f)
	d.OVN = true
	d.UplinkCIDR = "10.99.0.0/24"
	d.Resolver = "192.0.2.53"
	if err := d.EnsureNetwork(context.Background(), NetworkSpec{Name: "fnt-probe", CIDR: "10.99.1.0/24", NAT: true}); err != nil {
		t.Fatalf("ensure: %v", err)
	}
	line := networkCreateLine(f)
	if !strings.Contains(line, "dns.nameservers=192.0.2.53") {
		t.Errorf("the resolver the operator named was not announced: %s", line)
	}
	if strings.Contains(line, "dns.nameservers="+DefaultResolver) {
		t.Errorf("the default was announced beside the operator's value: %s", line)
	}
}
