package outscale

import (
	"context"
	"net/http"
	"net/netip"
	"strconv"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/network"
	"github.com/stephrobert/feint/internal/core/resource"
)

// A Net is Outscale's VPC and a Subnet is a range inside it, which is the AWS
// model rather than Scaleway's private networks plus a separate IPAM.
//
// The addressing is arithmetic, not decoration, and that is the whole point of
// serving these at all. Every other local cloud emulator accepts any CIDR, never
// checks the mask, never checks overlap, and reports a fixed address count
// whatever the prefix — so the addressing plan a client declares means nothing.
// Here a block is parsed, its mask bounded, its containment inside the Net
// checked, its overlap with siblings refused, and AvailableIpsCount computed
// from the mask.
//
// Shapes come from the OpenAPI document: Net, Subnet, CreateNetRequest,
// CreateSubnetRequest. The contract check validates every response against them,
// so a field invented here fails the run.

const (
	kindNet    = "net"
	kindSubnet = "subnet"
)

// The mask bounds Outscale accepts on a Net, from their documentation: between
// /16 and /28. A /8 would be a plan the emulator cannot back with a real bridge,
// and a /29 leaves no usable address once the four reserved ones are taken.
const (
	minNetBits    = 16
	maxNetBits    = 28
	minSubnetBits = 16
	maxSubnetBits = 29
)

// reservedPerSubnet is how many addresses Outscale keeps at the head of every
// subnet. Their own document spells it out, in the description of PrivateIps:
// an address "cannot be one of the first four IPs (ending in .0, .1, .2, .3) or
// the last IP (ending in .255) of the Subnet, as these are reserved by 3DS
// OUTSCALE". Four at the head, plus the broadcast, which the allocator excludes
// on its own — so five in total, exactly AWS's count.
//
// The arithmetic is checked against an example in that same document: a /18 is
// published with AvailableIpsCount 16379, and 16384 - 16379 = 5. A /24 therefore
// offers 251, not 252, and a client sizing its deployment on that number is
// right to.
const reservedPerSubnet = 4

type createNetRequest struct {
	IPRange string `json:"IpRange"`
	Tenancy string `json:"Tenancy"`
	DryRun  *bool  `json:"DryRun"`
}

type createSubnetRequest struct {
	IPRange       string `json:"IpRange"`
	NetID         string `json:"NetId"`
	SubregionName string `json:"SubregionName"`
	DryRun        *bool  `json:"DryRun"`
}

type readNetsRequest struct {
	Filters filterSet `json:"Filters"`
	DryRun  *bool     `json:"DryRun"`
}

// netFilters and subnetFilters are what these resources can answer from what is
// stored. Tags, DHCP option sets and subregions are not modelled, so they are
// refused rather than silently matched.
var (
	netFilters    = []string{"NetIds", "IpRanges", "States"}
	subnetFilters = []string{"SubnetIds", "NetIds", "IpRanges", "States"}
)

type readSubnetsRequest struct {
	Filters filterSet `json:"Filters"`
	DryRun  *bool     `json:"DryRun"`
}

type deleteNetRequest struct {
	NetID  string `json:"NetId"`
	DryRun *bool  `json:"DryRun"`
}

type deleteSubnetRequest struct {
	SubnetID string `json:"SubnetId"`
	DryRun   *bool  `json:"DryRun"`
}

