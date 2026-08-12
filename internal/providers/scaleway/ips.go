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
	// Dynamic addresses draw from the same block, and live on the server rather
	// than as IP resources — upstream, a dynamic address is not a flexible IP
	// and never appears in /ips. Skipping them here would hand a flexible IP an
	// address a running server already answers on.
	for _, srv := range p.env.Store.List(kindServer, resource.Tenant{Provider: Name}) {
		if taken := srv.Runtime[runtimeDynamicIPKey]; taken != "" {
			if addr, err := netip.ParseAddr(taken); err == nil {
				_ = alloc.Reserve(addr)
			}
		}
	}
	return alloc.Allocate()
}

// emulatedAddress reports whether an address is one this pack can have handed
// out: inside flexibleBlock.
//
// It is the gate every address must pass on its way to the driver, and it
// exists because the values it filters come from the store: a flexible IP's
// address and a server's dynamic address are restored verbatim by
// PUT /_feint/state and `feint snapshot load`. Routing an arbitrary address to
// a machine would make the host send its traffic for that address — an
// operator's LAN peer, a real service — into a container. Well-formed is not
// authorised; this is the authorisation half.
//
// TestAPoisonedStoredAddressIsNeverRouted fails without it.
func emulatedAddress(address string) bool {
	prefix, err := netip.ParsePrefix(flexibleBlock)
	if err != nil {
		return false
	}
	addr, err := netip.ParseAddr(address)
	if err != nil {
		return false
	}
	return prefix.Contains(addr)
}

// attachedIPsOf lists the flexible IPs attached to a server. One walk for the
// view, the release path, the boot and the replay — four callers, one loop,
// because the copies had already started to disagree once (an audit found the
// create path and updateIP setting different halves of the same link).
func (p *Pack) attachedIPsOf(serverID, zone string) []*resource.Resource {
	out := make([]*resource.Resource, 0, 1)
	for _, ip := range p.env.Store.List(kindIP, resource.Tenant{Provider: Name, Zone: zone}) {
		attached, _ := ip.Attrs["server"].(map[string]any)
		if attached != nil && attached["id"] == serverID {
			out = append(out, ip)
		}
	}
	return out
}

// publicAddressesOf is every public address the server has been promised: its
// attached flexible IPs, and the dynamic address when it holds one. This is
// what rides the launch, so the machine answers on them from its first boot.
func (p *Pack) publicAddressesOf(server *resource.Resource) []string {
	ips := p.attachedIPsOf(server.ID, server.Tenant.Zone)
	out := make([]string, 0, len(ips)+1)
	for _, ip := range ips {
		if address, _ := ip.Attrs["address"].(string); emulatedAddress(address) {
			out = append(out, address)
		}
	}
	if address := server.Runtime[runtimeDynamicIPKey]; emulatedAddress(address) {
		out = append(out, address)
	}
	return out
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
	address, _ := ip.Attrs["address"].(string)
	p.routeAddress(ctx, address, server)
}

// routeAddress routes one public address — flexible or dynamic — to the
// server's machine. Idempotent by the Router contract, which is what lets the
// poweron replay call it on addresses the launch already installed.
func (p *Pack) routeAddress(ctx context.Context, address string, server *resource.Resource) {
	router, ok := p.env.Machines.(machine.Router)
	if !ok {
		return
	}
	name := server.Runtime[runtimeMachineKey]
	if name == "" || address == "" {
		return
	}
	// A stored address is untrusted input: a restored snapshot carries it
	// verbatim, and routing an arbitrary value would send the host's traffic
	// for that address into a container. See emulatedAddress.
	if !emulatedAddress(address) {
		p.logger().Warn("refusing to route an address outside the emulated public block",
			"address", address, "server", server.ID)
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
		p.logger().Error("could not route the public address to the machine",
			"address", address, "server", server.ID, "error", err)
	}
}

