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
	// "Private NIC tags", exact like the rest of instance/v1: a conjunction.
	if tags := csvValues(r.URL.Query(), "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasEveryTag(res, tags)
		})
	}
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

	res, ok := p.attachNIC(w, r, server.ID, req)
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusCreated, map[string]any{"private_nic": p.privateNICView(res)})
}

// attachNIC is the whole attachment, from the server hold to the firewall, and
// it is shared because two APIs now reach it: instance/v1
// (POST /servers/{id}/private_nics) and instance/v2alpha1
// (POST /private-network-interfaces, server_id in the body). Terraform provider
// 2.81.0 uses the second, `scw` still uses the first.
//
// Shared rather than copied, and CLAUDE.md says why in general terms; here it is
// concrete. This sequence holds a per-server lock, re-reads the server inside it,
// serialises address allocation, books or allocates an address, writes back with
// Commit rather than Put, attaches the machine outside the store lock and reapplies
// the firewall. Six invariants, each one paid for by a defect. A second copy
// would have five of them for about a week.
//
// What differs between the two doors is the envelope alone — where server_id
// comes from, and the shape of the answer — so that is what stays in the
// handlers.
func (p *Pack) attachNIC(w http.ResponseWriter, r *http.Request, serverID string, req createPrivateNICRequest) (*resource.Resource, bool) {
	// Held for the whole handler. p.lockAddresses() below serialises NIC creates
	// against each other, but it is not the lock deleteServer takes, so nothing
	// ordered this handler against a delete of the server it is attaching to.
	//
	// What that costs is not the resurrection it looks like — Commit answers that
	// — but a NIC and an IPAM address stored beside a server that is gone by the
	// time the handler reaches its write-back. The address then stays booked
	// forever: the allocator is rebuilt from what IPAM holds, and the NIC is
	// invisible to every client, since NIC listings are scoped by server.
	//
	// Taken before p.lockAddresses() because serverAction already establishes
	// that order — it holds the server, then allocates an address — and one
	// direction everywhere is what keeps two callers from meeting head on.
	//
	// TestAttachingANICDoesNotResurrectADeletedServer fails without this.
	unlock := p.binding().Serialise(serverID)
	defer unlock()

	// And the target is read again inside the hold, because the hold alone is not
	// enough: serverOf answered before the lock existed, so a delete that had
	// already finished by then leaves this handler working from a copy of
	// something that no longer exists. Measured, not assumed — with only the hold,
	// the barrage below still stranded a NIC on trial 10.
	//
	// The two together close it from both sides: the re-read rules out a delete
	// that landed before the lock, the hold rules out one landing after.
	//
	// TestAttachingANICDoesNotResurrectADeletedServer fails without this too.
	server, found := p.env.Store.Get(Name, kindServer, serverID)
	if !found {
		writeNotFound(w, "server", serverID)
		return nil, false
	}

	if req.PrivateNetworkID == "" {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "private_network_id", Reason: "required"})
		return nil, false
	}
	pn, found := p.env.Store.Get(Name, kindPrivateNetwork, req.PrivateNetworkID)
	if !found {
		writeNotFound(w, "private_network", req.PrivateNetworkID)
		return nil, false
	}
	// One NIC per network per server, which is what the API enforces: a second
	// one would receive a second address on the same bridge and the control
	// plane could not say which one the machine answers on.
	for _, existing := range p.privateNICsOf(server.ID) {
		if existing.Runtime[runtimePrivateNetworkKey] == pn.ID {
			writePrecondition(w, "private_nic", existing.ID,
				"the server is already attached to this private network")
			return nil, false
		}
	}

	// Held across rebuild, allocate and persist: releasing it earlier would let
	// a concurrent request rebuild from a store that does not yet know about
	// this address, and hand out the same one. The booked path holds it too:
	// checking an address is unattached and attaching it is the same
	// read-modify-write.
	unlockAddresses := p.lockAddresses()
	defer unlockAddresses()

	// The client either names addresses it booked through ipam/v1, or receives
	// one from the network's own block. Same pool both ways: the allocator is
	// rebuilt from every IPAM address of the network, booked included.
	booked, ok := p.bookedIPsOf(w, req.bookedIPIDs(), pn, server)
	if !ok {
		return nil, false
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
			return nil, false
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
			return nil, false
		}
		address, err = alloc.Allocate()
		if err != nil {
			// An exhausted block is a real answer, not an internal error: the client
			// asked for one more address than the subnet holds.
			writePrecondition(w, "private_network", pn.ID,
				"no address left in "+alloc.Prefix().String())
			return nil, false
		}
	}

	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindPrivateNIC, server.Tenant, "available", now)
	res.Attrs = map[string]any{
		"private_network_id": pn.ID,
		"mac_address":        macAddressOf(address),
		"tags":               orEmpty(req.Tags),
	}
	res.Runtime = map[string]string{
		runtimeServerKey:         server.ID,
		runtimePrivateNetworkKey: pn.ID,
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
			return nil, false
		}
		p.env.Store.Put(p.newIPAMIP(regionOfZone(server.Tenant.Zone), server.Tenant.Project,
			netip.PrefixFrom(address, prefix.Bits()), res, pn))
	}

	// server.private_ip is deliberately not set: the SDK marks it deprecated and
	// "always null when routed_ip_enabled is True", which every server here is.
	// A client reads the address through this NIC and ipam/v1.
	//
	// Commit, not Put, and it is the rule the repository already states: Put
	// reinserts unconditionally, so attaching a NIC to a server a concurrent
	// DELETE had just removed brought the server back — with its address, its
	// volumes and a machine nobody would think to stop.
	//
	// **What this line is worth today, measured rather than asserted.** The
	// comment used to end "TestAttachingANICDoesNotResurrectADeletedServer fails
	// without this", and on 17 August 2026 that turned out to be false: with
	// Commit replaced by an unconditional Put, the barrage stayed green over 120
	// trials (tools/falsify/specs/one-attachment-two-doors.json, `repeat: 10`).
	// It is the per-server hold above that the same barrage kills in one run.
	//
	// The two guards overlap, and the hold subsumes this one: the re-read
	// happens inside the lock, so a delete either landed before it — and the
	// re-read answers 404 — or cannot land until this returns. Commit stays
	// because it costs nothing and is the only guard left if that hold is ever
	// narrowed, but it is a belt behind braces, and saying otherwise is the
	// exact failure CLAUDE.md names: a sentence that reads like a proof and is
	// not one.
	//
	// The base is the server exactly as this handler holds it: the attachment
	// changes nothing on the server itself, so the merge writes nothing but
	// the modification date — a concurrent tag or user-data write survives
	// where the old wholesale Commit erased it (#295).
	if !p.env.Store.Commit(server.Clone(), server, p.env.Now()) {
		writeNotFound(w, "server", server.ID)
		return nil, false
	}

	// The address lock is done once the allocation is written, and it is released
	// here rather than at the return: everything below reaches the runtime, which
	// takes seconds, and this lock is the one every address this pack hands out
	// waits on. Holding it across an attach serialised the whole pack behind one
	// interface reconfiguration — the same defect #216 named in the Outscale pack,
	// in the pack that was not audited for it.
	//
	// What still orders this handler against a concurrent delete of the same
	// server is the per-server hold taken at the top, which is a different lock
	// and stays held. The release is sync.Once-guarded, so the defer is still
	// correct on every path above.
	unlockAddresses()

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
		// Inside the store lock, and only the state: Put re-inserted a NIC
		// deleted during the seconds the attachment took, and wrote the whole
		// stale clone over anything else that landed meanwhile (#295).
		_ = p.env.Store.Update(Name, kindPrivateNIC, res.ID, func(stored *resource.Resource) error {
			stored.State = "syncing_error"
			stored.Updated = p.env.Now()
			return nil
		})
		res.State = "syncing_error"
	}

	// The rule set binds to interfaces, so a NIC created after the server was
	// powered on carries none until this runs. Without it the security group
	// applies to the first interface and not to the private one, which is
	// exactly the interface the group is about.
	p.applyServerFirewall(r.Context(), server)

	return res, true
}

