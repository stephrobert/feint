package machine

import (
	"context"
	"encoding/json"
	"fmt"
	"sort"
)

// Two OVN networks that are not peered must not reach each other (#201).
//
// The claim this replaces was written in prose and never asserted, in two
// places at once. `IsolateNetwork` returned early under OVN on the grounds that
// "an OVN network reaches nothing it is not peered with: there is no shared L2
// to build reject rules against", and the packs' isolateNetworks took the
// native-isolation branch and called PeerNetworks alone, so no rule set was ever
// applied to an OVN network at all.
//
// Measured on a station with OVN wired and `capabilities.isolation: true`
// published: two Outscale Nets with no peering between them, one machine in
// each, reachable in ICMP *and* in TCP. The mechanism is in the host's routing
// table, not in OVN:
//
//	10.191.1.0/24 dev feint-uplink proto static scope link
//	10.192.1.0/24 dev feint-uplink proto static scope link
//
// Both networks carry their block on the uplink this driver creates for them, so
// the host forwards between them. The shared L2 the comment declared absent is
// the emulator's own uplink.
//
// The fix is an ingress rule set on each network, and that it works is measured
// rather than assumed: with a reject rule for the other block applied to the
// destination network, the same ping stops arriving, and the machine keeps
// reaching its own gateway. So this needs no rule in the operator's firewall,
// which would have been a much larger decision than a driver change.
//
// TestUnpeeredOVNNetworksAreIsolatedFromEachOther fails without this.

// ourOVNSubnets answers the block each of the emulator's own OVN networks
// carries, keyed by network name.
//
// Only networks under this driver's own prefix: an operator's networks are none
// of its business, and a rule set that named one would be the first step towards
// filtering traffic nobody asked it to touch.
func (d *Incus) ourOVNSubnets(ctx context.Context) (map[string]string, error) {
	out, err := d.run(ctx, "query", "/1.0/networks?recursion=1")
	if err != nil {
		return nil, fmt.Errorf("list networks: %w", err)
	}
	var raw []struct {
		Name   string            `json:"name"`
		Type   string            `json:"type"`
		Config map[string]string `json:"config"`
	}
	if err := json.Unmarshal(out, &raw); err != nil {
		return nil, fmt.Errorf("decode the network list: %w", err)
	}
	subnets := map[string]string{}
	for _, network := range raw {
		if network.Type != "ovn" || !ownedNetwork(network.Name) {
			continue
		}
		if block := network.Config["ipv4.address"]; block != "" {
			subnets[network.Name] = block
		}
	}
	return subnets, nil
}

// foreignBlocks answers the blocks a network must not reach: every other network
// of this emulator's, minus the ones it is peered with.
//
// Derived from the peers the caller is applying rather than from the runtime's
// current state, so the rule set and the peering move together. Reading the
// peers back would isolate against whatever was true a moment ago.
func foreignBlocks(network string, peers []string, subnets map[string]string) []string {
	allowed := map[string]bool{network: true}
	for _, peer := range peers {
		allowed[peer] = true
	}
	var blocks []string
	for name, block := range subnets {
		if allowed[name] {
			continue
		}
		blocks = append(blocks, block)
	}
	// Sorted so two runs write the same rule set and a diff of `network acl
	// show` means something.
	sort.Strings(blocks)
	return blocks
}

// isolateOVN applies the rule set that keeps a network away from every other one
// of the emulator's that it is not peered with.
//
// Errors are returned rather than logged: a peering that reports success while
// the isolation silently failed is the false capability this whole file exists
// to remove.
func (d *Incus) isolateOVN(ctx context.Context, network string, peers []string) error {
	subnets, err := d.ourOVNSubnets(ctx)
	if err != nil {
		return err
	}
	return d.IsolateNetwork(ctx, network, foreignBlocks(network, peers, subnets))
}