func (p *Pack) createNet(w http.ResponseWriter, r *http.Request) {
	var req createNetRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}

	prefix, err := network.ParseCIDR(req.IPRange)
	if err != nil {
		p.badRequest(w, "IpRange: "+err.Error())
		return
	}
	if err := network.CheckMask(prefix, minNetBits, maxNetBits); err != nil {
		p.badRequest(w, "IpRange: "+err.Error())
		return
	}

	// Held from the check to the store, or two concurrent creates both find the
	// block free and both take it.
	p.addresses.Lock()
	defer p.addresses.Unlock()

	if other, clash := network.FirstOverlap(prefix, p.netPrefixes()); clash {
		p.conflict(w, "IpRange "+prefix.String()+" overlaps the Net on "+other.String())
		return
	}

	now := p.env.Now()
	res := &resource.Resource{
		ID:      newID("vpc", p.env.NewID()),
		Kind:    kindNet,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"IpRange": prefix.String(),
			"Tenancy": orDefault(req.Tenancy, "default"),
			"Tags":    []any{},
		},
	}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Net":             netView(res),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) readNets(w http.ResponseWriter, r *http.Request) {
	var req readNetsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, netFilters...) {
		return
	}

	nets := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindNet, resource.Tenant{Provider: Name}) {
		ipRange, _ := res.Attrs["IpRange"].(string)
		if !matchesStrings(req.Filters, "NetIds", res.ID) ||
			!matchesStrings(req.Filters, "IpRanges", ipRange) ||
			!matchesStrings(req.Filters, "States", res.State) {
			continue
		}
		nets = append(nets, netView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Nets":            nets,
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteNet(w http.ResponseWriter, r *http.Request) {
	var req deleteNetRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if _, found := p.env.Store.Get(Name, kindNet, req.NetID); !found {
		p.notFound(w, "Net", req.NetID)
		return
	}
	// A Net holding subnets does not go, which is what the real API answers and
	// what Terraform's destroy order relies on: it removes the subnets first and
	// would report our success as a cycle it did not need to break.
	//
	// Under the addressing lock, which is where a subnet is placed in a Net. The
	// guard was here and the lock was not, so the list was empty by construction
	// for the whole window a create was open: an audit deleted a Net and watched
	// a Subnet land in it, naming a Net the pack no longer serves, with its
	// bridge left on the host. The same reasoning that put deleteSubnet under
	// this lock applies here, and it was applied to only one of the two.
	//
	// TestANetDoesNotDeleteUnderASubnetBeingCreated fails without this.
	p.addresses.Lock()
	if subnets := p.subnetsOf(req.NetID); len(subnets) > 0 {
		p.addresses.Unlock()
		p.conflict(w, "the Net "+req.NetID+" still holds "+strconv.Itoa(len(subnets))+" subnet(s)")
		return
	}
	p.env.Store.Delete(Name, kindNet, req.NetID)
	p.addresses.Unlock()

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"ResponseContext": p.context(),
	})
}

