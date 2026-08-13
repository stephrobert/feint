package scaleway

import (
	"context"
	"net/http"
	"net/netip"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// A private NIC attaches a server to a Private Network, and it is where the
// whole chain finally meets: the block validated by the VPC product, the address
// handed out by the allocator, and the interface the machine driver puts on the
// backing network.
//
// That chain is the point of the emulator. Every local cloud emulator can answer
// "here is your NIC, here is its address"; the address then belongs to no
// declared range and the machine never carries it. Here the address comes from
// the Private Network's own block, is reserved so nothing else receives it, and
// is the address the machine answers on.
//
// Shapes come from the SDK (api/instance/v1/instance_sdk.go): PrivateNIC, and
// the List/Get/Create envelopes. The link to the server lives in Runtime, since
// the wire shape carries server_id as a plain field the view rebuilds.

const kindPrivateNIC = "instance/private-nic"

// runtimePrivateNetworkKey links a NIC to the Private Network it sits on.
const runtimePrivateNetworkKey = "private_network"

type createPrivateNICRequest struct {
	PrivateNetworkID string   `json:"private_network_id"`
	Tags             []string `json:"tags"`
	// Two spellings of one field: ip_ids is the SDK's deprecated name,
	// ipam_ip_ids the one the Terraform provider sends today. Both carry IPAM
	// ids of addresses booked beforehand.
	IPIDs     []string `json:"ip_ids"`
	IpamIPIDs []string `json:"ipam_ip_ids"`
}

// bookedIPIDs returns the booked addresses a create names, under the SDK's two
// spellings, the current one winning.
func (req createPrivateNICRequest) bookedIPIDs() []string {
	if len(req.IpamIPIDs) > 0 {
		return req.IpamIPIDs
	}
	return req.IPIDs
}

func (p *Pack) listPrivateNICs(w http.ResponseWriter, r *http.Request) {
	server, ok := p.serverOf(w, r)
	if !ok {
		return
	}

	all := p.privateNICsOf(server.ID)
	page := parsePage(r)
	start, end := page.slice(len(all))
	nics := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		nics = append(nics, p.privateNICView(res))
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"private_nics": nics,
		"total_count":  len(all),
	})
}

func (p *Pack) getPrivateNIC(w http.ResponseWriter, r *http.Request) {
	res, ok := p.privateNICOf(w, r)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"private_nic": p.privateNICView(res)})
}

