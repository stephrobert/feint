package exoscale

import (
	"context"
	"fmt"
	"net/http"
	"net/netip"
	"sort"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Elastic IPs, in the measured shape: {addressfamily, cidr, description, id,
// ip}, description omitted when empty. Attach and detach are actions on the IP
// naming the instance, and the operation they mint refers to the elastic IP,
// not the instance — that is what the recording shows the provider waiting on.
//
// The addresses come from 192.0.2.0/24 (TEST-NET-1): fixed, documented as
// never routable on the internet, and disjoint from the machine runtime's own
// ranges. One RFC 5737 block per pack, because an attached address is routed
// on the host now and one host serves three emulated clouds: Scaleway keeps
// TEST-NET-3, Outscale has TEST-NET-2, and each pack's guard refuses the
// other two.
//
// Attaching routes the address to the instance's machine, the way the real
// cloud puts an elastic IP on an instance; detaching and deleting take the
// route back. It moved only the record for as long as the machines sat on the
// operator's default bridge, which the driver rightly refuses to route
// through; they live on emulator-owned networks now.

// elasticIPBlock is the block as a prefix, for the guard below.
const elasticIPBlock = "192.0.2.0/24"

// emulatedElasticIP reports whether an address is one this pack can have
// handed out: inside elasticIPBlock. A stored address is restored verbatim by
// PUT /_feint/state and `feint snapshot load`, and routing an arbitrary value
// would send the host's traffic for that address into a container.
// TestAPoisonedElasticIPIsNeverRouted fails without it.
func emulatedElasticIP(address string) bool {
	prefix, err := netip.ParsePrefix(elasticIPBlock)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return false
	}
	return prefix.Contains(addr)
}

// routeElasticIP makes an attached address reach the instance's machine.
// Degrades quietly, like every runtime call in this pack.
// TestAttachElasticIPRoutesTheAddress fails without it.
func (p *Pack) routeElasticIP(ctx context.Context, address string, inst *resource.Resource) {
	router, ok := p.env.Machines.(machine.Router)
	if !ok {
		return
	}
	name := inst.Runtime[p.binding().RuntimeKey]
	if name == "" || address == "" {
		return
	}
	if !emulatedElasticIP(address) {
		p.logger().Warn("refusing to route an address outside the emulated elastic block",
			"address", address, "instance", inst.ID)
		return
	}
	// Through the binding rather than straight at the router, because this pack
	// lets several instances hold one Elastic IP — which is what the real cloud
	// does, measured on ch-gva-2, where two instances both reported holding
	// 185.19.28.243. The control plane is right to allow it; the runtime cannot
	// honour it, since two containers answering ARP for one /32 make the host
	// pick arbitrarily while the API describes both as holders.
	//
	// So the address goes to the most recent attach and is taken back from the
	// previous holder. Upstream elects by healthcheck; feint has none and does
	// not invent one — docs/limits.md names the rule instead.
	//
	// TestAnElasticIPReachesOneMachineAtATime fails without this.
	if err := p.binding().RouteAddress(ctx, router, machine.AddressSpec{Machine: name, Address: address}); err != nil {
		p.logger().Error("could not route the elastic IP to the machine",
			"address", address, "instance", inst.ID, "error", err)
	}
}

// unrouteElasticIP takes the route back. The machine may already be gone; the
// driver treats that as nothing left to undo, and on OVN it still withdraws
// the uplink route, which outlives the machine.
func (p *Pack) unrouteElasticIP(ctx context.Context, address string, inst *resource.Resource) {
	router, ok := p.env.Machines.(machine.Router)
	if !ok || !emulatedElasticIP(address) {
		return
	}
	name := ""
	if inst != nil {
		name = inst.Runtime[p.binding().RuntimeKey]
	}
	if err := p.binding().UnrouteAddress(ctx, router, name, address); err != nil {
		p.logger().Error("could not stop routing the elastic IP",
			"address", address, "error", err)
	}
}

// elasticBootAddresses is what an instance's machine must answer on from its
// first boot: every elastic IP attached to it. On the launch rather than
// routed afterwards, because editing a live OVN NIC re-plugs it.
func (p *Pack) elasticBootAddresses(res *resource.Resource) []string {
	ids := stringList(res.Attrs[attrElasticIPIDs])
	out := make([]string, 0, len(ids)+1)
	// The instance's own address comes first: it is the one `public-ip`
	// publishes and the one a client ssh's into, so it is the machine's primary
	// address rather than an extra routed onto it.
	if own, _ := res.Attrs["public-ip"].(string); emulatedElasticIP(own) {
		out = append(out, own)
	}
	for _, id := range ids {
		eip, found := p.env.Store.Get(Name, kindElasticIP, id)
		if !found {
			continue
		}
		if address, _ := eip.Attrs["ip"].(string); emulatedElasticIP(address) {
			out = append(out, address)
		}
	}
	return out
}

const (
	kindElasticIP = "elastic-ip"
	nounElasticIP = "elastic-ip"
)

type createElasticIPRequest struct {
	Description string `json:"description"`
	// Declared because their API does; the emulated pool is inet4 only and says
	// so rather than allocating an inet6 prefix it could not honour.
	AddressFamily string            `json:"addressfamily"`
	Healthcheck   map[string]any    `json:"healthcheck"`
	Labels        map[string]string `json:"labels"`
}

func (p *Pack) createElasticIP(w http.ResponseWriter, r *http.Request) {
	var req createElasticIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	if req.AddressFamily == "inet6" {
		writeError(w, http.StatusBadRequest, "this emulator allocates inet4 elastic IPs only")
		return
	}
	// Held across the read and the write, which is the whole point: the address
	// is chosen from what the store holds and only becomes taken when the
	// resource lands in it.
	unlock := p.lockAddresses()
	defer unlock()

	ip, ok := p.freeAddress()
	if !ok {
		writeError(w, http.StatusBadRequest, "no elastic IP left in the emulated pool")
		return
	}
	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindElasticIP, resource.Tenant{Provider: Name}, "present", now)
	res.Attrs = map[string]any{
		"ip":            ip,
		"addressfamily": "inet4",
		"description":   req.Description,
	}
	if req.Healthcheck != nil {
		res.Attrs["healthcheck"] = req.Healthcheck
	}
	p.env.Store.Put(res)
	p.writeOperation(w, p.operationReferring(nounElasticIP, res.ID))
}

