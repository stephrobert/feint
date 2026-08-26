package machine

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
)

// Two OVN networks that are not peered must not reach each other (#201).
//
// The claim this asserts was written in prose and never held: IsolateNetwork
// returned early under OVN because "an OVN network reaches nothing it is not
// peered with", and the packs' native branch called PeerNetworks alone, so no
// rule set ever reached an OVN network.
//
// Measured on a station publishing capabilities.isolation: true — two Nets, no
// peering, reachable in ICMP and in TCP. The host routes between them, because
// every one of this driver's OVN subnets is `scope link` on the uplink it
// creates for them. tools/conformance/*/network.sh holds the property against
// the runtime; this holds the arguments that carry it.
func TestUnpeeredOVNNetworksAreIsolatedFromEachOther(t *testing.T) {
	f := &fakeRuntime{}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "query /1.0/networks?recursion=1"):
			return []byte(`[
				{"name":"fnt-aaa","type":"ovn","config":{"ipv4.address":"10.1.0.1/24"}},
				{"name":"fnt-bbb","type":"ovn","config":{"ipv4.address":"10.2.0.1/24"}},
				{"name":"fnt-ccc","type":"ovn","config":{"ipv4.address":"10.3.0.1/24"}},
				{"name":"incusbr0","type":"bridge","config":{"ipv4.address":"10.76.154.1/24"}}
			]`), nil, true
		case strings.Contains(joined, "/peers?recursion=1"):
			return []byte(`[]`), nil, true
		case strings.HasPrefix(joined, "network acl show"):
			return nil, errors.New("Error: Network ACL not found"), true
		}
		return nil, nil, false
	}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.PeerNetworks(context.Background(), "fnt-aaa", []string{"fnt-bbb"}); err != nil {
		t.Fatalf("peer: %v", err)
	}

	// The rule set must name the block of the network that is not peered, and
	// only it.
	edits := f.matching("network acl edit")
	if len(edits) == 0 {
		// The driver writes the body through stdin on some paths; fall back to
		// whatever carried the blocks.
		edits = f.matching("10.3.0")
	}
	if len(edits) == 0 {
		t.Fatalf("no rule set was written for fnt-aaa:\n%s", strings.Join(f.commands(), "\n"))
	}
	body := strings.Join(edits, "\n")
	if !strings.Contains(body, "10.3.0") {
		t.Errorf("the rule set does not keep out the unpeered network's block:\n%s", body)
	}
	if strings.Contains(body, "10.2.0") {
		t.Errorf("the rule set keeps out a network it is peered with:\n%s", body)
	}
	// And never the operator's own bridge.
	if strings.Contains(body, "10.76.154") {
		t.Errorf("the rule set names a network the emulator did not create:\n%s", body)
	}
}

// foreignBlocks is the decision itself, and it is worth holding on its own: the
// accepting half (a peer is not foreign) and the refusing half (everything else
// is) are the two ways this can be wrong, and a version that returned nothing
// would pass a test that only checked the peer.
func TestForeignBlocksKeepsPeersAndTheNetworkItself(t *testing.T) {
	subnets := map[string]string{
		"fnt-aaa": "10.1.0.1/24",
		"fnt-bbb": "10.2.0.1/24",
		"fnt-ccc": "10.3.0.1/24",
	}
	got := foreignBlocks("fnt-aaa", []string{"fnt-bbb"}, subnets)
	if len(got) != 1 || got[0] != "10.3.0.1/24" {
		t.Errorf("foreign blocks = %v, want only the unpeered network's", got)
	}

	// Peered with everything: nothing is foreign, and a rule set that kept a
	// block anyway would separate networks a client asked to join.
	if got := foreignBlocks("fnt-aaa", []string{"fnt-bbb", "fnt-ccc"}, subnets); len(got) != 0 {
		t.Errorf("a network peered with every other still keeps out %v", got)
	}

	// Peered with nothing: every other block, and never its own.
	got = foreignBlocks("fnt-aaa", nil, subnets)
	if len(got) != 2 {
		t.Errorf("an unpeered network keeps out %v, want both others", got)
	}
	for _, block := range got {
		if block == "10.1.0.1/24" {
			t.Error("a network was isolated from itself")
		}
	}
}