func (p *Pack) createSubnet(w http.ResponseWriter, r *http.Request) {
	var req createSubnetRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}

	net, found := p.env.Store.Get(Name, kindNet, req.NetID)
	if !found {
		p.notFound(w, "Net", req.NetID)
		return
	}
	prefix, err := network.ParseCIDR(req.IPRange)
	if err != nil {
		p.badRequest(w, "IpRange: "+err.Error())
		return
	}
	if err := network.CheckMask(prefix, minSubnetBits, maxSubnetBits); err != nil {
		p.badRequest(w, "IpRange: "+err.Error())
		return
	}

	netRange, err := prefixOf(net, "IpRange")
	if err != nil {
		p.badRequest(w, "the Net "+req.NetID+" carries no usable range")
		return
	}
	if !network.Contains(netRange, prefix) {
		p.badRequest(w, "IpRange "+prefix.String()+" is outside the Net range "+netRange.String())
		return
	}

	// Reserved under the lock, created outside it. Holding the addressing plane
	// across `incus network create` — hundreds of milliseconds to seconds — put
	// every other CreateVms, CreateNet and CreateSubnet in a queue behind it: a
	// terraform apply making ten subnets serialised them completely, measured at
	// 1.5 s for one unrelated CreateNet. "Un effet de bord lent ne tient pas dans
	// le verrou" was applied to the Vm path and left violated here.
	//
	// The reservation is what the lock is for: the resource is stored before the
	// runtime call, so a concurrent create sees the range as taken and no two
	// subnets can claim it. If the runtime then refuses, the reservation is
	// withdrawn.
	//
	// TestSubnetCreateDoesNotHoldTheAddressingLockAcrossTheRuntime fails
	// without this.
	p.addresses.Lock()

	if other, clash := network.FirstOverlap(prefix, p.subnetPrefixes(req.NetID)); clash {
		p.addresses.Unlock()
		p.conflict(w, "IpRange "+prefix.String()+" overlaps the subnet on "+other.String())
		return
	}

	now := p.env.Now()
	res := &resource.Resource{
		ID:      newID("subnet", p.env.NewID()),
		Kind:    kindSubnet,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"IpRange":             prefix.String(),
			"NetId":               req.NetID,
			"SubregionName":       orDefault(req.SubregionName, subregionName),
			"MapPublicIpOnLaunch": false,
			"Tags":                []any{},
		},
	}

	// Stored before the runtime call and before the lock is released: this is the
	// reservation, and it is what makes a concurrent create see the range as
	// taken.
	p.env.Store.Put(res)
	p.addresses.Unlock()

	// The subnet is a real network on the runtime, which is what makes an
	// address published here an address a machine can carry. Without a runtime
	// this is a no-op and the control plane still answers.
	if err := p.ensureBackingNetwork(r.Context(), res, prefix); err != nil {
		// The reservation goes back, or a refused create would hold a range
		// nothing owns. Delete rather than Commit: there is nothing to keep.
		p.env.Store.Delete(Name, kindSubnet, res.ID)
		p.logger().Error("could not create the backing network",
			"subnet", res.ID, "range", prefix, "error", err)
		p.conflict(w, "the machine runtime could not provide a network for "+prefix.String()+
			"; the block is most likely already in use on this host: "+err.Error())
		return
	}
	// The runtime wrote the network name into Runtime, so the reservation is
	// updated rather than replaced: a Put would resurrect a subnet deleted while
	// the network was being created.
	if !p.env.Store.Commit(res, p.env.Now()) {
		// Deleted while its network was being made. Take the network back down
		// rather than leave it on the host.
		p.removeBackingNetwork(r.Context(), res)
		p.notFound(w, "Subnet", res.ID)
		return
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Subnet":          p.subnetView(res),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) readSubnets(w http.ResponseWriter, r *http.Request) {
	var req readSubnetsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, subnetFilters...) {
		return
	}

	subnets := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindSubnet, resource.Tenant{Provider: Name}) {
		netID, _ := res.Attrs["NetId"].(string)
		ipRange, _ := res.Attrs["IpRange"].(string)
		if !matchesStrings(req.Filters, "SubnetIds", res.ID) ||
			!matchesStrings(req.Filters, "NetIds", netID) ||
			!matchesStrings(req.Filters, "IpRanges", ipRange) ||
			!matchesStrings(req.Filters, "States", res.State) {
			continue
		}
		subnets = append(subnets, p.subnetView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Subnets":         subnets,
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteSubnet(w http.ResponseWriter, r *http.Request) {
	var req deleteSubnetRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	subnet, found := p.env.Store.Get(Name, kindSubnet, req.SubnetID)
	if !found {
		p.notFound(w, "Subnet", req.SubnetID)
		return
	}

	// A Subnet does not vanish under the machines placed in it. The Net path
	// above has always refused while subnets remained, and this one checked
	// nothing: an audit deleted a subnet under a running Vm, then its Net, and
	// ReadVms went on naming both. With a runtime it tore down the backing
	// network under attached machines — and the destructive paths are the ones
	// this repository's own instructions say to guard first.
	//
	// Under the addressing lock, which is where allocateVms places a Vm: without
	// it the guard reads an empty list while a create is between its placement
	// and its Put, and the Subnet goes out under a machine that lands a
	// microsecond later. The first version checked outside the lock, which made
	// the guard true only when nothing else was running.
	//
	// TestASubnetDoesNotDeleteUnderAVm fails without the guard.
	// TestASubnetDoesNotDeleteUnderARace holds the invariant under sixty racing
	// rounds, and — measured, not assumed — it still passes when the lock is
	// removed: the window between the List and the Delete is a few microseconds
	// and nothing in the test can widen it. So the lock is argued from the
	// allocation path, which does hold it, and the race test is a regression net,
	// not the proof. Saying which is which is the point.
	p.addresses.Lock()
	for _, vm := range p.env.Store.List(kindVM, resource.Tenant{Provider: Name}) {
		if stringOf(vm.Attrs["SubnetId"]) == req.SubnetID {
			p.addresses.Unlock()
			p.conflict(w, "the Subnet "+req.SubnetID+" still holds "+vm.ID)
			return
		}
	}
	// Removed from the store under the lock, so a create that wakes up next
	// fails to resolve it rather than placing a Vm in a Subnet being deleted.
	p.env.Store.Delete(Name, kindSubnet, req.SubnetID)
	p.addresses.Unlock()

	// The runtime call is slow and stays outside: the Subnet is already
	// unreachable to every other handler by this point.
	p.removeBackingNetwork(r.Context(), subnet)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// ---- Views and plumbing ------------------------------------------------------

func netView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+2)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["NetId"] = res.ID
	out["State"] = res.State
	return out
}

// subnetView computes AvailableIpsCount rather than storing it: it is a fact
// about the mask and what is allocated, and a stored copy goes stale the moment
// an address is taken.
func (p *Pack) subnetView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+3)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["SubnetId"] = res.ID
	out["State"] = res.State

	// Computed from the mask *and* from what is allocated, which is what the
	// comment on this view has always claimed and what it did not do: a fresh
	// allocator was built and nothing reserved, so a /24 with three machines in
	// it still answered 251. The conformance suite only ever read empty subnets,
	// so it proved the mask half and the comment asserted the rest.
	//
	// A stored count would go stale the moment an address is taken; this one
	// cannot, because it is derived on every read.
	//
	// TestAvailableIpsCountFollowsTheMachines fails without the reservation loop.
	if prefix, err := prefixOf(res, "IpRange"); err == nil {
		if allocator, err := network.NewAllocator(prefix, reservedPerSubnet); err == nil {
			for _, vm := range p.env.Store.List(kindVM, resource.Tenant{Provider: Name}) {
				if stringOf(vm.Attrs["SubnetId"]) != res.ID {
					continue
				}
				if taken, parseErr := netip.ParseAddr(stringOf(vm.Attrs["PrivateIp"])); parseErr == nil {
					_ = allocator.Reserve(taken)
				}
			}
			out["AvailableIpsCount"] = allocator.Available()
		}
	}
	return out
}

