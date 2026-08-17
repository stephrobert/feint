package outscale

import (
	"errors"
	"net/netip"

	"github.com/stephrobert/feint/internal/core/resource"
)

// Placing a Vm on a Subnet, which the pack accepted and threw away.
//
// CreateVms declared SubnetId and SecurityGroupIds and no code read either. That
// is worse than not declaring them: the unread-field observer — built precisely
// to catch an argument a client sent and a handler dropped — only reports fields
// nobody declared, so declaring one without reading it is its single blind spot.
// A client got a 200, then read back a Vm with no SubnetId, and Terraform saw
// permanent drift. An audit found it; nothing here could have.
//
// It also made the addressing plane a dead end. Nets and Subnets could be built,
// checked and backed by real networks, and nothing could be placed on them.

// errUnknownSubnet is what a create gets for a SubnetId nobody registered. It is
// refused rather than ignored: accepting it would put the Vm nowhere while
// telling the client it went where it asked.
var errUnknownSubnet = errors.New("no such subnet")

// placement is what a Vm's SubnetId resolves to: the Net it belongs to, the
// address it takes inside the Subnet, and the runtime network that carries it.
type placement struct {
	SubnetID  string
	NetID     string
	Address   netip.Addr
	PrefixLen int
	// SubregionName is the zone the Subnet was created in — a stored fact
	// since #269, and what a Vm created without a Placement of its own
	// inherits (#268): its machine can only sit where its Subnet sits.
	SubregionName string
	// Network is the runtime network backing the Subnet, empty when no runtime
	// is configured. Recorded for the caller that wants to know without asking
	// the store again; the boot rebuilds it from the stored Subnet instead, so
	// a machine restarted after a snapshot restore lands on the same network.
	Network string
}

// placeInSubnet allocates an address for a new Vm inside a Subnet.
//
// The address is allocated rather than invented, from the same allocator the
// Subnet's own count is computed with, so what the API publishes as available
// and what it hands out cannot disagree. The allocator is subnetAllocator's —
// one constructor, not two. This function used to carry its own reservation
// loops, and the copy differed from subnetAllocator on exactly one point: it
// reserved the addresses of terminated Vms forever, so a Subnet slowly filled
// with ghosts nothing could release, while deleteVms itself says upstream
// frees the private address at termination. A first unification was reverted
// on the verdict of the then state-blind sweep invariant, which reported the
// legitimate reuse as a double allocation — the false verdict sweep.go now
// records. The allocator is rebuilt per call because the store is the state,
// and holding a long-lived allocator beside it is how the two drift apart
// across a snapshot restore.
//
// TestATerminatedVmsAddressReturnsToItsSubnet fails without the release.
func (p *Pack) placeInSubnet(subnetID string) (placement, error) {
	subnet, found := p.env.Store.Get(Name, kindSubnet, subnetID)
	if !found {
		return placement{}, errUnknownSubnet
	}
	prefix, err := prefixOf(subnet, "IpRange")
	if err != nil {
		return placement{}, err
	}
	allocator, err := p.subnetAllocator(subnetID)
	if err != nil {
		return placement{}, err
	}

	address, err := allocator.Allocate()
	if err != nil {
		return placement{}, err
	}
	netID := stringOf(subnet.Attrs["NetId"])
	return placement{
		SubnetID:      subnetID,
		NetID:         netID,
		Address:       address,
		PrefixLen:     prefix.Bits(),
		SubregionName: orDefault(stringOf(subnet.Attrs["SubregionName"]), defaultSubregionName),
		Network:       subnet.Runtime[runtimeNetworkKey],
	}, nil
}

// apply records the placement on the resource, in the fields Outscale's own Vm
// schema declares for them.
func (pl placement) apply(res *resource.Resource) {
	res.Attrs["SubnetId"] = pl.SubnetID
	res.Attrs["NetId"] = pl.NetID
	res.Attrs["PrivateIp"] = pl.Address.String()
}

func stringOf(v any) string {
	s, _ := v.(string)
	return s
}