func (p *Pack) createPrivateNIC(w http.ResponseWriter, r *http.Request) {
	server, ok := p.serverOf(w, r)
	if !ok {
		return
	}

	var req createPrivateNICRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.PrivateNetworkID == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "private_network_id", Reason: "required"})
		return
	}
	pn, found := p.env.Store.Get(Name, kindPrivateNetwork, req.PrivateNetworkID)
	if !found {
		writeNotFound(w, "private_network", req.PrivateNetworkID)
		return
	}
	// One NIC per network per server, which is what the API enforces: a second
	// one would receive a second address on the same bridge and the control
	// plane could not say which one the machine answers on.
	for _, existing := range p.privateNICsOf(server.ID) {
		if existing.Runtime[runtimePrivateNetworkKey] == pn.ID {
			writePrecondition(w, "private_nic", existing.ID,
				"the server is already attached to this private network")
			return
		}
	}

	// Held across rebuild, allocate and persist: releasing it earlier would let
	// a concurrent request rebuild from a store that does not yet know about
	// this address, and hand out the same one. The booked path holds it too:
	// checking an address is unattached and attaching it is the same
	// read-modify-write.
	p.addresses.Lock()
	defer p.addresses.Unlock()

	// The client either names addresses it booked through ipam/v1, or receives
	// one from the network's own block. Same pool both ways: the allocator is
	// rebuilt from every IPAM address of the network, booked included.
	booked, ok := p.bookedIPsOf(w, req.bookedIPIDs(), pn, server)
	if !ok {
		return
	}

	var address netip.Addr
	if len(booked) > 0 {
		// Comma-ok on purpose: an address attr comes back verbatim from a
		// restored snapshot, and a bare assertion would let a crafted one panic
		// the process.
		raw, _ := booked[0].Attrs["address"].(string)
		prefix, err := netip.ParsePrefix(raw)
		if err != nil {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "ipam_ip_ids", Reason: "constraint", HelpMessage: err.Error(),
			})
			return
		}
		address = prefix.Addr()
	} else {
		alloc, err := p.allocatorFor(pn)
		if err != nil {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "private_network_id",
				Reason:       "constraint",
				HelpMessage:  err.Error(),
			})
			return
		}
		address, err = alloc.Allocate()
		if err != nil {
			// An exhausted block is a real answer, not an internal error: the client
			// asked for one more address than the subnet holds.
			writePrecondition(w, "private_network", pn.ID,
				"no address left in "+alloc.Prefix().String())
			return
		}
	}

	now := p.env.Now()
	res := &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindPrivateNIC,
		Tenant:  server.Tenant,
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"private_network_id": pn.ID,
			"mac_address":        macAddressOf(address),
			"tags":               orEmpty(req.Tags),
		},
		Runtime: map[string]string{
			runtimeServerKey:         server.ID,
			runtimePrivateNetworkKey: pn.ID,
		},
	}
	p.env.Store.Put(res)

	if len(booked) > 0 {
		// The booked addresses become this NIC's: same linkage as an allocated
		// one, so the view, the machine driver and a later delete read one model.
		for _, ip := range booked {
			if ip.Runtime == nil {
				ip.Runtime = map[string]string{}
			}
			ip.Runtime[runtimeNICKey] = res.ID
			ip.Attrs["mac_address"] = res.Attrs["mac_address"]
			ip.Attrs["zone"] = server.Tenant.Zone
			ip.Updated = now
			p.env.Store.Put(ip)
		}
	} else {
		// The address becomes an IPAM resource, because that is where the API
		// keeps it: the NIC only names it through ipam_ip_ids. The mask is the
		// network's own; allocatorFor just parsed the same block, so this
		// cannot fail here.
		prefix, err := prefixOf(pn)
		if err != nil {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "private_network_id", Reason: "constraint", HelpMessage: err.Error(),
			})
			return
		}
		p.env.Store.Put(p.newIPAMIP(regionOfZone(server.Tenant.Zone), server.Tenant.Project,
			netip.PrefixFrom(address, prefix.Bits()), res, pn))
	}

	// server.private_ip is deliberately not set: the SDK marks it deprecated and
	// "always null when routed_ip_enabled is True", which every server here is.
	// A client reads the address through this NIC and ipam/v1.
	server.Updated = p.env.Now()
	p.env.Store.Put(server)

	// The machine follows the control plane, not the other way round: the
	// address published here is the one it must carry. A running machine has to
	// be restarted to pick up a new interface, which is also true upstream.
	//
	// When the runtime refuses, the NIC says so instead of the failure living
	// only in a log line. PrivateNICState declares syncing_error for exactly
	// this, so nothing is invented. It was measured under --vm incus-vm, where
	// Incus cannot hot-plug a NIC into a running virtual machine ("PCI: slot 0
	// function 0 not available for virtio-net-pci"): the attachment failed, the
	// pack logged it, and the API went on publishing an address the machine did
	// not carry — the one thing this project exists to avoid.
	//
	// TestARefusedAttachmentIsVisibleOnTheNIC fails without this.
	if err := p.attachMachineToNetwork(r.Context(), server, pn, address); err != nil {
		res.State = "syncing_error"
		p.env.Store.Put(res)
	}

	// The rule set binds to interfaces, so a NIC created after the server was
	// powered on carries none until this runs. Without it the security group
	// applies to the first interface and not to the private one, which is
	// exactly the interface the group is about.
	p.applyServerFirewall(r.Context(), server)

	emulator.WriteJSON(w, http.StatusCreated, map[string]any{"private_nic": p.privateNICView(res)})
}

// bookedIPsOf resolves the IPAM addresses a NIC create names, and refuses the
// ones a create cannot take: an address of another network, another region, or
// one something already holds. Resolved under p.addresses, which the caller
// holds — the unattached check and the attachment must be one critical section.
func (p *Pack) bookedIPsOf(w http.ResponseWriter, ids []string, pn, server *resource.Resource) ([]*resource.Resource, bool) {
	booked := make([]*resource.Resource, 0, len(ids))
	for _, id := range ids {
		ip, found := p.env.Store.Get(Name, kindIPAMIP, id)
		if !found || ip.Tenant.Zone != regionOfZone(server.Tenant.Zone) {
			writeNotFound(w, "ip", id)
			return nil, false
		}
		if ip.Attrs["private_network_id"] != pn.ID {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "ipam_ip_ids",
				Reason:       "constraint",
				HelpMessage:  "IP " + id + " does not belong to private network " + pn.ID,
			})
			return nil, false
		}
		if ipamAttached(ip) {
			writePrecondition(w, "ip", id, "IP is already attached to a resource")
			return nil, false
		}
		booked = append(booked, ip)
	}
	return booked, true
}

