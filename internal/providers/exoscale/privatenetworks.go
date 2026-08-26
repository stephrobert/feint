package exoscale

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"net"
	"net/http"
	"net/netip"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Private networks, batch 3 of the Exoscale roadmap (#9).
//
// What is measured and what is not, said plainly because the two must not be
// confused. The request bodies are measured: EXOSCALE_TRACE=1 prints every
// request the official CLI sends, and the create was traced on 2026-08-14 —
// `{"description":…,"end-ip":…,"name":…,"netmask":…,"options":{},"start-ip":…}`
// for a managed network, `{"name":…,"options":{}}` for a plain one, options
// always present even when empty. The response shape is NOT measured: the one
// recording in shapes/exoscale.json (`GET /v2/private-network`) was taken on an
// account that held no private network, so it proves the envelope key and
// nothing below it. The body here is read from Exoscale's published API
// description (the `private-network` schema: description, end-ip, id, labels,
// leases, name, netmask, options, start-ip, vni), which the contract check
// enforces as closed. What would turn this into a measurement is a
// `feint shapes --record` run on an account that holds one, leases included.
//
// The model: a private network is its own L2 segment (the schema's vni is its
// VXLAN id). A *managed* network carries a DHCP range — start-ip, end-ip,
// netmask — and hands each attached instance a lease; a plain one carries no
// addressing at all, and upstream the guest configures itself. The emulator
// follows that split: a managed network gets a backing network from the machine
// driver and its leases are real addresses on it, a plain one stays a
// control-plane record, because a backing network needs a block and inventing
// one would publish addressing upstream never promised.
//
// Leases are computed from the instances that hold them, never stored on the
// network: a relation stored twice is the one that goes stale, and an instance
// deleted mid-flight takes its lease with it by construction.

const (
	kindPrivateNetwork = "private-network"
	nounPrivateNetwork = "private-network"

	// attrInstancePrivateNetworks is the instance-side membership: a list of
	// {id, ip} maps, ip empty on a plain network. Internal, never emitted by a
	// view; the wire shapes are computed from it.
	attrInstancePrivateNetworks = "feint-private-networks"

	// runtimeNetworkKey is the driver-side network name backing a managed
	// private network, same key as the Scaleway pack for the same thing.
	runtimeNetworkKey = "network"
)

// privateNetworkOptions is the DHCP options object of their schema, echoed
// back to the client that set it.
type privateNetworkOptions struct {
	DNSServers   []string `json:"dns-servers"`
	DomainSearch []string `json:"domain-search"`
	NTPServers   []string `json:"ntp-servers"`
	Routers      []string `json:"routers"`
}

func (o *privateNetworkOptions) empty() bool {
	return o == nil || (len(o.DNSServers) == 0 && len(o.DomainSearch) == 0 &&
		len(o.NTPServers) == 0 && len(o.Routers) == 0)
}

func (o *privateNetworkOptions) attr() map[string]any {
	out := map[string]any{}
	put := func(key string, values []string) {
		if len(values) == 0 {
			return
		}
		list := make([]any, 0, len(values))
		for _, v := range values {
			list = append(list, v)
		}
		out[key] = list
	}
	put("dns-servers", o.DNSServers)
	put("domain-search", o.DomainSearch)
	put("ntp-servers", o.NTPServers)
	put("routers", o.Routers)
	return out
}

// privateNetworkRequest is both the create and the update body: their API
// declares the same fields on both, name required only on the create. The
// traced CLI sends options on every create, {} when nothing was asked.
type privateNetworkRequest struct {
	Name        *string                `json:"name"`
	Description *string                `json:"description"`
	StartIP     *string                `json:"start-ip"`
	EndIP       *string                `json:"end-ip"`
	Netmask     *string                `json:"netmask"`
	Labels      map[string]string      `json:"labels"`
	Options     *privateNetworkOptions `json:"options"`
}

// managedRange is the DHCP range of a managed network, resolved to a prefix the
// machine driver can back.
type managedRange struct {
	start, end netip.Addr
	prefix     netip.Prefix
}

// managedRangeOf validates the range triple. All three fields or none: their
// CLI sends the three together, and a network with a start and no netmask is
// addressing nobody can compute. The netmask must be contiguous — net.IPMask
// answers (0, 0) for anything else — and both ends must sit in the network the
// mask carves around the start.
//
// TestAHalfDeclaredRangeIsRefused fails without this.
func managedRangeOf(startIP, endIP, netmask string) (managedRange, bool, error) {
	if startIP == "" && endIP == "" && netmask == "" {
		return managedRange{}, false, nil
	}
	if startIP == "" || endIP == "" || netmask == "" {
		return managedRange{}, false, errRange("start-ip, end-ip and netmask go together")
	}
	start, err := netip.ParseAddr(startIP)
	if err != nil || !start.Is4() {
		return managedRange{}, false, errRange("start-ip is not an IPv4 address")
	}
	end, err := netip.ParseAddr(endIP)
	if err != nil || !end.Is4() {
		return managedRange{}, false, errRange("end-ip is not an IPv4 address")
	}
	mask := net.ParseIP(netmask)
	if mask == nil || mask.To4() == nil {
		return managedRange{}, false, errRange("netmask is not an IPv4 netmask")
	}
	ones, bits := net.IPMask(mask.To4()).Size()
	if bits != 32 || ones == 0 || ones > 30 {
		return managedRange{}, false, errRange("netmask is not a usable IPv4 netmask")
	}
	prefix, err := start.Prefix(ones)
	if err != nil {
		return managedRange{}, false, errRange("start-ip does not fit the netmask")
	}
	if !prefix.Contains(end) {
		return managedRange{}, false, errRange("end-ip is outside the network start-ip and netmask define")
	}
	if end.Less(start) {
		return managedRange{}, false, errRange("end-ip is below start-ip")
	}
	return managedRange{start: start, end: end, prefix: prefix}, true, nil
}

type rangeError string

func (e rangeError) Error() string { return string(e) }

func errRange(msg string) error { return rangeError(msg) }

// rangeOf reads the stored triple back. A network stored without one is a
// plain network, and ok is false.
func rangeOf(res *resource.Resource) (managedRange, bool) {
	startIP, _ := res.Attrs["start-ip"].(string)
	endIP, _ := res.Attrs["end-ip"].(string)
	netmask, _ := res.Attrs["netmask"].(string)
	r, managed, err := managedRangeOf(startIP, endIP, netmask)
	if err != nil {
		return managedRange{}, false
	}
	return r, managed
}

// ---- Create -----------------------------------------------------------------

func (p *Pack) createPrivateNetwork(w http.ResponseWriter, r *http.Request) {
	var req privateNetworkRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	name := ""
	if req.Name != nil {
		name = *req.Name
	}
	if name == "" {
		writeError(w, http.StatusBadRequest, "name is required")
		return
	}
	if strings.ContainsAny(name+stringOf(req.Description), "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name carries control characters")
		return
	}
	dhcp, managed, err := managedRangeOf(stringOf(req.StartIP), stringOf(req.EndIP), stringOf(req.Netmask))
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	// Held from the moment a VNI is chosen until the network holding it is
	// stored: the VNI is read-modify-write over the store, the exact shape the
	// barrage of #134 caught on this pack's elastic addresses.
	// TestConcurrentAllocationsShareNothing fails without it.
	unlock := p.lockAddresses()
	defer unlock()

	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindPrivateNetwork, resource.Tenant{Provider: Name}, "present", now)
	res.Attrs = map[string]any{
		"name": name,
		// The VXLAN id their schema declares on every network. Lowest free,
		// so it is stable for a client that stored it and returns to the
		// pool when the network goes.
		"vni": p.freeVNI(),
	}
	if d := stringOf(req.Description); d != "" {
		res.Attrs["description"] = d
	}
	if managed {
		res.Attrs["start-ip"] = dhcp.start.String()
		res.Attrs["end-ip"] = dhcp.end.String()
		res.Attrs["netmask"] = stringOf(req.Netmask)
	}
	if len(req.Labels) > 0 {
		res.Attrs["labels"] = labelsToAttr(req.Labels)
	}
	if !req.Options.empty() {
		res.Attrs["options"] = req.Options.attr()
	}

	// A managed network becomes a real network before the record exists, and a
	// runtime refusal is fatal to the request rather than logged: handing back
	// a network that exists nowhere, with addresses no machine can carry, is
	// the defect this emulator exists to avoid. The common cause is the block
	// already being used on the operator's host, and the refusal says so. A
	// plain network carries no block, so there is nothing to back.
	if managed {
		if err := p.ensureBackingNetwork(r.Context(), res, dhcp.prefix); err != nil {
			p.logger().Error("could not create the backing network",
				"private-network", res.ID, "range", dhcp.prefix.String(), "error", err)
			writeError(w, http.StatusBadRequest,
				"the machine runtime could not provide a network for "+dhcp.prefix.String()+
					"; the block is most likely already in use on this host: "+err.Error())
			return
		}
	}
	p.env.Store.Put(res)
	p.isolatePrivateNetworks(r.Context())

	p.writeOperation(w, p.operationReferring(nounPrivateNetwork, res.ID))
}