// freeAddress hands out the lowest unused address of the pool. Computed
// from what exists rather than counted, so a deleted IP returns to the pool.
//
// Every kind that publishes an address of this block is scanned, and the list
// is the whole point: handing one address to two resources makes the driver
// route a single /32 to two machines, and makes two clients read the same
// address off two different objects.
//
// TestABalancerAndAnElasticIPNeverShareAnAddress fails when a kind drops out
// of it.
func (p *Pack) freeAddress() (string, bool) {
	used := map[string]bool{}
	for _, res := range p.env.Store.List(kindElasticIP, resource.Tenant{Provider: Name}) {
		if ip, _ := res.Attrs["ip"].(string); ip != "" {
			used[ip] = true
		}
	}
	// Instances draw from the same block since they assign their own public
	// address at creation, the way the real cloud does. Scanning only elastic
	// IPs would hand one address to two resources, and the driver would then
	// route a single /32 to two machines.
	for _, res := range p.env.Store.List(kindInstance, resource.Tenant{Provider: Name}) {
		if ip, _ := res.Attrs["public-ip"].(string); ip != "" {
			used[ip] = true
		}
	}
	// And network load balancers (#345), whose `ip` is the one address that
	// family owns. One RFC 5737 block per pack is the rule the file header
	// states, so the balancer cannot be given a block of its own the way the
	// Outscale LBU has one; it shares this pool and is counted in it.
	for _, res := range p.env.Store.List(kindLoadBalancer, resource.Tenant{Provider: Name}) {
		if ip, _ := res.Attrs["ip"].(string); ip != "" {
			used[ip] = true
		}
	}
	for host := 1; host <= 254; host++ {
		ip := fmt.Sprintf("192.0.2.%d", host)
		if !used[ip] {
			return ip, true
		}
	}
	return "", false
}

func (p *Pack) listElasticIPs(w http.ResponseWriter, _ *http.Request) {
	list := p.env.Store.List(kindElasticIP, resource.Tenant{Provider: Name})
	sort.Slice(list, func(i, j int) bool { return list[i].Created.Before(list[j].Created) })
	ips := make([]map[string]any, 0, len(list))
	for _, res := range list {
		ips = append(ips, elasticIPView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"elastic-ips": ips})
}

