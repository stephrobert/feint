package machine

import (
	"context"
	"errors"
	"net/netip"
	"strings"
	"testing"
)

// The two ranges feed one uplink: an overlap hands the same address to a
// bridged guest and to an OVN router, and the collision surfaces as a machine
// that answers for a gateway.
func TestUplinkRangesSplitTheBlockWithoutOverlap(t *testing.T) {
	tests := []struct {
		cidr     string
		wantDHCP string
		wantOVN  string
		wantErr  bool
	}{
		{cidr: "10.209.83.0/24", wantDHCP: "10.209.83.2-10.209.83.127", wantOVN: "10.209.83.128-10.209.83.254"},
		{cidr: "192.168.240.0/28", wantDHCP: "192.168.240.2-192.168.240.7", wantOVN: "192.168.240.8-192.168.240.14"},
		{cidr: "10.0.0.0/30", wantErr: true}, // no room for two ranges
		{cidr: "fd42::/64", wantErr: true},   // IPv4 only
		{cidr: "10.4.0.0/16", wantDHCP: "10.4.0.2-10.4.127.255", wantOVN: "10.4.128.0-10.4.255.254"},
	}
	for _, tt := range tests {
		prefix, err := netip.ParsePrefix(tt.cidr)
		if err != nil {
			t.Fatalf("bad test input %q: %v", tt.cidr, err)
		}
		dhcp, ovn, err := uplinkRanges(prefix)
		if tt.wantErr {
			if err == nil {
				t.Errorf("uplinkRanges(%s) = %q, %q, want an error", tt.cidr, dhcp, ovn)
			}
			continue
		}
		if err != nil {
			t.Errorf("uplinkRanges(%s): %v", tt.cidr, err)
			continue
		}
		if dhcp != tt.wantDHCP || ovn != tt.wantOVN {
			t.Errorf("uplinkRanges(%s) = %q, %q, want %q, %q", tt.cidr, dhcp, ovn, tt.wantDHCP, tt.wantOVN)
		}
	}
}

// The driver's name is what the health endpoint publishes and what the
// conformance suite branches on, so it is part of the contract.
func TestIncusDriverNames(t *testing.T) {
	tests := []struct {
		vm, ovn bool
		want    string
	}{
		{want: "incus"},
		{vm: true, want: "incus-vm"},
		{ovn: true, want: "incus-ovn"},
		{vm: true, ovn: true, want: "incus-ovn-vm"},
	}
	for _, tt := range tests {
		d := &Incus{VM: tt.vm, OVN: tt.ovn}
		if got := d.Name(); got != tt.want {
			t.Errorf("(&Incus{VM: %v, OVN: %v}).Name() = %q, want %q", tt.vm, tt.ovn, got, tt.want)
		}
	}
}

// uplinkFake answers what EnsureNetwork asks on its OVN path: the network to
// create is absent, the uplink exists carrying the emulator's label, its route
// list holds one delegated block and one routed /32, and the uplink rule set
// does not exist yet.
func uplinkFake() *fakeRuntime {
	return &fakeRuntime{
		answers: map[string]string{
			"query /1.0/networks/" + DefaultUplinkName:          `{"type":"bridge","config":{"user.` + LabelKey + `":"feint"}}`,
			"network get " + DefaultUplinkName + " ipv4.routes": "10.191.1.0/24,203.0.113.7/32",
		},
		fail: map[string]error{
			"query /1.0/networks/fnt-b": errors.New("Network not found"),
			"network acl show":          errors.New("Network ACL not found"),
		},
	}
}

