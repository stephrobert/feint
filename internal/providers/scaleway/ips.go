package scaleway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/netip"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/machine"
	"github.com/stephrobert/feint/internal/core/network"
	"github.com/stephrobert/feint/internal/core/resource"
)

// flexibleBlock is where emulated public addresses come from. RFC 5737 reserves
// it for documentation, which matters more than it looks: the runtime routes
// these addresses to machines, and doing that on a real public range would
// capture the host's own traffic towards it.
const flexibleBlock = "203.0.113.0/24"

// kindIP is the store kind for flexible IPs.
const kindIP = "instance/ip"

type createIPRequest struct {
	Project      string   `json:"project"`
	Organization string   `json:"organization"`
	Tags         []string `json:"tags"`
	Server       string   `json:"server"`
	Type         string   `json:"type"`
}

// updateIPRequest carries pointers because the API distinguishes "leave alone"
// from "clear": a null server detaches the address.
//
// Server is json.RawMessage, not *string, and that is the whole fix. The SDK
// sends `{"server": null}` for a detach (NullableStringValue), and
// encoding/json cannot tell JSON null from an absent field through a *string:
// both decode to nil. So `scw instance ip detach` answered 200 and did nothing,
// leaving the address attached — with a runtime on, still routed to the machine
// — while the struct comment above claimed a null server detaches. A comment
// that was not a control, on the defect class this project is built around.
//
// TestDetachingAnAddressActuallyDetachesIt fails without this.
type updateIPRequest struct {
	Reverse *string         `json:"reverse"`
	Server  json.RawMessage `json:"server"`
	Tags    *[]string       `json:"tags"`
}

// serverField reads the three states the API distinguishes: absent (leave
// alone), null or empty (detach), an id (attach).
func serverField(raw json.RawMessage) (id string, present, clears bool) {
	if len(raw) == 0 {
		return "", false, false
	}
	if string(raw) == "null" {
		return "", true, true
	}
	var s string
	if err := json.Unmarshal(raw, &s); err != nil {
		return "", true, false
	}
	return s, true, s == ""
}

func (p *Pack) createIP(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}

	var req createIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}

	project, organization := projectOf(req.Project)

	// Same lock as private addresses: rebuild, allocate, persist is a
	// read-modify-write over the store, and two concurrent creates would
	// otherwise receive the same address.
	p.addresses.Lock()
	defer p.addresses.Unlock()

	address, err := p.allocateFlexibleAddress()
	if err != nil {
		writePrecondition(w, "ip", "", err.Error())
		return
	}

	now := p.env.Now()
	res := &resource.Resource{
		ID:      p.env.NewID(),
		Kind:    kindIP,
		Tenant:  resource.Tenant{Provider: Name, Project: project, Zone: zone},
		State:   "detached",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"address":      address.String(),
			"reverse":      nil,
			"server":       nil,
			"organization": organization,
			"project":      project,
			"tags":         orEmpty(req.Tags),
			"type":         orDefault(req.Type, "routed_ipv4"),
			"prefix":       nil,
			// A flexible IP is an IPAM address upstream, and the SDK carries the
			// link. Serving an empty one would send a client looking for an
			// address that does not exist there.
			"ipam_id": "",
		},
	}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusCreated, map[string]any{"ip": ipView(res)})
}

// allocateFlexibleAddress hands out an address no other flexible IP holds. The
// allocator is rebuilt from the store rather than kept in memory, so a restart
// that reloads a snapshot cannot hand out an address twice.
func (p *Pack) allocateFlexibleAddress() (netip.Addr, error) {
	prefix, err := network.ParseCIDR(flexibleBlock)
	if err != nil {
		return netip.Addr{}, err
	}
	// Two reserved: the network address, and one for symmetry with the private
	// blocks, where the runtime answers on the first usable address.
	alloc, err := network.NewAllocator(prefix, 2)
	if err != nil {
		return netip.Addr{}, err
	}
	for _, res := range p.env.Store.List(kindIP, resource.Tenant{Provider: Name}) {
		if taken, _ := res.Attrs["address"].(string); taken != "" {
			if addr, err := netip.ParseAddr(taken); err == nil {
				_ = alloc.Reserve(addr)
			}
		}
	}
	return alloc.Allocate()
}