// bookedIPsOf resolves the IPAM addresses a NIC create names, and refuses the
// ones a create cannot take: an address of another network, another region, or
// one something already holds. Resolved under p.lockAddresses(), which the caller
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
	p.releaseNIC(r.Context(), res)
	w.WriteHeader(http.StatusNoContent)
}

// releaseNIC gives back what a gone NIC held, and both doors to that state call
// it: DELETE on the NIC itself, and the delete of the server carrying it.
//
// Two doors, one function, for the reason releaseServerResources already states
// about volumes and addresses — written twice, they came to disagree twice. This
// one was not written twice, it was written once: deleting a server left its
// NICs and their addresses in the store, with no concurrency needed to see it.
// The NIC then named a server answering 404, invisible to a client because every
// NIC listing is scoped by server, while the address it held stayed booked and
// the network's allocator never handed it out again — so a subnet emptied over
// repeated create-attach-delete cycles.
//
// Measured on fr-par-1 before it was changed: attaching a server to a private
// network booked two IPAM addresses, and deleting the server left the network
// with none. The real API takes the NIC and its addresses with the server.
//
// TestDeletingAServerReleasesItsPrivateNICs fails without this.
//
// The runtime detach lives here rather than in each caller, and #426 is the
// measurement that put it here. There are three doors into this state — DELETE
// on the NIC, the v2alpha1 spelling of the same, and the delete of the server
// carrying it — and before this the store was the only thing any of them
// touched. Read on the host: a 204 on DELETE .../private_nics/{id} left the
// device on the container, so the later DeletePrivateNetwork got "The network
// is currently in use" from the runtime and the bridge outlived the run holding
// its block. TestDeletingAPrivateNICDetachesItFromTheRuntime fails without it.
func (p *Pack) releaseNIC(ctx context.Context, res *resource.Resource) {
	// Before the store forgets it: the names of both ends live on the resources
	// this is about to delete, and a detach that cannot name its machine is a
	// detach that never happens.
	p.detachMachineFromNetwork(ctx, res)
	// An allocated address goes back to the pool by disappearing from the
	// store: the allocator is rebuilt from what IPAM holds, so nothing to
	// release here. A booked one is the client's — it was reserved through
	// BookIP and is released through ReleaseIP — so it is detached and kept,
	// which is what the Terraform destroy order depends on: the NIC goes first,
	// the scaleway_ipam_ip after it. TestABookedAddressSurvivesItsNIC fails
	// without this.
	for _, ip := range p.ipamIPsOf(res.ID) {
		if isBooked, _ := ip.Attrs[attrBooked].(bool); isBooked {
			// Inside the store lock, never Get-mutate-Put: Put re-inserted an
			// address released meanwhile, and the wholesale write erased a
			// concurrent write to another field — its tags — after their 200
			// (#295).
			_ = p.env.Store.Update(Name, kindIPAMIP, ip.ID, func(stored *resource.Resource) error {
				delete(stored.Runtime, runtimeNICKey)
				delete(stored.Attrs, "mac_address")
				delete(stored.Attrs, "zone")
				stored.Updated = p.env.Now()
				return nil
			})
			continue
		}
		p.env.Store.Delete(Name, kindIPAMIP, ip.ID)
	}
	p.env.Store.Delete(Name, kindPrivateNIC, res.ID)
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

// detachMachineFromNetwork takes the backing machine off the backing network,
// the exact undo of attachMachineToNetwork.
//
// It degrades quietly for the same reason the attach does: with no runtime, or
// with a machine that never started, there is nothing to detach and the NIC
// still goes. A refusal is logged rather than returned, because the control
// plane has already decided the NIC is gone; what must not happen is the
// silence this replaced, where nothing was even attempted.
func (p *Pack) detachMachineFromNetwork(ctx context.Context, nic *resource.Resource) {
	if p.env.Machines == nil {
		return
	}
	server, ok := p.env.Store.Get(Name, kindServer, nic.Runtime[runtimeServerKey])
	if !ok {
		return
	}
	pn, ok := p.env.Store.Get(Name, kindPrivateNetwork, nic.Runtime[runtimePrivateNetworkKey])
	if !ok {
		return
	}
	machineName := server.Runtime[runtimeMachineKey]
	networkName := pn.Runtime[runtimeNetworkKey]
	if machineName == "" || networkName == "" {
		return
	}
	if err := p.env.Machines.Detach(ctx, machineName, networkName); err != nil {
		p.logger().Error("could not detach the machine from the private network",
			"private_nic", nic.ID, "server", server.ID, "private_network", pn.ID, "error", err)
	}
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

// Owns is this pack's half of the shared orphan sweep: which of its resources
// belong to another, and to which one.
//
// Two links, both of them exclusive and both of them found the hard way — a
// volume that named a deleted server, then a NIC that did (#214). The vocabulary
// is Scaleway's, so it is declared here; the invariant is everyone's, so it lives
// in storetest.
func Owns(res *resource.Resource) (kind, id string, ok bool) {
	switch res.Kind {
	case kindPrivateNIC, kindVolume:
		if serverID := res.Runtime[runtimeServerKey]; serverID != "" {
			return kindServer, serverID, true
		}
	case kindIPAMIP:
		if nicID := res.Runtime[runtimeNICKey]; nicID != "" {
			return kindPrivateNIC, nicID, true
		}
	}
	return "", "", false
}