func (p *Pack) getElasticIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.env.Store.Get(Name, kindElasticIP, r.PathValue("id"))
	if !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	emulator.WriteJSON(w, http.StatusOK, elasticIPView(res))
}

type updateElasticIPRequest struct {
	Description *string           `json:"description"`
	Healthcheck map[string]any    `json:"healthcheck"`
	Labels      map[string]string `json:"labels"`
}

func (p *Pack) updateElasticIP(w http.ResponseWriter, r *http.Request) {
	var req updateElasticIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}
	id := r.PathValue("id")
	err := p.env.Store.Update(Name, kindElasticIP, id, func(stored *resource.Resource) error {
		if req.Description != nil {
			stored.Attrs["description"] = *req.Description
		}
		if req.Healthcheck != nil {
			stored.Attrs["healthcheck"] = req.Healthcheck
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	p.writeOperation(w, p.operationReferring(nounElasticIP, id))
}

func (p *Pack) deleteElasticIP(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, ok := p.env.Store.Get(Name, kindElasticIP, id); !ok {
		writeError(w, http.StatusNotFound, "resource not found")
		return
	}
	// The address is withdrawn from every instance that publishes it before the
	// IP goes: a view naming a deleted IP would be published by nothing, and a
	// route that outlived its record would shadow the next allocation.
	address := ""
	if eip, found := p.env.Store.Get(Name, kindElasticIP, id); found {
		address, _ = eip.Attrs["ip"].(string)
	}
	for _, inst := range p.env.Store.List(kindInstance, resource.Tenant{Provider: Name}) {
		ids := stringList(inst.Attrs[attrElasticIPIDs])
		if !contains(ids, id) {
			continue
		}
		p.unrouteElasticIP(r.Context(), address, inst)
		_ = p.env.Store.Update(Name, kindInstance, inst.ID, func(stored *resource.Resource) error {
			stored.Attrs[attrElasticIPIDs] = without(stringList(stored.Attrs[attrElasticIPIDs]), id)
			return nil
		})
	}
	p.env.Store.Delete(Name, kindElasticIP, id)
	p.writeOperation(w, p.operationReferring(nounElasticIP, id))
}

func (p *Pack) attachInstanceToElasticIP(w http.ResponseWriter, r *http.Request) {
	instanceID, ok := p.changeInstanceMembership(w, r, kindElasticIP, nounElasticIP, attrElasticIPIDs, true)
	if !ok {
		return
	}
	p.moveElasticRoute(r, instanceID, true)
}

func (p *Pack) detachInstanceFromElasticIP(w http.ResponseWriter, r *http.Request) {
	instanceID, ok := p.changeInstanceMembership(w, r, kindElasticIP, nounElasticIP, attrElasticIPIDs, false)
	if !ok {
		return
	}
	p.moveElasticRoute(r, instanceID, false)
}

// moveElasticRoute routes or unroutes the elastic IP the request names, on the
// instance the membership change just touched.
func (p *Pack) moveElasticRoute(r *http.Request, instanceID string, attach bool) {
	inst, foundInstance := p.env.Store.Get(Name, kindInstance, instanceID)
	eip, foundIP := p.env.Store.Get(Name, kindElasticIP, r.PathValue("id"))
	if !foundInstance || !foundIP {
		return
	}
	address, _ := eip.Attrs["ip"].(string)
	if attach {
		p.routeElasticIP(r.Context(), address, inst)
		return
	}
	p.unrouteElasticIP(r.Context(), address, inst)
}

func elasticIPView(res *resource.Resource) map[string]any {
	ip, _ := res.Attrs["ip"].(string)
	out := map[string]any{
		"id":            res.ID,
		"ip":            ip,
		"cidr":          ip + "/32",
		"addressfamily": res.Attrs["addressfamily"],
	}
	if description, _ := res.Attrs["description"].(string); description != "" {
		out["description"] = description
	}
	if healthcheck, ok := res.Attrs["healthcheck"].(map[string]any); ok {
		out["healthcheck"] = healthcheck
	}
	return out
}

func contains(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// without returns the list minus one value, in the []any form attrs store.
func without(list []string, drop string) []any {
	out := make([]any, 0, len(list))
	for _, item := range list {
		if item != drop {
			out = append(out, item)
		}
	}
	return out
}