// The isolation set used to end with a catch-all allow in both directions.
// Under OVN that allow sat at rule priority 300 in the single pipeline where a
// NIC's default action sits at 100/111 (acl_ovn.go at v7.2.0), so it outranked
// every security group's default deny on every NIC of the network: a port no
// rule opens answered from the station on any multi-subnet run (#491), the
// forbidden port flipping OPEN→CLOSED the moment the set was detached, nothing
// else changed. The rejects stay at 400, above every allow a group can state —
// which is what keeps the two properties at once: the foreign subnets stay out
// whatever the groups say, and the groups' default deny holds again.
func TestAnOVNIsolationSetCarriesNoCatchAllAllow(t *testing.T) {
	f := isolationFake()
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.IsolateNetwork(context.Background(), "fnt-aaa", []string{"10.2.0.0/24"}); err != nil {
		t.Fatalf("isolate: %v", err)
	}

	body := isolationBody(t, f, "iso-fnt-aaa")
	for _, direction := range [][]aclRule{body.Ingress, body.Egress} {
		for _, rule := range direction {
			if rule.Action == "allow" || rule.Action == "allow-stateless" {
				t.Fatalf("the OVN isolation set carries an allow rule, which outranks every NIC default deny: %+v", rule)
			}
		}
	}
	if len(body.Ingress) == 0 || len(body.Egress) == 0 {
		t.Fatalf("the rejects are gone with the catch-all: %+v", body)
	}
	for _, rule := range body.Egress {
		if rule.Action != "reject" || rule.Destination != "10.2.0.0/24" {
			t.Errorf("egress rule does not reject the foreign block: %+v", rule)
		}
	}
}

// The bridge half keeps its catch-all: there a network ACL filters at the
// bridge-host boundary, a separate mechanism from the NIC rule sets, and
// without the allow the boundary would reject the station itself.
func TestABridgeIsolationSetKeepsItsCatchAll(t *testing.T) {
	f := isolationFake()
	d := newFakeDriver(f)

	if err := d.IsolateNetwork(context.Background(), "fnt-aaa", []string{"10.2.0.0/24"}); err != nil {
		t.Fatalf("isolate: %v", err)
	}

	body := isolationBody(t, f, "iso-fnt-aaa")
	allows := 0
	for _, direction := range [][]aclRule{body.Ingress, body.Egress} {
		for _, rule := range direction {
			if rule.Action == "allow" && rule.Source == "" && rule.Destination == "" {
				allows++
			}
		}
	}
	if allows != 2 {
		t.Fatalf("the bridge isolation set must keep its catch-all allow in both directions, found %d:\n%+v", allows, body)
	}
}

// Attaching an ACL to an OVN network is what makes the runtime add the reject
// default to every NIC of the network. A machine applied before the network
// became isolated, whose groups enforce nothing, carries no rule set — so the
// isolation would close it. The sweep puts the permissive posture set on those
// NICs, before the network attach so no machine passes through a closed
// instant; it never touches a NIC that carries a rule set, and never an
// instance the emulator did not create, because the instance list comes from
// the host and editing a stranger's devices is the audit class ownership
// checks exist for.
func TestIsolatingANetworkSpreadsThePermissiveSetToSetlessNICs(t *testing.T) {
	f := isolationFake()
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.IsolateNetwork(context.Background(), "fnt-aaa", []string{"10.2.0.0/24"}); err != nil {
		t.Fatalf("isolate: %v", err)
	}

	spread := f.matching("config device set srv-bare eth1 security.acls=opn-fnt")
	if len(spread) != 1 {
		t.Fatalf("the set-less NIC did not receive the permissive set:\n%s", strings.Join(f.commands(), "\n"))
	}
	if got := f.matching("config device set srv-grouped"); len(got) != 0 {
		t.Errorf("a NIC already carrying a rule set was edited: %v", got)
	}
	for _, cmd := range f.commands() {
		if strings.Contains(cmd, "production-database") {
			t.Fatalf("a command names an instance the emulator did not create: %q", cmd)
		}
	}
	if got := f.matching("/1.0/network-acls/opn-fnt"); len(got) == 0 {
		t.Error("the permissive set was attached without being written first")
	}

	// Before the network attach: after it, the NIC would already be closed.
	spreadAt, attachAt := -1, -1
	for i, cmd := range f.commands() {
		switch {
		case strings.Contains(cmd, "security.acls=opn-fnt"):
			if spreadAt == -1 {
				spreadAt = i
			}
		case strings.HasPrefix(cmd, "network set fnt-aaa security.acls"):
			attachAt = i
		}
	}
	if attachAt == -1 || spreadAt == -1 || spreadAt > attachAt {
		t.Errorf("the permissive set must reach the NICs before the network attach (spread at %d, attach at %d)", spreadAt, attachAt)
	}
}