// Issue #201: two unpeered OVN networks reached each other through the uplink,
// both protocols, because every delegated block is a scope-link host route and
// nothing closed the path. Creating an OVN network must therefore write a rule
// set on the uplink that rejects every delegated network block — the routed
// /32 public addresses excluded, they are the emulated internet — and attach
// it. This is the test the falsification spec names.
func TestTheUplinkRejectsDelegatedBlocks(t *testing.T) {
	f := uplinkFake()
	d := newFakeDriver(f)
	d.OVN = true

	err := d.EnsureNetwork(context.Background(), NetworkSpec{
		Name: "fnt-b", CIDR: "10.192.1.0/24", NAT: false,
	})
	if err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}

	acl := isolationACL(DefaultUplinkName)
	puts := f.matching("query -X PUT --data ")
	var body string
	for _, cmd := range puts {
		if strings.Contains(cmd, "/1.0/network-acls/"+acl) {
			body = cmd
		}
	}
	if body == "" {
		t.Fatalf("no rule-set write for %s; calls: %v", acl, f.commands())
	}
	for _, block := range []string{"10.191.1.0/24", "10.192.1.0/24"} {
		if !strings.Contains(body, `"action":"reject","state":"enabled","destination":"`+block+`"`) {
			t.Errorf("the uplink rule set does not reject %s: %s", block, body)
		}
	}
	if strings.Contains(body, "203.0.113.7") {
		t.Errorf("the uplink rule set names a routed /32, which must stay reachable: %s", body)
	}
	if !strings.Contains(body, `"action":"allow"`) {
		t.Errorf("the uplink rule set has no catch-all allow, everything else would fall to the default: %s", body)
	}
	if got := f.matching("network set " + DefaultUplinkName + " security.acls " + acl); len(got) == 0 {
		t.Errorf("the rule set was written but never attached to the uplink; calls: %v", f.commands())
	}
}

// A block already delegated returns early — and an interrupted earlier run can
// have left exactly that state: route standing, rule set missing. The early
// path must still assert the rule set, or the leak reopens silently on the
// instance that recreates a network.
func TestADelegatedBlockAlreadyPresentStillAssertsTheUplinkRuleSet(t *testing.T) {
	f := uplinkFake()
	f.answers["network get "+DefaultUplinkName+" ipv4.routes"] = "10.191.1.0/24,10.192.1.0/24"
	d := newFakeDriver(f)
	d.OVN = true

	err := d.EnsureNetwork(context.Background(), NetworkSpec{
		Name: "fnt-b", CIDR: "10.192.1.0/24", NAT: false,
	})
	if err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	acl := isolationACL(DefaultUplinkName)
	writes := f.matching("/1.0/network-acls/" + acl)
	if len(writes) == 0 {
		t.Fatalf("an already-delegated block skipped the uplink rule set; calls: %v", f.commands())
	}
	// The write must carry the rules, not merely happen: an empty rule set is
	// the leak with a receipt.
	if !strings.Contains(strings.Join(writes, " "), `"destination":"10.192.1.0/24"`) {
		t.Errorf("the re-asserted rule set does not reject the delegated block: %v", writes)
	}
}

// The uplink rule set must be the emulator's to remove: the sweep recognises
// it by name even when an interrupted run never wrote its description, and
// RemoveFirewall deletes it without a description round trip.
func TestTheUplinkIsolationIsSweepable(t *testing.T) {
	acl := isolationACL(DefaultUplinkName)
	if !isolationOwned(acl) {
		t.Fatalf("isolationOwned(%q) = false; an interrupted run would leave it behind forever", acl)
	}
	f := &fakeRuntime{}
	d := newFakeDriver(f)
	if err := d.RemoveFirewall(context.Background(), acl); err != nil {
		t.Fatalf("RemoveFirewall(%s): %v", acl, err)
	}
	if got := f.matching("network acl delete " + acl); len(got) == 0 {
		t.Errorf("RemoveFirewall never deleted %s; calls: %v", acl, f.commands())
	}
}

// Only the OVN mode may claim native isolation: a pack hearing "true" stops
// writing reject rules, and on bridges nothing else would separate anything.
func TestNativeIsolationFollowsTheMode(t *testing.T) {
	if NewIncus().NativeIsolation() {
		t.Error("the bridge driver claims native isolation")
	}
	if !NewIncusOVN().NativeIsolation() {
		t.Error("the OVN driver does not claim native isolation")
	}
}