// routeServerAddresses (re)routes every public address a server holds.
//
// Called after the machine exists: at poweron, and on the read that discovers
// a late address. The boot itself installs the host half (the route keys ride
// the launch); this replay is what hands the guest its addresses, and what
// repairs a machine that already existed with different attachments.
//
// It is the missing half of #116: an address attached at create was recorded
// and never routed, because attachAddress ran while the server had no machine
// and nothing replayed it once one existed.
// TestPowerOnRoutesAnAddressAttachedBeforeBoot fails without it.
func (p *Pack) routeServerAddresses(ctx context.Context, res *resource.Resource) {
	for _, ip := range p.attachedIPsOf(res.ID, res.Tenant.Zone) {
		p.attachAddress(ctx, ip, res)
	}
	if address := res.Runtime[runtimeDynamicIPKey]; address != "" {
		p.routeAddress(ctx, address, res)
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
	for _, ip := range p.attachedIPsOf(serverID, zone) {
		p.detachAddress(ctx, ip)
		ip.Attrs["server"] = nil
		ip.State = "detached"
		p.env.Store.Commit(ip, p.env.Now())
	}
}

// ---- Dynamic addresses ------------------------------------------------------
//
// `dynamic_ip_required` upstream gives the server an ephemeral public address
// at poweron and takes it back when the server stops; the address is not a
// flexible IP and never appears in /ips. The pack used to decode the flag,
// echo it back, and allocate nothing — which no report could see, because the
// field *was* read (#117).

// runtimeDynamicIPKey is where a server's dynamic address is kept, and
// runtimeDynamicIPIDKey the identifier public_ips publishes for it. Runtime,
// not Attrs: the address is surfaced through the ServerIP view only, the way
// the machine name and the runtime address already are.
const (
	runtimeDynamicIPKey   = "dynamic-ip"
	runtimeDynamicIPIDKey = "dynamic-ip-id"
)

// ensureDynamicAddress allocates the ephemeral address a poweron owes a server
// whose dynamic_ip_required is set, when no flexible IP already covers it —
// upstream's own precedence: a reserved address suppresses the dynamic one.
//
// The allocation is committed to the store at once, not left for the caller's
// final write-back: the allocator is rebuilt from the store, and a machine
// takes seconds to boot, so an uncommitted reservation is an address a
// concurrent POST /ips hands out a second time.
func (p *Pack) ensureDynamicAddress(res *resource.Resource) {
	if want, _ := res.Attrs["dynamic_ip_required"].(bool); !want {
		return
	}
	if res.Runtime[runtimeDynamicIPKey] != "" {
		return
	}
	if len(p.attachedIPsOf(res.ID, res.Tenant.Zone)) > 0 {
		return
	}

	p.addresses.Lock()
	defer p.addresses.Unlock()
	address, err := p.allocateFlexibleAddress()
	if err != nil {
		p.logger().Error("could not allocate a dynamic address",
			"server", res.ID, "error", err)
		return
	}
	if res.Runtime == nil {
		res.Runtime = map[string]string{}
	}
	res.Runtime[runtimeDynamicIPKey] = address.String()
	res.Runtime[runtimeDynamicIPIDKey] = p.env.NewID()
	p.env.Store.Commit(res, p.env.Now())
}

// releaseDynamicAddress takes the ephemeral address back: on poweroff, standby,
// terminate and delete alike. Upstream releases it on stop — it is the whole
// difference between dynamic and flexible — and holding it here would publish
// an address a stopped machine no longer answers on.
//
// The store write stays with the caller, which either commits the resource or
// deletes it; what must not wait is the unroute, because on OVN the uplink
// route outlives the machine.
func (p *Pack) releaseDynamicAddress(ctx context.Context, res *resource.Resource) {
	address := res.Runtime[runtimeDynamicIPKey]
	if address == "" {
		return
	}
	delete(res.Runtime, runtimeDynamicIPKey)
	delete(res.Runtime, runtimeDynamicIPIDKey)
	router, ok := p.env.Machines.(machine.Router)
	if !ok || !emulatedAddress(address) {
		return
	}
	if err := router.UnrouteAddress(ctx, res.Runtime[runtimeMachineKey], address); err != nil {
		p.logger().Error("could not stop routing the dynamic address",
			"address", address, "server", res.ID, "error", err)
	}
}
