package outscale

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
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
	p.addresses.Lock()
	defer p.addresses.Unlock()

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

	// Written by Commit rather than Put: the NIC may have been deleted while the
	// addresses were being computed, and Put would resurrect it.
	added := secondaryAddresses(nic)
	for _, address := range wanted {
		if !contains(added, address) {
			added = append(added, address)
		}
	}
	setSecondaryAddresses(nic, added)
	if !p.env.Store.Commit(nic, p.env.Now()) {
		p.notFound(w, "network interface", req.NicID)
		return
	}
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

	p.addresses.Lock()
	defer p.addresses.Unlock()

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

	kept := make([]string, 0, len(held))
	for _, address := range held {
		if !contains(req.PrivateIps, address) {
			kept = append(kept, address)
		}
	}
	setSecondaryAddresses(nic, kept)
	if !p.env.Store.Commit(nic, p.env.Now()) {
		p.notFound(w, "network interface", req.NicID)
		return
	}
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
		if vm.State == stateTerminated || stringOf(vm.Attrs["SubnetId"]) != subnetID {
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
func setSecondaryAddresses(nic *resource.Resource, addresses []string) {
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
			"PrivateDnsName": privateDNSName(address),
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
// when no runtime is configured, which is the default and what CI uses. And the
// call is made outside the store lock for the reason every runtime call in this
// pack is — launching or reconfiguring a machine takes seconds where the lock
// takes microseconds.
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