// prefixOf reads a block off a resource.
func prefixOf(res *resource.Resource, attr string) (netip.Prefix, error) {
	raw, _ := res.Attrs[attr].(string)
	return network.ParseCIDR(raw)
}

func (p *Pack) netPrefixes() []netip.Prefix {
	out := make([]netip.Prefix, 0, 4)
	for _, res := range p.env.Store.List(kindNet, resource.Tenant{Provider: Name}) {
		if prefix, err := prefixOf(res, "IpRange"); err == nil {
			out = append(out, prefix)
		}
	}
	return out
}

func (p *Pack) subnetPrefixes(netID string) []netip.Prefix {
	out := make([]netip.Prefix, 0, 4)
	for _, res := range p.subnetsOf(netID) {
		if prefix, err := prefixOf(res, "IpRange"); err == nil {
			out = append(out, prefix)
		}
	}
	return out
}

func (p *Pack) subnetsOf(netID string) []*resource.Resource {
	out := make([]*resource.Resource, 0, 4)
	for _, res := range p.env.Store.List(kindSubnet, resource.Tenant{Provider: Name}) {
		if id, _ := res.Attrs["NetId"].(string); id == netID {
			out = append(out, res)
		}
	}
	return out
}

// runtimeNetworkKey is the runtime network backing a subnet, kept out of Attrs.
const runtimeNetworkKey = "network"

func (p *Pack) ensureBackingNetwork(ctx context.Context, res *resource.Resource, prefix netip.Prefix) error {
	if p.env.Machines == nil {
		return nil
	}
	name := machine.NetworkName(machine.NetworkPrefix, res.ID)
	// NAT is deliberately left off. A Subnet reaches the internet only once a
	// client attaches an internet service or a NAT service, which is Outscale's
	// model and AWS's before it. Switching NAT on here would make every emulated
	// Subnet routable and quietly turn a plan that should fail into one that
	// works — the emulator would be more permissive than the cloud it imitates,
	// which is the one direction a test environment must never take.
	if err := p.env.Machines.EnsureNetwork(ctx, machine.NetworkSpec{
		Name:   name,
		CIDR:   prefix.String(),
		Labels: map[string]string{machine.LabelKey: Name, "feint.subnet": res.ID},
	}); err != nil {
		return err
	}
	if res.Runtime == nil {
		res.Runtime = map[string]string{}
	}
	res.Runtime[runtimeNetworkKey] = name
	return nil
}

func (p *Pack) removeBackingNetwork(ctx context.Context, res *resource.Resource) {
	if p.env.Machines == nil || res.Runtime[runtimeNetworkKey] == "" {
		return
	}
	if err := p.env.Machines.RemoveNetwork(ctx, res.Runtime[runtimeNetworkKey]); err != nil {
		p.logger().Error("could not remove the backing network",
			"subnet", res.ID, "network", res.Runtime[runtimeNetworkKey], "error", err)
	}
}