func (p *Pack) listIPs(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	all := p.env.Store.List(kindIP, p.scopeOf(r, zone))
	page := parsePage(r)
	start, end := page.slice(len(all))

	ips := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		ips = append(ips, ipView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ips": ips, "total_count": len(all)})
}

// ipByRef resolves the path parameter the SDK documents as "IP ID or address to
// get" — the same for update and delete. Only the id was honoured, so a client
// naming the address got not_found where the real API answers.
//
// TestAnAddressIsAValidIPReference fails without this.
func (p *Pack) ipByRef(ref, zone string) (*resource.Resource, bool) {
	if res, ok := p.env.Store.Get(Name, kindIP, ref); ok && res.Tenant.Zone == zone {
		return res, true
	}
	for _, res := range p.env.Store.List(kindIP, resource.Tenant{Provider: Name, Zone: zone}) {
		if address, _ := res.Attrs["address"].(string); address == ref {
			return res, true
		}
	}
	return nil, false
}

func (p *Pack) getIP(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	res, found := p.ipByRef(id, zone)
	if !found {
		writeNotFound(w, "ip", id)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ip": ipView(res)})
}

// updateIP attaches a flexible IP to a server, or detaches it.
//
// Attaching is what makes the address mean something: the runtime routes it to
// the machine, so a client that reads the public address from the API and
// connects to it reaches the server. Emulators usually stop at the record, and
// floci goes as far as reporting 127.0.0.1 as every instance's public address.
func (p *Pack) updateIP(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	res, found := p.ipByRef(id, zone)
	if !found {
		writeNotFound(w, "ip", id)
		return
	}

	var req updateIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Tags != nil {
		res.Attrs["tags"] = orEmpty(*req.Tags)
	}
	if req.Reverse != nil {
		res.Attrs["reverse"] = *req.Reverse
	}

	// A null server detaches, which is how the API frees an address.
	serverID, serverGiven, clears := serverField(req.Server)
	if serverGiven {
		if clears {
			p.detachAddress(r.Context(), res)
			res.Attrs["server"] = nil
			res.State = "detached"
		} else {
			server, ok := p.env.Store.Get(Name, kindServer, serverID)
			if !ok || server.Tenant.Zone != zone {
				writeNotFound(w, "server", serverID)
				return
			}
			// Moving the address means taking it back first: the route lives on
			// the previous machine's device, and attaching elsewhere without
			// unrouting leaves two machines claiming the same /32, the old one
			// winning or losing by ARP order.
			if summary, _ := res.Attrs["server"].(map[string]any); summary != nil {
				if previous, _ := summary["id"].(string); previous != "" && previous != server.ID {
					p.detachAddress(r.Context(), res)
				}
			}
			name, _ := server.Attrs["name"].(string)
			res.Attrs["server"] = map[string]any{"id": server.ID, "name": name}
			res.State = "attached"
			p.attachAddress(r.Context(), res, server)
		}
	}

	// attachAddress talks to the runtime outside the lock, so a release that
	// landed meanwhile must not be undone here.
	if !p.env.Store.Commit(res, p.env.Now()) {
		writeNotFound(w, "ip", res.ID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ip": ipView(res)})
}

// attachAddress asks the runtime to route the emulated address to the machine.
// Degrades quietly, like every other runtime call in this pack: the control
// plane keeps describing the attachment, and the log says why nothing answers.
func (p *Pack) attachAddress(ctx context.Context, ip, server *resource.Resource) {
	router, ok := p.env.Machines.(machine.Router)
	if !ok {
		return
	}
	name := server.Runtime[runtimeMachineKey]
	address, _ := ip.Attrs["address"].(string)
	if name == "" || address == "" {
		return
	}
	// On the network the server lives on, when it has one: a public address on
	// the runtime's default bridge would answer, and would sit on an interface
	// the emulated topology says nothing about.
	if err := router.RouteAddress(ctx, machine.AddressSpec{
		Machine: name,
		Address: address,
		Network: p.privateNetworkNameOf(server),
	}); err != nil {
		p.logger().Error("could not route the flexible IP to the machine",
			"ip", ip.ID, "address", address, "server", server.ID, "error", err)
	}
}

// privateNetworkNameOf returns the runtime network of the server's first
// private NIC, empty when it sits on none.
func (p *Pack) privateNetworkNameOf(server *resource.Resource) string {
	for _, nic := range p.privateNICsOf(server.ID) {
		pn, found := p.env.Store.Get(Name, kindPrivateNetwork, nic.Runtime[runtimePrivateNetworkKey])
		if found && pn.Runtime[runtimeNetworkKey] != "" {
			return pn.Runtime[runtimeNetworkKey]
		}
	}
	return ""
}

func (p *Pack) detachAddress(ctx context.Context, ip *resource.Resource) {
	router, ok := p.env.Machines.(machine.Router)
	if !ok {
		return
	}
	address, _ := ip.Attrs["address"].(string)
	if address == "" {
		return
	}
	// The machine that held it, when the record still names one.
	holder := ""
	if summary, _ := ip.Attrs["server"].(map[string]any); summary != nil {
		if id, _ := summary["id"].(string); id != "" {
			if server, found := p.env.Store.Get(Name, kindServer, id); found {
				holder = server.Runtime[runtimeMachineKey]
			}
		}
	}
	if err := router.UnrouteAddress(ctx, holder, address); err != nil {
		p.logger().Error("could not stop routing the flexible IP",
			"ip", ip.ID, "address", address, "error", err)
	}
}

func (p *Pack) deleteIP(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	id := r.PathValue("id")
	res, found := p.ipByRef(id, zone)
	if !found {
		writeNotFound(w, "ip", id)
		return
	}
	// The address stops being routed with the record that described it.
	p.detachAddress(r.Context(), res)
	p.env.Store.Delete(Name, kindIP, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

func ipView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+4)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["id"] = res.ID
	out["zone"] = res.Tenant.Zone
	out["state"] = res.State
	return out
}

// releaseAddressesOf detaches every flexible IP a server carried.
//
// Called when the server goes away, by delete and by terminate alike: an address
// naming a resource that no longer exists is the defect this project exists to
// avoid, and it is worse with a machine runtime, where the route outlives the
// machine.
//
// TestDeletingAServerReleasesItsAddresses fails without this.
func (p *Pack) releaseAddressesOf(ctx context.Context, serverID, zone string) {
	for _, ip := range p.env.Store.List(kindIP, resource.Tenant{Provider: Name, Zone: zone}) {
		attached, _ := ip.Attrs["server"].(map[string]any)
		if attached == nil || attached["id"] != serverID {
			continue
		}
		p.detachAddress(ctx, ip)
		ip.Attrs["server"] = nil
		ip.State = "detached"
		p.env.Store.Commit(ip, p.env.Now())
	}
}