// The exact undo: once the network's ACL is gone, a NIC carrying only the
// permissive set goes back to carrying none, and everything else is left
// alone.
func TestDeisolatingANetworkWithdrawsThePermissiveSet(t *testing.T) {
	f := &fakeRuntime{}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		if strings.HasPrefix(strings.Join(args, " "), "query /1.0/instances?recursion=1") {
			return []byte(`[
				{"name":"srv-bare","config":{"user.feint.provider":"scaleway"},
				 "expanded_devices":{"eth1":{"type":"nic","network":"fnt-aaa","security.acls":"opn-fnt"}},
				 "devices":{"eth1":{"type":"nic","network":"fnt-aaa","security.acls":"opn-fnt"}}},
				{"name":"srv-grouped","config":{"user.feint.provider":"scaleway"},
				 "expanded_devices":{"eth1":{"type":"nic","network":"fnt-aaa","security.acls":"scw-abc"}},
				 "devices":{"eth1":{"type":"nic","network":"fnt-aaa","security.acls":"scw-abc"}}}
			]`), nil, true
		}
		return nil, nil, false
	}
	d := newFakeDriver(f)
	d.OVN = true

	if err := d.IsolateNetwork(context.Background(), "fnt-aaa", nil); err != nil {
		t.Fatalf("de-isolate: %v", err)
	}

	cleared := f.matching("config device set srv-bare eth1 security.acls=")
	if len(cleared) != 1 {
		t.Fatalf("the permissive set was not withdrawn:\n%s", strings.Join(f.commands(), "\n"))
	}
	if got := f.matching("config device set srv-grouped"); len(got) != 0 {
		t.Errorf("a NIC carrying a security group was edited on de-isolation: %v", got)
	}

	// After the network detach, for the same no-closed-instant reason in
	// reverse: while the network still carries the ACL, a bare NIC is a closed
	// NIC.
	unsetAt, clearAt := -1, -1
	for i, cmd := range f.commands() {
		switch {
		case strings.HasPrefix(cmd, "network unset fnt-aaa security.acls"):
			unsetAt = i
		case strings.Contains(cmd, "srv-bare eth1 security.acls="):
			clearAt = i
		}
	}
	if unsetAt == -1 || clearAt == -1 || clearAt < unsetAt {
		t.Errorf("the withdrawal must follow the network detach (unset at %d, clear at %d)", unsetAt, clearAt)
	}
}

// isolationFake answers what IsolateNetwork asks on the way to writing a rule
// set: the network exists, no ACL exists yet, and the host runs three
// instances — one of the emulator's with no rule set, one with a group, and
// one that is somebody else's.
func isolationFake() *fakeRuntime {
	f := &fakeRuntime{}
	f.hook = func(_ int, args []string) ([]byte, error, bool) {
		joined := strings.Join(args, " ")
		switch {
		case strings.HasPrefix(joined, "network acl show iso-"):
			return nil, errors.New("Error: Network ACL not found"), true
		case strings.HasPrefix(joined, "query /1.0/instances?recursion=1"):
			return []byte(`[
				{"name":"srv-bare","config":{"user.feint.provider":"scaleway"},
				 "expanded_devices":{"eth1":{"type":"nic","network":"fnt-aaa"}},
				 "devices":{"eth1":{"type":"nic","network":"fnt-aaa"}}},
				{"name":"srv-grouped","config":{"user.feint.provider":"scaleway"},
				 "expanded_devices":{"eth1":{"type":"nic","network":"fnt-aaa","security.acls":"scw-abc"}},
				 "devices":{"eth1":{"type":"nic","network":"fnt-aaa","security.acls":"scw-abc"}}},
				{"name":"production-database","config":{},
				 "expanded_devices":{"eth0":{"type":"nic","network":"fnt-aaa"}},
				 "devices":{"eth0":{"type":"nic","network":"fnt-aaa"}}}
			]`), nil, true
		}
		return nil, nil, false
	}
	return f
}

// isolationBody digs the JSON the driver PUT to the named rule set out of the
// recorded calls.
func isolationBody(t *testing.T, f *fakeRuntime, name string) aclBody {
	t.Helper()
	for _, call := range f.calls {
		if len(call) < 2 || call[0] != "query" {
			continue
		}
		if call[len(call)-1] != "/1.0/network-acls/"+name {
			continue
		}
		for i, arg := range call {
			if arg == "--data" && i+1 < len(call) {
				var body aclBody
				if err := json.Unmarshal([]byte(call[i+1]), &body); err != nil {
					t.Fatalf("unreadable rule set body: %v", err)
				}
				return body
			}
		}
	}
	t.Fatalf("no rule set was written for %s:\n%s", name, strings.Join(f.commands(), "\n"))
	return aclBody{}
}