// freeVNI hands out the lowest unused VXLAN id, starting at 1 as their schema's
// exclusive minimum of 0 demands. Computed from what exists rather than
// counted, so a deleted network's id returns to the pool. Callers hold
// p.lockAddresses() across the read and the write.
func (p *Pack) freeVNI() int {
	used := map[int]bool{}
	for _, res := range p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name}) {
		used[intOf(res.Attrs["vni"])] = true
	}
	for vni := 1; ; vni++ {
		if !used[vni] {
			return vni
		}
	}
}

// intOf reads a stored integer back, tolerating the float64 a snapshot restore
// produces.
func intOf(v any) int {
	switch n := v.(type) {
	case int:
		return n
	case float64:
		return int(n)
	}
	return 0
}

func stringOf(v *string) string {
	if v == nil {
		return ""
	}
	return *v
}

// ensureBackingNetwork asks the machine driver for a real network carrying the
// managed range. The gateway is the block's first host address, one the lease
// allocator below never hands out. The mechanics — the derived name, the
// labels, recording the name only once the driver accepted it — are
// Binding.EnsureBackingNetwork's, shared with the two other packs (#510).
func (p *Pack) ensureBackingNetwork(ctx context.Context, res *resource.Resource, prefix netip.Prefix) error {
	return p.binding().EnsureBackingNetwork(ctx, res, machine.BackingNetwork{
		Key:     runtimeNetworkKey,
		CIDR:    prefix,
		Gateway: true,
		NAT:     true,
		Marker:  "feint.private-network",
	})
}

