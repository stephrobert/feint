package machine

import (
	"context"
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