func (p *Pack) deletePrivateNIC(w http.ResponseWriter, r *http.Request) {
	res, ok := p.privateNICOf(w, r)
	if !ok {
		return
	}
	// An allocated address goes back to the pool by disappearing from the
	// store: the allocator is rebuilt from what IPAM holds, so nothing to
	// release here. A booked one is the client's — it was reserved through
	// BookIP and is released through ReleaseIP — so it is detached and kept,
	// which is what the Terraform destroy order depends on: the NIC goes first,
	// the scaleway_ipam_ip after it. TestABookedAddressSurvivesItsNIC fails
	// without this.
	now := p.env.Now()
	for _, ip := range p.ipamIPsOf(res.ID) {
		if isBooked, _ := ip.Attrs[attrBooked].(bool); isBooked {
			delete(ip.Runtime, runtimeNICKey)
			delete(ip.Attrs, "mac_address")
			delete(ip.Attrs, "zone")
			ip.Updated = now
			p.env.Store.Put(ip)
			continue
		}
		p.env.Store.Delete(Name, kindIPAMIP, ip.ID)
	}
	p.env.Store.Delete(Name, kindPrivateNIC, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

// nicsOnNetwork lists the NICs sitting on a Private Network. The VPC product
// reads it to rebuild an allocator and to refuse deleting a network in use.
func (p *Pack) nicsOnNetwork(privateNetworkID string) []*resource.Resource {
	all := p.env.Store.List(kindPrivateNIC, resource.Tenant{Provider: Name})
	out := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if res.Runtime[runtimePrivateNetworkKey] == privateNetworkID {
			out = append(out, res)
		}
	}
	return out
}

func (p *Pack) privateNICsOf(serverID string) []*resource.Resource {
	all := p.env.Store.List(kindPrivateNIC, resource.Tenant{Provider: Name})
	out := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if res.Runtime[runtimeServerKey] == serverID {
			out = append(out, res)
		}
	}
	return out
}

func (p *Pack) privateNICOf(w http.ResponseWriter, r *http.Request) (*resource.Resource, bool) {
	server, ok := p.serverOf(w, r)
	if !ok {
		return nil, false
	}
	id := r.PathValue("nicID")
	res, found := p.env.Store.Get(Name, kindPrivateNIC, id)
	// A NIC read through the wrong server is not found, not someone else's NIC.
	if !found || res.Runtime[runtimeServerKey] != server.ID {
		writeNotFound(w, "private_nic", id)
		return nil, false
	}
	return res, true
}

// macAddressOf derives a stable MAC from the address the NIC carries. Stable
// because a client stores it, and locally administered (the 0x02 bit) so it
// cannot collide with a real vendor prefix.
func macAddressOf(addr netip.Addr) string {
	b := addr.As4()
	return "02:00:" +
		hexPair(b[0]) + ":" + hexPair(b[1]) + ":" + hexPair(b[2]) + ":" + hexPair(b[3])
}

func hexPair(b byte) string {
	const digits = "0123456789abcdef"
	return string([]byte{digits[b>>4], digits[b&0x0f]})
}

// attachMachineToNetwork puts the backing machine on the backing network at the
// address the control plane just published.
//
// Everything here degrades quietly, as elsewhere in the pack: with no runtime,
// or with one that refuses, the control plane still answers and the NIC still
// exists. The log is then the only way to learn why nothing is attached.
func (p *Pack) attachMachineToNetwork(ctx context.Context, server, pn *resource.Resource, address netip.Addr) error {
	// No runtime, or nothing started yet: there is no machine to refuse, so the
	// NIC is not in error. The address is applied when the machine boots.
	if p.env.Machines == nil {
		return nil
	}
	networkName := pn.Runtime[runtimeNetworkKey]
	machineName := server.Runtime[runtimeMachineKey]
	if networkName == "" || machineName == "" {
		return nil
	}
	// The mask travels with the address: reserving it on the bridge is not the
	// same as the guest carrying it, and configuring the interface needs both.
	prefix, err := prefixOf(pn)
	if err != nil {
		p.logger().Error("private network has no usable subnet",
			"private_network", pn.ID, "error", err)
		return err
	}
	if err := p.env.Machines.Attach(ctx, machineName, machine.Attachment{
		Network:   networkName,
		Address:   address.String(),
		PrefixLen: prefix.Bits(),
	}); err != nil {
		p.logger().Error("could not attach the machine to the private network",
			"server", server.ID, "private_network", pn.ID, "address", address, "error", err)
		return err
	}
	return nil
}

// privateNICView is the wire shape, and it carries no address on purpose:
// instance/v1.PrivateNIC has none. A client resolves the address by following
// ipam_ip_ids into ipam/v1, which is what the Terraform provider does.
func (p *Pack) privateNICView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":            res.ID,
		"server_id":     res.Runtime[runtimeServerKey],
		"state":         res.State,
		"zone":          res.Tenant.Zone,
		"creation_date": res.Created.Format(time.RFC3339),
	}
	for k, v := range res.Attrs {
		out[k] = v
	}

	ids := make([]any, 0, 1)
	for _, ip := range p.ipamIPsOf(res.ID) {
		ids = append(ids, ip.ID)
	}
	out["ipam_ip_ids"] = ids
	return out
}