// isolatePrivateNetworks keeps the emulated networks apart, which two managed
// bridges on one host are not by themselves.
//
// What counts as foreign is an Exoscale question, and the answer is simpler
// than Scaleway's: every private network is its own VXLAN segment upstream, so
// every other network is foreign — there is no VPC routing to let through in
// this batch, and the predicate below says exactly that. The reconciliation —
// empty peer lists under native isolation, every managed block to keep out
// otherwise — is machine.ReconcileIsolation, shared with the two other packs.
//
// Coalesced like the Outscale pack's (#473, the measurement and the guarantee
// are on isolateNetworks there): a burst of concurrent creates shares its
// passes instead of each request paying a full O(N) one. The pass re-reads
// the store when it runs, and every caller returns only once a pass that read
// its own change has completed.
func (p *Pack) isolatePrivateNetworks(ctx context.Context) {
	ctx = context.WithoutCancel(ctx)
	p.isolation.Run(func() { p.isolationPass(ctx) })
}

// isolationPass is one full reconciliation, reading the store at the moment it
// runs. Only the Coalescer calls it.
func (p *Pack) isolationPass(ctx context.Context) {
	all := p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name})
	members := make([]machine.IsolationMember, len(all))
	for i, pn := range all {
		block := ""
		if dhcp, managed := rangeOf(pn); managed {
			block = dhcp.prefix.Masked().String()
		}
		members[i] = machine.IsolationMember{
			ID:      pn.ID,
			Network: pn.Runtime[runtimeNetworkKey],
			Block:   block,
		}
	}
	p.binding().ReconcileIsolation(ctx, "private-network",
		members, func(int, int) bool { return false })
}

// ---- Reads ------------------------------------------------------------------

func (p *Pack) listPrivateNetworks(w http.ResponseWriter, _ *http.Request) {
	list := p.env.Store.List(kindPrivateNetwork, resource.Tenant{Provider: Name})
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	networks := make([]map[string]any, 0, len(list))
	for _, res := range list {
		networks = append(networks, p.privateNetworkView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"private-networks": networks})
}

func (p *Pack) getPrivateNetwork(w http.ResponseWriter, r *http.Request) {
	res, ok := p.env.Store.Get(Name, kindPrivateNetwork, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.privateNetworkView(res))
}

