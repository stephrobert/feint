package outscale

import (
	"errors"
	"net/netip"

	"github.com/stephrobert/feint/internal/core/network"
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
// and what it hands out cannot disagree. Addresses already taken by other Vms in
// the same Subnet are reserved first — the allocator is rebuilt per call because
// the store is the state, and holding a long-lived allocator beside it is how
// the two drift apart across a snapshot restore.
func (p *Pack) placeInSubnet(subnetID string) (placement, error) {
	subnet, found := p.env.Store.Get(Name, kindSubnet, subnetID)
	if !found {
		return placement{}, errUnknownSubnet
	}
	prefix, err := prefixOf(subnet, "IpRange")
	if err != nil {
		return placement{}, err
	}

	allocator, err := network.NewAllocator(prefix, reservedPerSubnet)
	if err != nil {
		return placement{}, err
	}
	for _, vm := range p.env.Store.List(kindVM, resource.Tenant{Provider: Name}) {
		if vm.Attrs["SubnetId"] != subnetID {
			continue
		}
		taken, parseErr := netip.ParseAddr(stringOf(vm.Attrs["PrivateIp"]))
		if parseErr == nil {
			// A duplicate here is not worth failing a create over: the address
			// is already out of the pool, which is the only thing that matters.
			_ = allocator.Reserve(taken)
		}
	}
	// Secondary interfaces hold an address in the same Subnet, so they are
	// reserved too: without this a created NIC and a Vm could be handed the
	// same address, which on a runtime is two interfaces fighting for one IP.
	for _, nic := range p.env.Store.List(kindNic, resource.Tenant{Provider: Name}) {
		if nic.Attrs["SubnetId"] != subnetID {
			continue
		}
		if taken, parseErr := netip.ParseAddr(stringOf(nic.Attrs["PrivateIp"])); parseErr == nil {
			_ = allocator.Reserve(taken)
		}
	}

	address, err := allocator.Allocate()
	if err != nil {
		return placement{}, err
	}
	netID := stringOf(subnet.Attrs["NetId"])
	return placement{
		SubnetID:  subnetID,
		NetID:     netID,
		Address:   address,
		PrefixLen: prefix.Bits(),
		Network:   subnet.Runtime[runtimeNetworkKey],
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
