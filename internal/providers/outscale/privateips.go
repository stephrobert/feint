package outscale

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"slices"
	"sort"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/network"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Secondary private addresses on a NIC (#172).
//
// The last two Outscale operations without a decision, and they were left
// undecided on purpose rather than declined: a NIC carrying several private
// addresses is ordinary, implementable, and refusing it would have written "out
// of scope" where the truth was "not yet" — two things this repository's third
// rule says are not the same.
//
// Shapes read from the SDK's own api.yaml, not from the web documentation:
//
//	LinkPrivateIpsRequest    NicId (required), PrivateIps, SecondaryPrivateIpCount,
//	                         AllowRelink, DryRun
//	UnlinkPrivateIpsRequest  NicId and PrivateIps, both required
//
// and the reservation rule with them: an address "cannot be one of the first
// four IPs (ending in .0, .1, .2, .3) or the last IP of the Subnet, as these are
// reserved by 3DS OUTSCALE". The allocator already applies exactly that through
// reservedPerSubnet, which is why the count form below draws from it rather than
// counting by hand.

// linkPrivateIps assigns secondary addresses to a NIC, named or counted.
//
// TestSecondaryAddressesAreAllocatedAndPublished fails without this.
func (p *Pack) linkPrivateIps(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NicID                   string   `json:"NicId"`
		PrivateIps              []string `json:"PrivateIps"`
		SecondaryPrivateIPCount *int     `json:"SecondaryPrivateIpCount"`
		AllowRelink             *bool    `json:"AllowRelink"`
		DryRun                  *bool    `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.NicID == "" {
		p.badRequest(w, "NicId is required")
		return
	}
	if len(req.PrivateIps) == 0 && req.SecondaryPrivateIPCount == nil {
		p.badRequest(w, "either PrivateIps or SecondaryPrivateIpCount is required")
		return
	}
	if len(req.PrivateIps) > 0 && req.SecondaryPrivateIPCount != nil {
		p.badRequest(w, "PrivateIps and SecondaryPrivateIpCount are mutually exclusive")
		return
	}

	// The allocation and the store write happen under one lock, for the reason
	// placeInSubnet exists: two concurrent links must not be handed the same
	// address, which on a runtime is two interfaces fighting for one IP.
	unlock := p.lockAddresses()
	defer unlock()

	nic, found := p.env.Store.Get(Name, kindNic, req.NicID)
	if !found {
		p.notFound(w, "network interface", req.NicID)
		return
	}
	subnetID := stringOf(nic.Attrs["SubnetId"])
	allocator, err := p.subnetAllocator(subnetID)
	if err != nil {
		p.badRequest(w, err.Error())
		return
	}

	wanted := req.PrivateIps
	if req.SecondaryPrivateIPCount != nil {
		count := *req.SecondaryPrivateIPCount
		if count < 1 {
			p.badRequest(w, "SecondaryPrivateIpCount must be 1 or greater")
			return
		}
		for range count {
			address, allocErr := allocator.Allocate()
			if allocErr != nil {
				p.conflict(w, "the Subnet "+subnetID+" has no address left: "+allocErr.Error())
				return
			}
			wanted = append(wanted, address.String())
		}
	} else {
		// A named address must be inside the Subnet and not already taken —
		// unless AllowRelink says otherwise, which the SDK documents as moving
		// it from whichever NIC holds it.
		for _, raw := range wanted {
			address, parseErr := netip.ParseAddr(raw)
			if parseErr != nil {
				p.badRequest(w, "not an address: "+raw)
				return
			}
			if !allocator.Prefix().Contains(address) {
				p.badRequest(w, raw+" is outside the Subnet's range "+allocator.Prefix().String())
				return
			}
			if err := allocator.Reserve(address); err != nil && !boolOr(req.AllowRelink, false) {
				p.conflict(w, raw+" is already assigned in this Subnet; pass AllowRelink to move it")
				return
			}
		}
	}

	// The merge runs under the store lock, on the stored address list rather
	// than on the clone resolved above: merged into the clone and committed
	// wholesale, this erased a concurrent write to another field of the same
	// NIC — its tags, its description — after their 200 (#295). Update also
	// keeps a NIC deleted while the addresses were being computed deleted,
	// which is what Commit was here for.
	var updated *resource.Resource
	err = p.env.Store.Update(Name, kindNic, req.NicID, func(stored *resource.Resource) error {
		added := secondaryAddresses(stored)
		for _, address := range wanted {
			if !contains(added, address) {
				added = append(added, address)
			}
		}
		p.setSecondaryAddresses(stored, added)
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		p.notFound(w, "network interface", req.NicID)
		return
	}
	nic = updated

	// Released before the runtime is touched, and this line is the fix for a
	// comment that described the opposite of what the code did (#216).
	// carrySecondary reconfigures an interface on a live machine, which takes
	// seconds; the lock it was holding is the one every address allocation in this
	// pack needs, so one link blocked the whole pack for the length of an
	// incus exec. The release is sync.Once-guarded, so the defer above is still
	// correct on every error path.
	//
	// TestAnAddressLinkDoesNotHoldTheAddressLockAcrossTheRuntime fails without it.
	unlock()
	p.carrySecondary(r.Context(), nic)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// unlinkPrivateIps removes secondary addresses from a NIC. The primary is not
// one of them: it belongs to the interface and goes with it.
//
// TestThePrimaryAddressIsNeverUnlinked fails without the guard.
func (p *Pack) unlinkPrivateIps(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NicID      string   `json:"NicId"`
		PrivateIps []string `json:"PrivateIps"`
		DryRun     *bool    `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.NicID == "" || len(req.PrivateIps) == 0 {
		p.badRequest(w, "NicId and PrivateIps are both required")
		return
	}

	unlock := p.lockAddresses()
	defer unlock()

	nic, found := p.env.Store.Get(Name, kindNic, req.NicID)
	if !found {
		p.notFound(w, "network interface", req.NicID)
		return
	}
	primary := stringOf(nic.Attrs["PrivateIp"])
	held := secondaryAddresses(nic)
	for _, address := range req.PrivateIps {
		if address == primary {
			p.conflict(w, address+" is the primary address of "+nic.ID+" and cannot be unlinked")
			return
		}
		if !contains(held, address) {
			p.badRequest(w, address+" is not assigned to "+nic.ID)
			return
		}
	}

	// The removal recomputes from the stored list, under the lock, same reason
	// as linkPrivateIps (#295).
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindNic, req.NicID, func(stored *resource.Resource) error {
		current := secondaryAddresses(stored)
		kept := make([]string, 0, len(current))
		for _, address := range current {
			if !contains(req.PrivateIps, address) {
				kept = append(kept, address)
			}
		}
		p.setSecondaryAddresses(stored, kept)
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		p.notFound(w, "network interface", req.NicID)
		return
	}
	nic = updated
	// Same release as the link path, and for the same reason.
	unlock()
	p.carrySecondary(r.Context(), nic)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// subnetAllocator answers an allocator for a Subnet with every address this
// account already holds in it reserved — machines, primary NIC addresses, and
// the secondary ones this file adds.
//
// The last of the three is the one that had to be added: without it a Vm could
// be handed an address already linked to a NIC, which on a runtime is two
// interfaces fighting for one IP. placeInSubnet's own comment warned about
// exactly that for the primary case.
func (p *Pack) subnetAllocator(subnetID string) (*network.Allocator, error) {
	subnet, found := p.env.Store.Get(Name, kindSubnet, subnetID)
	if !found {
		return nil, fmt.Errorf("the Subnet %s does not exist", subnetID)
	}
	prefix, err := prefixOf(subnet, "IpRange")
	if err != nil {
		return nil, err
	}
	allocator, err := network.NewAllocator(prefix, reservedPerSubnet)
	if err != nil {
		return nil, err
	}
	for _, vm := range p.env.Store.List(kindVM, resource.Tenant{Provider: Name}) {
		// Gone, not a state comparison: the same answer the sweep invariant
		// reads, so what the invariant excuses and what this pool reuses
		// cannot disagree.
		if Gone(vm) || stringOf(vm.Attrs["SubnetId"]) != subnetID {
			continue
		}
		if taken, parseErr := netip.ParseAddr(stringOf(vm.Attrs["PrivateIp"])); parseErr == nil {
			_ = allocator.Reserve(taken)
		}
	}
	for _, nic := range p.env.Store.List(kindNic, resource.Tenant{Provider: Name}) {
		if stringOf(nic.Attrs["SubnetId"]) != subnetID {
			continue
		}
		if taken, parseErr := netip.ParseAddr(stringOf(nic.Attrs["PrivateIp"])); parseErr == nil {
			_ = allocator.Reserve(taken)
		}
		for _, address := range secondaryAddresses(nic) {
			if taken, parseErr := netip.ParseAddr(address); parseErr == nil {
				_ = allocator.Reserve(taken)
			}
		}
	}
	// A load balancer holds a primary private IP in its subnet, from this same
	// pool ("The primary private IP of the load balancer", the SDK's own
	// field); not reserving it here would hand the address to the next Vm.
	for _, lb := range p.env.Store.List(kindLoadBalancer, resource.Tenant{Provider: Name}) {
		if !slices.Contains(stringsOf(lb.Attrs["Subnets"]), subnetID) {
			continue
		}
		if taken, parseErr := netip.ParseAddr(stringOf(lb.Attrs["PrivateIp"])); parseErr == nil {
			_ = allocator.Reserve(taken)
		}
	}
	return allocator, nil
}

// secondaryAddresses reads the non-primary addresses out of the NIC's published
// PrivateIps list, which is where they live: the API publishes one list with a
// flag rather than two fields, so the store holds one list too.
func secondaryAddresses(nic *resource.Resource) []string {
	entries, _ := nic.Attrs["PrivateIps"].([]any)
	out := make([]string, 0, len(entries))
	for _, raw := range entries {
		entry, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		if primary, _ := entry["IsPrimary"].(bool); primary {
			continue
		}
		if address := stringOf(entry["PrivateIp"]); address != "" {
			out = append(out, address)
		}
	}
	sort.Strings(out)
	return out
}

// setSecondaryAddresses rewrites the list, keeping the primary entry exactly as
// it was: it carries a DNS name the client reads back, and rebuilding it would
// change a field nobody asked to change.
func (p *Pack) setSecondaryAddresses(nic *resource.Resource, addresses []string) {
	entries, _ := nic.Attrs["PrivateIps"].([]any)
	rebuilt := make([]any, 0, len(addresses)+1)
	for _, raw := range entries {
		if entry, ok := raw.(map[string]any); ok {
			if primary, _ := entry["IsPrimary"].(bool); primary {
				rebuilt = append(rebuilt, entry)
			}
		}
	}
	sort.Strings(addresses)
	for _, address := range addresses {
		rebuilt = append(rebuilt, map[string]any{
			"IsPrimary":      false,
			"PrivateDnsName": p.privateDNSName(address),
			"PrivateIp":      address,
		})
	}
	nic.Attrs["PrivateIps"] = rebuilt
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// carrySecondary tells the runtime what the NIC now carries.
//
// Best effort and logged, never fatal: the control plane must keep answering
// when no runtime is configured, which is the default and what CI uses.
//
// Its callers release the pack's address lock before calling it, and that had to
// be made true rather than merely written: this comment used to claim the call
// ran outside the lock while both callers held it across the whole handler
// (#216). Reconfiguring an interface takes seconds, the lock is the one every
// address allocation in this pack needs, so one link serialised the pack behind
// one incus exec — the shape CLAUDE.md documents under *un effet de bord lent ne
// tient pas dans le verrou*, written into the very function that denied it.
func (p *Pack) carrySecondary(ctx context.Context, nic *resource.Resource) {
	if p.env.Machines == nil {
		return
	}
	vmID := stringOf(nic.Attrs["LinkVmId"])
	networkName := ""
	if subnet, found := p.env.Store.Get(Name, kindSubnet, stringOf(nic.Attrs["SubnetId"])); found {
		networkName = subnet.Runtime[runtimeNetworkKey]
	}
	if vmID == "" || networkName == "" {
		// Nothing is attached yet, so there is no interface to configure. The
		// addresses are stored and published all the same, and they land when
		// the NIC is linked to a machine.
		return
	}
	vm, found := p.env.Store.Get(Name, kindVM, vmID)
	if !found {
		return
	}
	machineName := p.binding().Name(vm.ID)
	prefixLen := 0
	if subnet, ok := p.env.Store.Get(Name, kindSubnet, stringOf(nic.Attrs["SubnetId"])); ok {
		if prefix, err := prefixOf(subnet, "IpRange"); err == nil {
			prefixLen = prefix.Bits()
		}
	}
	att := machine.Attachment{
		Network:   networkName,
		Address:   stringOf(nic.Attrs["PrivateIp"]),
		PrefixLen: prefixLen,
		Secondary: secondaryAddresses(nic),
	}
	if err := p.env.Machines.Attach(ctx, machineName, att); err != nil {
		p.logger().Error("could not carry the secondary addresses",
			"nic", nic.ID, "machine", machineName, "error", err)
	}
}