// privateNetworkView renders the schema's fields and only those. The omission
// rules — description, labels, options, leases and the range absent rather
// than empty — follow the measured habit of this API on security groups
// (rules and external-sources are absent keys when empty); no recording holds
// a private network yet, so that rule is a transfer, not a measurement.
func (p *Pack) privateNetworkView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":   res.ID,
		"name": res.Attrs["name"],
		"vni":  res.Attrs["vni"],
	}
	for _, key := range []string{"description", "start-ip", "end-ip", "netmask"} {
		if v, _ := res.Attrs[key].(string); v != "" {
			out[key] = v
		}
	}
	if labels, _ := res.Attrs["labels"].(map[string]any); len(labels) > 0 {
		out["labels"] = labels
	}
	if options, _ := res.Attrs["options"].(map[string]any); len(options) > 0 {
		out["options"] = options
	}
	if leases := p.leasesOf(res.ID); len(leases) > 0 {
		out["leases"] = leases
	}
	return out
}

// takenLeases is the set of addresses the network's leases already hold, for a
// chooser that must not hand one out twice. Read under p.lockAddresses(), like
// every other walk over the leases.
func (p *Pack) takenLeases(networkID string) map[string]bool {
	taken := map[string]bool{}
	for _, lease := range p.leasesOf(networkID) {
		m, _ := lease.(map[string]any)
		if ip, _ := m["ip"].(string); ip != "" {
			taken[ip] = true
		}
	}
	return taken
}

// gatewayOf is the block's first host address, which belongs to the gateway
// the backing network answers on, never to a lease.
func gatewayOf(dhcp managedRange) string {
	return dhcp.prefix.Masked().Addr().Next().String()
}

// firstFreeLease picks the lowest address of a managed range that no lease
// holds and that is not the gateway, or "" when the range is exhausted.
func firstFreeLease(dhcp managedRange, taken map[string]bool) string {
	gateway := gatewayOf(dhcp)
	for addr := dhcp.start; !dhcp.end.Less(addr); addr = addr.Next() {
		candidate := addr.String()
		if !taken[candidate] && candidate != gateway {
			return candidate
		}
	}
	return ""
}

// leasesOf computes the leases of a managed network from the instances that
// hold them: {instance-id, ip}, ordered by address so two reads answer the
// same bytes.
func (p *Pack) leasesOf(networkID string) []any {
	type lease struct{ instanceID, ip string }
	var found []lease
	for _, inst := range p.env.Store.List(kindInstance, resource.Tenant{Provider: Name}) {
		for _, att := range instanceAttachmentsOf(inst) {
			if att.NetworkID == networkID && att.IP != "" {
				found = append(found, lease{instanceID: inst.ID, ip: att.IP})
			}
		}
	}
	sort.Slice(found, func(i, j int) bool { return found[i].ip < found[j].ip })
	out := make([]any, 0, len(found))
	for _, l := range found {
		out = append(out, map[string]any{"instance-id": l.instanceID, "ip": l.ip})
	}
	return out
}

// ---- Update, reset, delete --------------------------------------------------

func (p *Pack) updatePrivateNetwork(w http.ResponseWriter, r *http.Request) {
	var req privateNetworkRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if strings.ContainsAny(stringOf(req.Name)+stringOf(req.Description), "\n\r\x00") {
		writeError(w, http.StatusBadRequest, "name carries control characters")
		return
	}
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindPrivateNetwork, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	// The base of the write-back below. This handler was the one Put left in
	// this pack's update paths, and it sat on the slowest window of all: a
	// range change rebuilds the backing network on the runtime, and the Put
	// that followed re-inserted a network deleted meanwhile and erased a
	// concurrent write to any other field after its 200 — the issue's claim
	// that this pack was clean did not survive the audit (#295).
	base := res.Clone()

	// The effective range is the stored one with the sent fields applied, and
	// the whole triple is re-validated: an update that leaves start above end
	// is as wrong as a create that starts there.
	startIP, _ := res.Attrs["start-ip"].(string)
	endIP, _ := res.Attrs["end-ip"].(string)
	netmask, _ := res.Attrs["netmask"].(string)
	if req.StartIP != nil {
		startIP = *req.StartIP
	}
	if req.EndIP != nil {
		endIP = *req.EndIP
	}
	if req.Netmask != nil {
		netmask = *req.Netmask
	}
	dhcp, managed, err := managedRangeOf(startIP, endIP, netmask)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	oldRange, wasManaged := rangeOf(res)
	rangeChanged := managed != wasManaged || (managed && wasManaged && (dhcp.prefix != oldRange.prefix ||
		dhcp.start != oldRange.start || dhcp.end != oldRange.end))
	if rangeChanged && len(p.leasesOf(id)) > 0 {
		// The emulator's own refusal, not a measured one: a lease outside the
		// new range would be an address the API published and nothing carries.
		writeError(w, http.StatusBadRequest,
			"the range cannot change while instances hold leases on this network; detach them first")
		return
	}

	if req.Name != nil && *req.Name != "" {
		res.Attrs["name"] = *req.Name
	}
	if req.Description != nil {
		if *req.Description == "" {
			delete(res.Attrs, "description")
		} else {
			res.Attrs["description"] = *req.Description
		}
	}
	if req.Labels != nil {
		res.Attrs["labels"] = labelsToAttr(req.Labels)
	}
	if req.Options != nil {
		if req.Options.empty() {
			delete(res.Attrs, "options")
		} else {
			res.Attrs["options"] = req.Options.attr()
		}
	}
	if managed {
		res.Attrs["start-ip"] = dhcp.start.String()
		res.Attrs["end-ip"] = dhcp.end.String()
		res.Attrs["netmask"] = netmask
	}

	// A changed block needs a changed backing network. EnsureNetwork succeeds
	// on an existing network without reshaping it, so the old one goes first —
	// safe here because the refusal above proved nothing is attached.
	//
	// No "is a runtime configured" guard: both verbs below degrade to nothing
	// without one (Binding.RemoveBackingNetwork and EnsureBackingNetwork both
	// return on a nil driver), and asking the question meant holding the
	// driver, which is what #511 took away.
	if rangeChanged {
		if err := p.binding().RemoveBackingNetwork(r.Context(), res, runtimeNetworkKey); err != nil {
			p.logger().Error("could not remove the outgrown backing network",
				"private-network", res.ID, "network", res.Runtime[runtimeNetworkKey], "error", err)
			// Forgotten anyway: the re-ensure below derives the same name, and
			// keeping the record would change nothing about what it finds.
			delete(res.Runtime, runtimeNetworkKey)
		}
		if managed {
			if err := p.ensureBackingNetwork(r.Context(), res, dhcp.prefix); err != nil {
				p.logger().Error("could not create the backing network",
					"private-network", res.ID, "range", dhcp.prefix.String(), "error", err)
				writeError(w, http.StatusBadRequest,
					"the machine runtime could not provide a network for "+dhcp.prefix.String()+
						"; the block is most likely already in use on this host: "+err.Error())
				return
			}
		}
	}

	if !p.env.Store.Commit(base, res, p.env.Now()) {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.isolatePrivateNetworks(r.Context())
	p.writeOperation(w, p.operationReferring(nounPrivateNetwork, id))
}

// resetPrivateNetworkField clears one field. Their API description enumerates
// exactly one resettable field, labels, and the enum is the whole contract:
// anything else is refused rather than guessed at.
func (p *Pack) resetPrivateNetworkField(w http.ResponseWriter, r *http.Request) {
	if r.PathValue("field") != "labels" {
		writeError(w, http.StatusBadRequest, "labels is the only resettable field")
		return
	}
	id := r.PathValue("id")
	err := p.env.Store.Update(Name, kindPrivateNetwork, id, func(stored *resource.Resource) error {
		delete(stored.Attrs, "labels")
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounPrivateNetwork, id))
}

func (p *Pack) deletePrivateNetwork(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindPrivateNetwork, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	// A network an instance still sits on does not go — the emulator's own
	// refusal, like the security group's: letting it vanish would leave every
	// attached instance publishing a reference to a ghost.
	// TestANetworkStillAttachedRefusesItsDelete fails without this.
	for _, inst := range p.env.Store.List(kindInstance, resource.Tenant{Provider: Name}) {
		for _, att := range instanceAttachmentsOf(inst) {
			if att.NetworkID == id {
				writeError(w, http.StatusBadRequest,
					"the private network is still attached to at least one instance")
				return
			}
		}
	}
	if err := p.binding().RemoveBackingNetwork(r.Context(), res, runtimeNetworkKey); err != nil {
		p.logger().Error("could not remove the backing network",
			"private-network", res.ID, "network", res.Runtime[runtimeNetworkKey], "error", err)
	}
	p.env.Store.Delete(Name, kindPrivateNetwork, id)
	p.isolatePrivateNetworks(r.Context())
	p.writeOperation(w, p.operationReferring(nounPrivateNetwork, id))
}

// ---- Attachment -------------------------------------------------------------

// pnAttachment is one instance-side membership record.
type pnAttachment struct {
	NetworkID string
	IP        string
}

// instanceAttachmentsOf reads the memberships back, tolerating the []any of
// map[string]any a snapshot restore produces.
func instanceAttachmentsOf(res *resource.Resource) []pnAttachment {
	raw, _ := res.Attrs[attrInstancePrivateNetworks].([]any)
	out := make([]pnAttachment, 0, len(raw))
	for _, entry := range raw {
		m, _ := entry.(map[string]any)
		id, _ := m["id"].(string)
		if id == "" {
			continue
		}
		ip, _ := m["ip"].(string)
		out = append(out, pnAttachment{NetworkID: id, IP: ip})
	}
	return out
}

func attachmentsToAttr(list []pnAttachment) []any {
	out := make([]any, 0, len(list))
	for _, att := range list {
		out = append(out, map[string]any{"id": att.NetworkID, "ip": att.IP})
	}
	return out
}

// attachTarget is the measured body of the ":attach", ":detach" and
// ":update-ip" actions: the instance as an object of which the identifier
// names it, and for managed networks an optional static lease.
type attachTarget struct {
	Instance struct {
		ID string `json:"id"`
		// The CLI serialises its whole instance object on these actions the way
		// it does on the security-group ones; declared so the unread-field
		// report sees it read, and a client-side timestamp is not an
		// instruction.
		CreatedAt string `json:"created-at"`
	} `json:"instance"`
	IP string `json:"ip"`
}

func (p *Pack) attachInstanceToPrivateNetwork(w http.ResponseWriter, r *http.Request) {
	var req attachTarget
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Instance.ID == "" {
		writeError(w, http.StatusBadRequest, "instance is required")
		return
	}
	id := r.PathValue("id")
	pn, found := p.env.Store.Get(Name, kindPrivateNetwork, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	// The instance is held for the read, the lease, and the runtime attach,
	// like every lifecycle path of this pack; the address allocation is its own
	// read-modify-write over every instance's leases and holds p.lockAddresses()
	// across the read and the write, which is what the barrage checks.
	unlock := p.binding().Serialise(req.Instance.ID)
	defer unlock()

	leaseIP, ok := p.takeLease(w, pn, req.Instance.ID, req.IP)
	if !ok {
		return
	}

	inst, found := p.env.Store.Get(Name, kindInstance, req.Instance.ID)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	// Join attaches and then resyncs the firewall: the interface this attach
	// just created carries the instance's rule sets like every other one — the
	// Terraform provider attaches networks after the create returns, and a set
	// applied at boot never reaches an interface born later (#475).
	// ApplyFirewall covers every NIC of the machine, so re-applying is
	// idempotent for the older ones; the groups naming this instance's groups
	// re-expand with it, because the new lease is now part of what they mean.
	p.joinPrivateNetwork(r.Context(), inst, pn, leaseIP)
	p.writeOperation(w, p.operationReferring(nounPrivateNetwork, id))
}

// takeLease reserves an address of the network's range for the instance and
// records the membership, under one hold of p.lockAddresses(): choosing from what
// exists and storing the choice must be one critical section, or two attaches
// interleaving there receive the same address — the defect the barrage of #134
// found on this pack's elastic pool. TestConcurrentAllocationsShareNothing
// fails without it.
func (p *Pack) takeLease(w http.ResponseWriter, pn *resource.Resource, instanceID, requested string) (string, bool) {
	unlock := p.lockAddresses()
	defer unlock()

	dhcp, managed := rangeOf(pn)
	leaseIP := ""
	switch {
	case !managed && requested != "":
		// Their CLI documents --ip as "managed Private Networks only"; on a
		// plain network there is no range to lease from, and storing the
		// address anyway would publish addressing the network never declared.
		writeError(w, http.StatusBadRequest,
			"a static lease needs a managed private network, and this one declares no range")
		return "", false
	case managed:
		taken := p.takenLeases(pn.ID)
		if requested != "" {
			addr, err := netip.ParseAddr(requested)
			if err != nil || !addr.Is4() {
				writeError(w, http.StatusBadRequest, "ip is not an IPv4 address")
				return "", false
			}
			if addr.Less(dhcp.start) || dhcp.end.Less(addr) {
				writeError(w, http.StatusBadRequest, "ip is outside the network's range")
				return "", false
			}
			if taken[requested] || requested == gatewayOf(dhcp) {
				writeError(w, http.StatusBadRequest, "ip is already leased")
				return "", false
			}
			leaseIP = requested
		} else {
			leaseIP = firstFreeLease(dhcp, taken)
			if leaseIP == "" {
				writeError(w, http.StatusBadRequest, "no address left in the network's range")
				return "", false
			}
		}
	}

	conflict := false
	err := p.env.Store.Update(Name, kindInstance, instanceID, func(stored *resource.Resource) error {
		list := instanceAttachmentsOf(stored)
		for _, att := range list {
			if att.NetworkID == pn.ID {
				// One attachment per network per instance: a second one would
				// hand the same interface two leases, and the control plane
				// could not say which one the machine answers on. The
				// emulator's own refusal, like the Scaleway NIC's.
				conflict = true
				return nil
			}
		}
		list = append(list, pnAttachment{NetworkID: pn.ID, IP: leaseIP})
		stored.Attrs[attrInstancePrivateNetworks] = attachmentsToAttr(list)
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return "", false
	}
	if conflict {
		writeError(w, http.StatusBadRequest, "the instance is already attached to this private network")
		return "", false
	}
	return leaseIP, true
}

func (p *Pack) detachInstanceFromPrivateNetwork(w http.ResponseWriter, r *http.Request) {
	var req attachTarget
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Instance.ID == "" {
		writeError(w, http.StatusBadRequest, "instance is required")
		return
	}
	id := r.PathValue("id")
	if _, found := p.env.Store.Get(Name, kindPrivateNetwork, id); !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}

	unlock := p.binding().Serialise(req.Instance.ID)
	defer unlock()

	err := p.env.Store.Update(Name, kindInstance, req.Instance.ID, func(stored *resource.Resource) error {
		list := instanceAttachmentsOf(stored)
		kept := make([]pnAttachment, 0, len(list))
		for _, att := range list {
			if att.NetworkID != id {
				kept = append(kept, att)
			}
		}
		stored.Attrs[attrInstancePrivateNetworks] = attachmentsToAttr(kept)
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	// And the machine half, which this used to say it could not do: the comment
	// here read "the driver deliberately has no hot-unplug ... the same window
	// the Scaleway NIC has", and #426 measured what that window costs. A device
	// left on a container keeps its network alive, RemoveNetwork then refuses
	// with "The network is currently in use", and the bridge outlives the run
	// holding its address block. The driver has Detach now, so this uses it and
	// the window is closed on both sides at once — which is the point of the
	// capability living in the shared layer rather than in one pack.
	//
	// TestDetachingAnInstanceTakesTheDeviceOffTheRuntime fails without this.
	p.detachMachineFromPrivateNetwork(r.Context(), req.Instance.ID, id)
	p.writeOperation(w, p.operationReferring(nounPrivateNetwork, id))
}

func (p *Pack) updatePrivateNetworkInstanceIP(w http.ResponseWriter, r *http.Request) {
	var req attachTarget
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.Instance.ID == "" {
		writeError(w, http.StatusBadRequest, "instance is required")
		return
	}
	if req.IP == "" {
		writeError(w, http.StatusBadRequest, "ip is required")
		return
	}
	id := r.PathValue("id")
	pn, found := p.env.Store.Get(Name, kindPrivateNetwork, id)
	if !found {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	dhcp, managed := rangeOf(pn)
	if !managed {
		writeError(w, http.StatusBadRequest,
			"a static lease needs a managed private network, and this one declares no range")
		return
	}

	unlock := p.binding().Serialise(req.Instance.ID)
	defer unlock()

	if ok := p.moveLease(w, pn, dhcp, req.Instance.ID, req.IP); !ok {
		return
	}
	if inst, ok := p.env.Store.Get(Name, kindInstance, req.Instance.ID); ok {
		p.joinPrivateNetwork(r.Context(), inst, pn, req.IP)
	}
	p.writeOperation(w, p.operationReferring(nounPrivateNetwork, id))
}

// moveLease points an existing membership at a new address, under the same
// hold of p.lockAddresses() as takeLease and for the same reason.
func (p *Pack) moveLease(w http.ResponseWriter, pn *resource.Resource, dhcp managedRange, instanceID, requested string) bool {
	unlock := p.lockAddresses()
	defer unlock()

	addr, err := netip.ParseAddr(requested)
	if err != nil || !addr.Is4() {
		writeError(w, http.StatusBadRequest, "ip is not an IPv4 address")
		return false
	}
	if addr.Less(dhcp.start) || dhcp.end.Less(addr) {
		writeError(w, http.StatusBadRequest, "ip is outside the network's range")
		return false
	}
	for _, inst := range p.env.Store.List(kindInstance, resource.Tenant{Provider: Name}) {
		if inst.ID == instanceID {
			continue
		}
		for _, att := range instanceAttachmentsOf(inst) {
			if att.NetworkID == pn.ID && att.IP == requested {
				writeError(w, http.StatusBadRequest, "ip is already leased")
				return false
			}
		}
	}

	attached := false
	err = p.env.Store.Update(Name, kindInstance, instanceID, func(stored *resource.Resource) error {
		list := instanceAttachmentsOf(stored)
		for i := range list {
			if list[i].NetworkID == pn.ID {
				list[i].IP = requested
				attached = true
			}
		}
		stored.Attrs[attrInstancePrivateNetworks] = attachmentsToAttr(list)
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return false
	}
	if !attached {
		writeError(w, http.StatusBadRequest, "the instance is not attached to this private network")
		return false
	}
	return true
}

// ---- The machine half -------------------------------------------------------

// membershipAttachment is what one private-network membership means to the
// runtime: the backing network, the leased address, and the range's mask when
// the network is managed — configuring the interface inside the guest needs
// both halves.
func (p *Pack) membershipAttachment(pn *resource.Resource, ip string) machine.Attachment {
	att := machine.Attachment{Network: pn.Runtime[runtimeNetworkKey], Address: ip}
	if dhcp, managed := rangeOf(pn); managed && ip != "" {
		att.PrefixLen = dhcp.prefix.Bits()
	}
	return att
}

// joinPrivateNetwork puts the backing machine on the backing network at the
// address the control plane just published, and resyncs the firewall after it
// — the hot half of a membership, through the shared Reconciler (#510).
// Degrades quietly, like every runtime call of this pack: with no runtime, or
// none started yet, the membership is still true and the interface arrives
// with the next boot's plan.
func (p *Pack) joinPrivateNetwork(ctx context.Context, inst, pn *resource.Resource, ip string) {
	_ = p.reconciler().Join(ctx, inst, p.membershipAttachment(pn, ip))
}

// detachMachineFromPrivateNetwork is the exact undo of joinPrivateNetwork's
// attach half, and degrades the same way: with no runtime, or a machine that
// never started, there is nothing to take off and the membership has already
// gone from the store.
func (p *Pack) detachMachineFromPrivateNetwork(ctx context.Context, instanceID, networkID string) {
	inst, found := p.env.Store.Get(Name, kindInstance, instanceID)
	if !found {
		return
	}
	pn, found := p.env.Store.Get(Name, kindPrivateNetwork, networkID)
	if !found {
		return
	}
	p.reconciler().Leave(ctx, inst, pn.Runtime[runtimeNetworkKey])
}

// privateNetworkRefs is the instance-side wire shape: their instance schema
// declares private-networks as [{id, mac-address}]. The MAC is derived from
// the pair, stable across reads because a client stores it, and locally
// administered so it cannot collide with a vendor prefix. A network that no
// longer exists is dropped: the relation is computed, never stored.
func (p *Pack) privateNetworkRefs(res *resource.Resource) []any {
	out := make([]any, 0)
	for _, att := range instanceAttachmentsOf(res) {
		if _, found := p.env.Store.Get(Name, kindPrivateNetwork, att.NetworkID); !found {
			continue
		}
		out = append(out, map[string]any{
			"id":          att.NetworkID,
			"mac-address": attachmentMAC(res.ID, att.NetworkID),
		})
	}
	return out
}

// attachmentMAC derives the interface's MAC from the pair that owns it. 0a
// keeps it locally administered and unicast, like the instance's own 06 prefix.
func attachmentMAC(instanceID, networkID string) string {
	sum := sha256.Sum256([]byte("pn-mac:" + instanceID + ":" + networkID))
	hexed := hex.EncodeToString(sum[:5])
	return "0a:" + hexed[0:2] + ":" + hexed[2:4] + ":" + hexed[4:6] + ":" + hexed[6:8] + ":" + hexed[8:10]
}
