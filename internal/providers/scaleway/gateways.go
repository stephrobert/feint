package scaleway

import (
	"net/http"
	"net/netip"
	"strings"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/network"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Public Gateways (vpc-gw/v2), served exactly as far as the measured clients
// exercise them (#282, #18).
//
// The demand is the surveyed terraform-talos stack plus Scaleway's own VPC
// module, whose gateway path is `scaleway_vpc_public_gateway_ip` →
// `scaleway_ipam_ip` → `scaleway_vpc_public_gateway` →
// `scaleway_vpc_gateway_network` with `ipam_config`. What the provider calls
// was read in its own services/vpcgw (v2.81.0: CreateIP/GetIP/UpdateIP/
// DeleteIP, CreateGateway/GetGateway + WaitForGateway polling GetGateway,
// UpdateGateway, DeleteGateway, CreateGatewayNetwork/GetGatewayNetwork/
// UpdateGatewayNetwork/DeleteGatewayNetwork, and ListIPs on /ipam/v1 to read
// the connection's private address back), then confirmed on the wire with
// `feint proxy --record`. `scw` 2.56.3 drives the same v2 family and adds the
// three lists.
//
// Only v2 is served. The v1 family is declined wholesale in pack.go: the
// portal publishes no v1 document any more (measured 2026-08-19:
// /en/developers/api/public-gateway offers v2 only), and every route mounted
// in this pack is checked against the portal's document — the same reason
// vpc/v2's EnableCustomRoutesPropagation is declined. A client pinned to v1
// (Terraform provider ≤ 2.51) meets a named 501, not a plain 404.
//
// What this control plane does NOT do, stated rather than implied: no packet
// is NATed, no default route reaches a machine, and the bastion accepts no
// connection. The gateway records the configuration a client asked for and
// answers it back exactly; docs/limits.md carries the statement. That is the
// same decision the Outscale LBU took in #281/#315: a 200 must not let a
// client believe traffic flows when nothing carries it.

const (
	kindGatewayIP      = "vpcgw/ip"
	kindGateway        = "vpcgw/gateway"
	kindGatewayNetwork = "vpcgw/gateway-network"
)

// gatewayBlock is where emulated gateway addresses come from: TEST-NET-1,
// distinct from the instance flexible block (203.0.113.0/24) and the Load
// Balancer block (198.51.100.0/24) so no product ever publishes an address
// another product also claims.
const gatewayBlock = "192.0.2.0/24"

// gatewayTypes is the commercial offer catalogue, from Scaleway's public
// pricing page (VPC-GW-S/M/L/XL). Serving a whitelist rather than echoing any
// string follows the catalogue rule the instance types learned in #279: the
// real API refuses an unknown offer, and an emulator that accepts one lets a
// plan pass that production would refuse.
var gatewayTypes = map[string]bool{
	"VPC-GW-S":  true,
	"VPC-GW-M":  true,
	"VPC-GW-L":  true,
	"VPC-GW-XL": true,
}

type createGatewayIPRequest struct {
	ProjectID string   `json:"project_id"`
	Tags      []string `json:"tags"`
}

type updateGatewayIPRequest struct {
	Tags      *[]string `json:"tags"`
	Reverse   *string   `json:"reverse"`
	GatewayID *string   `json:"gateway_id"`
}

type createGatewayRequest struct {
	ProjectID     string   `json:"project_id"`
	Name          string   `json:"name"`
	Tags          []string `json:"tags"`
	Type          string   `json:"type"`
	IPID          *string  `json:"ip_id"`
	EnableSMTP    bool     `json:"enable_smtp"`
	EnableBastion bool     `json:"enable_bastion"`
	BastionPort   *uint32  `json:"bastion_port"`
}

type updateGatewayRequest struct {
	Name          *string   `json:"name"`
	Tags          *[]string `json:"tags"`
	EnableBastion *bool     `json:"enable_bastion"`
	BastionPort   *uint32   `json:"bastion_port"`
	EnableSMTP    *bool     `json:"enable_smtp"`
}

type createGatewayNetworkRequest struct {
	GatewayID        string  `json:"gateway_id"`
	PrivateNetworkID string  `json:"private_network_id"`
	EnableMasquerade bool    `json:"enable_masquerade"`
	PushDefaultRoute bool    `json:"push_default_route"`
	IpamIPID         *string `json:"ipam_ip_id"`
}

type updateGatewayNetworkRequest struct {
	EnableMasquerade *bool   `json:"enable_masquerade"`
	PushDefaultRoute *bool   `json:"push_default_route"`
	IpamIPID         *string `json:"ipam_ip_id"`
}

// zoneProjectScopeOf mirrors regionScopeOf for the zonal products that spell
// the filter project_id (vpc-gw, lb), where instance/v1 spells it project.
//
// A request that names no project is scoped to the zone and not to
// defaultProject, and listBlockVolumes already states the rule this now
// shares: "comparing the identifier against the pack's constant would deny a
// client its own volumes for a configuration detail". Substituting the
// constant did exactly that here — a client that creates an address under its
// own project and then lists without a filter got an empty page, where the
// real cloud answers the token's project. Measured on
// corpus/scaleway/scw-billed-shapes.jsonl seq 12 and 37: two ListIPs, both
// answering one address upstream and none here.
//
// TestAListWithoutAProjectFilterAnswersWhatTheClientCreated fails without this.
func (p *Pack) zoneProjectScopeOf(r *http.Request, zone string) resource.Tenant {
	q := r.URL.Query()
	// organization_id names the account, and one organization lives here
	// (scopeOf's rule), so it narrows to the zone. Read rather than ignored:
	// a declared query parameter its handler never names is a parameter
	// silently dropped, which #277 turned into a gate.
	if q.Get("organization_id") != "" {
		return resource.Tenant{Provider: Name, Zone: zone}
	}
	if project := q.Get("project_id"); project != "" {
		return resource.Tenant{Provider: Name, Project: project, Zone: zone}
	}
	return resource.Tenant{Provider: Name, Zone: zone}
}

// zonalResourceOf resolves a zonal resource by path segment, writing the error.
func (p *Pack) zonalResourceOf(w http.ResponseWriter, r *http.Request, kind, segment, label string) (*resource.Resource, bool) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return nil, false
	}
	id := r.PathValue(segment)
	res, found := p.env.Store.Get(Name, kind, id)
	if !found || res.Tenant.Zone != zone {
		writeNotFound(w, label, id)
		return nil, false
	}
	return res, true
}

// ---- IPs --------------------------------------------------------------------

func (p *Pack) listGatewayIPs(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	all := p.env.Store.List(kindGatewayIP, p.zoneProjectScopeOf(r, zone))
	q := r.URL.Query()
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasAllTags(res, tags)
		})
	}
	if isFree, present := queryBool(q, "is_free"); present {
		all = filterResources(all, func(res *resource.Resource) bool {
			gw, _ := res.Attrs["gateway_id"].(string)
			return (gw == "") == isFree
		})
	}
	if reverse := q.Get("reverse"); reverse != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			held, _ := res.Attrs["reverse"].(string)
			return strings.Contains(held, reverse)
		})
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"ip":         cmpGatewayAddress,
	}, all) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	ips := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		ips = append(ips, p.gatewayIPView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"ips":         ips,
		"total_count": len(all),
	})
}

func cmpGatewayAddress(a, b *resource.Resource) int {
	pa, errA := netip.ParseAddr(textOf(a.Attrs["address"]))
	pb, errB := netip.ParseAddr(textOf(b.Attrs["address"]))
	if errA != nil || errB != nil {
		return strings.Compare(textOf(a.Attrs["address"]), textOf(b.Attrs["address"]))
	}
	return pa.Compare(pb)
}

func (p *Pack) createGatewayIP(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req createGatewayIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	project, _ := projectOf(req.ProjectID)

	unlock := p.lockAddresses()
	defer unlock()
	res, err := p.mintGatewayIP(zone, project, orEmpty(req.Tags))
	if err != nil {
		writePrecondition(w, "ip", "", err.Error())
		return
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, p.gatewayIPView(res))
}

// mintGatewayIP allocates an address of the gateway block and builds the
// resource. The caller holds the allocation lock and stores the result.
func (p *Pack) mintGatewayIP(zone, project string, tags []any) (*resource.Resource, error) {
	prefix, err := network.ParseCIDR(gatewayBlock)
	if err != nil {
		return nil, err
	}
	alloc, err := network.NewAllocator(prefix, 0)
	if err != nil {
		return nil, err
	}
	for _, res := range p.env.Store.List(kindGatewayIP, resource.Tenant{Provider: Name}) {
		if taken, err := netip.ParseAddr(textOf(res.Attrs["address"])); err == nil {
			_ = alloc.Reserve(taken)
		}
	}
	addr, err := alloc.Allocate()
	if err != nil {
		return nil, err
	}
	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindGatewayIP, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "detached", now)
	res.Attrs = map[string]any{
		"address":    addr.String(),
		"project_id": project,
		"tags":       tags,
		// The empty string and not null, which is what the sibling lb IP
		// already answers. The recording settled the type and not the value:
		// corpus/scaleway/scw-billed-shapes.jsonl seq 34-37 shows fr-par
		// answering a reverse on a freshly created gateway address, and the
		// sanitiser replaced the name itself, so there is nothing to copy. An
		// invented hostname would be the fabricated format this repository
		// refuses; null is the one answer the recording rules out, since a
		// client decoding *string finds nothing where the cloud always has a
		// name. UpdateIP still carries a real one when a client sets it.
		//
		// TestAGatewayAddressAnswersAReverseOfTheRecordedType fails without this.
		"reverse":    "",
		"gateway_id": "",
	}
	return res, nil
}

func (p *Pack) getGatewayIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindGatewayIP, "ipID", "ip")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.gatewayIPView(res))
}

func (p *Pack) updateGatewayIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindGatewayIP, "ipID", "ip")
	if !ok {
		return
	}
	var req updateGatewayIPRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.GatewayID != nil && *req.GatewayID != "" {
		if _, found := p.env.Store.Get(Name, kindGateway, *req.GatewayID); !found {
			writeNotFound(w, "gateway", *req.GatewayID)
			return
		}
	}
	err := p.env.Store.Update(Name, kindGatewayIP, res.ID, func(stored *resource.Resource) error {
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		if req.Reverse != nil {
			stored.Attrs["reverse"] = *req.Reverse
		}
		if req.GatewayID != nil {
			stored.Attrs["gateway_id"] = *req.GatewayID
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "ip", res.ID)
		return
	}
	current, _ := p.env.Store.Get(Name, kindGatewayIP, res.ID)
	emulator.WriteJSON(w, http.StatusOK, p.gatewayIPView(current))
}

func (p *Pack) deleteGatewayIP(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindGatewayIP, "ipID", "ip")
	if !ok {
		return
	}
	if gw, _ := res.Attrs["gateway_id"].(string); gw != "" {
		writePrecondition(w, "ip", res.ID, "IP is attached to a gateway; delete the gateway first")
		return
	}
	p.env.Store.Delete(Name, kindGatewayIP, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

func (p *Pack) gatewayIPView(res *resource.Resource) map[string]any {
	var gatewayID any
	if id, _ := res.Attrs["gateway_id"].(string); id != "" {
		gatewayID = id
	}
	return map[string]any{
		"id":              res.ID,
		"organization_id": defaultOrganization,
		"project_id":      res.Attrs["project_id"],
		"created_at":      res.Created.Format(time.RFC3339),
		"updated_at":      res.Updated.Format(time.RFC3339),
		"tags":            res.Attrs["tags"],
		"address":         res.Attrs["address"],
		"reverse":         res.Attrs["reverse"],
		"gateway_id":      gatewayID,
		"zone":            res.Tenant.Zone,
	}
}

// ---- Gateways ---------------------------------------------------------------

func (p *Pack) listGateways(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	all := p.env.Store.List(kindGateway, p.zoneProjectScopeOf(r, zone))
	q := r.URL.Query()
	if name := q.Get("name"); name != "" {
		all = filterResources(all, func(res *resource.Resource) bool {
			return strings.Contains(textOf(res.Attrs["name"]), name)
		})
	}
	if tags := csvValues(q, "tags"); len(tags) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return hasAllTags(res, tags)
		})
	}
	if types := csvValues(q, "types"); len(types) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return contains(types, textOf(res.Attrs["type"]))
		})
	}
	if statuses := csvValues(q, "status"); len(statuses) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return contains(statuses, res.State)
		})
	}
	if ids := idSet(q, "private_network_ids"); ids != nil {
		all = filterResources(all, func(res *resource.Resource) bool {
			for _, gn := range p.env.Store.List(kindGatewayNetwork, resource.Tenant{Provider: Name}) {
				if gn.Attrs["gateway_id"] == res.ID && ids[textOf(gn.Attrs["private_network_id"])] {
					return true
				}
			}
			return false
		})
	}
	// include_legacy widens the answer with the gateways still on non-IPAM
	// configurations. Every gateway served here is IPAM-based (is_legacy is
	// always false), so the widened set and the default one are the same set;
	// the parameter is read so that sending it is never silently dropped.
	_ = q.Get("include_legacy")
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"name":       cmpName,
		"type": func(a, b *resource.Resource) int {
			return strings.Compare(textOf(a.Attrs["type"]), textOf(b.Attrs["type"]))
		},
		"status": func(a, b *resource.Resource) int { return strings.Compare(a.State, b.State) },
	}, all) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	gateways := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		gateways = append(gateways, p.gatewayView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"gateways":    gateways,
		"total_count": len(all),
	})
}

func (p *Pack) createGateway(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req createGatewayRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if !gatewayTypes[strings.ToUpper(req.Type)] {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "type",
			Reason:       "constraint",
			HelpMessage:  "unknown gateway type " + req.Type + "; the emulated offers are VPC-GW-S, VPC-GW-M, VPC-GW-L and VPC-GW-XL",
		})
		return
	}
	project, _ := projectOf(req.ProjectID)

	// The address before the gateway, both under the allocation lock: a
	// gateway must never answer without its IP, and two concurrent creates
	// must not mint the same address.
	unlock := p.lockAddresses()
	defer unlock()

	var ip *resource.Resource
	if req.IPID != nil && *req.IPID != "" {
		existing, found := p.env.Store.Get(Name, kindGatewayIP, *req.IPID)
		if !found || existing.Tenant.Zone != zone {
			writeNotFound(w, "ip", *req.IPID)
			return
		}
		if held, _ := existing.Attrs["gateway_id"].(string); held != "" {
			writePrecondition(w, "ip", existing.ID, "IP is already attached to a gateway")
			return
		}
		ip = existing
	} else {
		// "If not set, the emulator mints one", exactly as upstream creates
		// and attaches a new address when the request names none.
		minted, err := p.mintGatewayIP(zone, project, []any{})
		if err != nil {
			writePrecondition(w, "ip", "", err.Error())
			return
		}
		p.env.Store.Put(minted)
		ip = minted
	}

	now := p.env.Now()
	id := p.env.NewID()
	var bastionPort uint32
	if req.BastionPort != nil {
		bastionPort = *req.BastionPort
	}
	res := resource.New(id, kindGateway, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "running", now)
	res.Attrs = map[string]any{
		"name":            orDefault(req.Name, "gw-"+id[:8]),
		"type":            req.Type,
		"tags":            orEmpty(req.Tags),
		"project_id":      project,
		"ip_id":           ip.ID,
		"bastion_enabled": req.EnableBastion,
		"bastion_port":    bastionPort,
		"smtp_enabled":    req.EnableSMTP,
	}
	p.env.Store.Put(res)
	_ = p.env.Store.Update(Name, kindGatewayIP, ip.ID, func(stored *resource.Resource) error {
		stored.Attrs["gateway_id"] = id
		stored.Updated = now
		return nil
	})

	current, _ := p.env.Store.Get(Name, kindGateway, id)
	emulator.WriteJSON(w, http.StatusOK, p.gatewayView(current))
}

func (p *Pack) getGateway(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindGateway, "gatewayID", "gateway")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.gatewayView(res))
}

func (p *Pack) updateGateway(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindGateway, "gatewayID", "gateway")
	if !ok {
		return
	}
	var req updateGatewayRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	err := p.env.Store.Update(Name, kindGateway, res.ID, func(stored *resource.Resource) error {
		if req.Name != nil {
			stored.Attrs["name"] = *req.Name
		}
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		if req.EnableBastion != nil {
			stored.Attrs["bastion_enabled"] = *req.EnableBastion
		}
		if req.BastionPort != nil {
			stored.Attrs["bastion_port"] = *req.BastionPort
		}
		if req.EnableSMTP != nil {
			stored.Attrs["smtp_enabled"] = *req.EnableSMTP
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "gateway", res.ID)
		return
	}
	current, _ := p.env.Store.Get(Name, kindGateway, res.ID)
	emulator.WriteJSON(w, http.StatusOK, p.gatewayView(current))
}

func (p *Pack) deleteGateway(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindGateway, "gatewayID", "gateway")
	if !ok {
		return
	}
	// A connected gateway does not vanish under its Private Networks: the
	// Terraform provider always deletes the GatewayNetworks first, and a
	// client that did not gets a retryable refusal instead of dangling
	// connections. TestDeletingAConnectedGatewayIsRefused fails without this.
	for _, gn := range p.env.Store.List(kindGatewayNetwork, resource.Tenant{Provider: Name}) {
		if gn.Attrs["gateway_id"] == res.ID {
			writePrecondition(w, "gateway", res.ID, "gateway is still attached to a private network; delete the gateway network first")
			return
		}
	}

	view := p.gatewayView(res)
	view["status"] = "deleting"
	p.env.Store.Delete(Name, kindGateway, res.ID)

	ipID, _ := res.Attrs["ip_id"].(string)
	if r.URL.Query().Get("delete_ip") == "true" {
		p.env.Store.Delete(Name, kindGatewayIP, ipID)
	} else if ipID != "" {
		_ = p.env.Store.Update(Name, kindGatewayIP, ipID, func(stored *resource.Resource) error {
			stored.Attrs["gateway_id"] = ""
			stored.Updated = p.env.Now()
			return nil
		})
	}
	// v2's DeleteGateway answers the deleted gateway rather than 204; the
	// provider then polls GetGateway until the 404 says it is gone.
	emulator.WriteJSON(w, http.StatusOK, view)
}

func (p *Pack) gatewayView(res *resource.Resource) map[string]any {
	var ipv4 any
	if ipID, _ := res.Attrs["ip_id"].(string); ipID != "" {
		if ip, found := p.env.Store.Get(Name, kindGatewayIP, ipID); found {
			ipv4 = p.gatewayIPView(ip)
		}
	}
	networks := make([]map[string]any, 0, 1)
	for _, gn := range p.env.Store.List(kindGatewayNetwork, resource.Tenant{Provider: Name}) {
		if gn.Attrs["gateway_id"] == res.ID {
			networks = append(networks, p.gatewayNetworkView(gn))
		}
	}
	return map[string]any{
		"id":              res.ID,
		"organization_id": defaultOrganization,
		"project_id":      res.Attrs["project_id"],
		"created_at":      res.Created.Format(time.RFC3339),
		"updated_at":      res.Updated.Format(time.RFC3339),
		"type":            res.Attrs["type"],
		// Zero, and deliberately: the offer's real bandwidth is the provider's
		// inventory, and nothing here shapes a packet. Publishing the
		// commercial figure would claim a capacity this emulator does not
		// carry (the GetDashboard argument, applied to one field).
		"bandwidth":        0,
		"status":           res.State,
		"name":             res.Attrs["name"],
		"tags":             res.Attrs["tags"],
		"ipv4":             ipv4,
		"gateway_networks": networks,
		// version is absent rather than null, and DeclinedFields carries the
		// reason. The distinction is what makes the decline work at all: a
		// field decline excuses a field the answer does not carry, so a key
		// present with a null value is a *type* divergence no decline can
		// reach — the emulator would be claiming "no version" in a shape the
		// cloud never answers. Absent, the decline states the decision and
		// the gate holds it to being true.
		"can_upgrade_to":  nil,
		"bastion_enabled": res.Attrs["bastion_enabled"],
		"bastion_port":    res.Attrs["bastion_port"],
		"smtp_enabled":    res.Attrs["smtp_enabled"],
		"is_legacy":       false,
		// Served empty until SetBastionAllowedIPs is: the bastion accepts no
		// connection here, and a recorded allow-list nothing enforces is the
		// exact shape docs/limits.md warns about.
		"bastion_allowed_ips": []any{},
		"zone":                res.Tenant.Zone,
	}
}

// ---- Gateway networks ---------------------------------------------------------

func (p *Pack) listGatewayNetworks(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	all := p.env.Store.List(kindGatewayNetwork, resource.Tenant{Provider: Name, Zone: zone})
	q := r.URL.Query()
	if ids := idSet(q, "gateway_ids"); ids != nil {
		all = filterResources(all, func(res *resource.Resource) bool {
			return ids[textOf(res.Attrs["gateway_id"])]
		})
	}
	if ids := idSet(q, "private_network_ids"); ids != nil {
		all = filterResources(all, func(res *resource.Resource) bool {
			return ids[textOf(res.Attrs["private_network_id"])]
		})
	}
	if masquerade, present := queryBool(q, "masquerade_enabled"); present {
		all = filterResources(all, func(res *resource.Resource) bool {
			enabled, _ := res.Attrs["masquerade_enabled"].(bool)
			return enabled == masquerade
		})
	}
	if statuses := csvValues(q, "status"); len(statuses) > 0 {
		all = filterResources(all, func(res *resource.Resource) bool {
			return contains(statuses, res.State)
		})
	}
	if !orderResources(w, r, "order_by", "created_at_asc", map[string]resourceCmp{
		"created_at": cmpCreated,
		"status":     func(a, b *resource.Resource) int { return strings.Compare(a.State, b.State) },
	}, all) {
		return
	}
	page := parsePage(r)
	start, end := page.slice(len(all))
	networks := make([]map[string]any, 0, end-start)
	for _, res := range all[start:end] {
		networks = append(networks, p.gatewayNetworkView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"gateway_networks": networks,
		"total_count":      len(all),
	})
}

func (p *Pack) createGatewayNetwork(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	var req createGatewayNetworkRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	gw, found := p.env.Store.Get(Name, kindGateway, req.GatewayID)
	if !found || gw.Tenant.Zone != zone {
		writeNotFound(w, "gateway", req.GatewayID)
		return
	}
	pn, found := p.env.Store.Get(Name, kindPrivateNetwork, req.PrivateNetworkID)
	if !found {
		writeNotFound(w, "private_network", req.PrivateNetworkID)
		return
	}

	// One connection per gateway-network pair, checked and written under the
	// allocation lock so two concurrent creates cannot both pass (#289's
	// shape: an acknowledged duplicate one of the two loses silently).
	unlock := p.lockAddresses()
	defer unlock()
	for _, gn := range p.env.Store.List(kindGatewayNetwork, resource.Tenant{Provider: Name}) {
		if gn.Attrs["gateway_id"] == gw.ID && gn.Attrs["private_network_id"] == pn.ID {
			writePrecondition(w, "gateway_network", gn.ID, "the gateway is already attached to this private network")
			return
		}
	}

	id := p.env.NewID()
	project, _ := pn.Attrs["project_id"].(string)
	var ipamIP *resource.Resource
	if req.IpamIPID != nil && *req.IpamIPID != "" {
		booked, found := p.env.Store.Get(Name, kindIPAMIP, *req.IpamIPID)
		if !found {
			writeNotFound(w, "ip", *req.IpamIPID)
			return
		}
		if booked.Attrs["private_network_id"] != pn.ID {
			writeInvalidArguments(w, ArgumentError{
				ArgumentName: "ipam_ip_id",
				Reason:       "constraint",
				HelpMessage:  "the booked address does not live in the private network being attached",
			})
			return
		}
		addr, err := netip.ParsePrefix(textOf(booked.Attrs["address"]))
		if err != nil {
			writeInvalidArguments(w, ArgumentError{ArgumentName: "ipam_ip_id", Reason: "constraint", HelpMessage: err.Error()})
			return
		}
		if err := p.holdIPAMIP(booked.ID, resourceTypeGatewayNetwork, id, "", macAddressOf(addr.Addr())); err != nil {
			writePrecondition(w, "ip", booked.ID, "IP is already attached to a resource")
			return
		}
		current, _ := p.env.Store.Get(Name, kindIPAMIP, booked.ID)
		ipamIP = current
	} else {
		// "When null, a new private IP address is created": same pool as the
		// NICs and the booked addresses, same allocator, same lock.
		alloc, err := p.allocatorFor(pn)
		if err != nil {
			writeInvalidArguments(w, ArgumentError{ArgumentName: "private_network_id", Reason: "constraint", HelpMessage: err.Error()})
			return
		}
		addr, err := alloc.Allocate()
		if err != nil {
			writePrecondition(w, "private_network", pn.ID, "no address left in "+alloc.Prefix().String())
			return
		}
		prefix := netip.PrefixFrom(addr, alloc.Prefix().Bits())
		ipamIP = p.newHeldIPAMIP(regionOfZone(zone), project, prefix, pn, resourceTypeGatewayNetwork, id, "", macAddressOf(addr))
		p.env.Store.Put(ipamIP)
	}

	addr, _ := netip.ParsePrefix(textOf(ipamIP.Attrs["address"]))
	now := p.env.Now()
	res := resource.New(id, kindGatewayNetwork, resource.Tenant{Provider: Name, Project: project, Zone: zone}, "ready", now)
	res.Attrs = map[string]any{
		"gateway_id":         gw.ID,
		"private_network_id": pn.ID,
		"masquerade_enabled": req.EnableMasquerade,
		"push_default_route": req.PushDefaultRoute,
		"ipam_ip_id":         ipamIP.ID,
		"mac_address":        macAddressOf(addr.Addr()),
	}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusOK, p.gatewayNetworkView(res))
}

func (p *Pack) getGatewayNetwork(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindGatewayNetwork, "gatewayNetworkID", "gateway_network")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.gatewayNetworkView(res))
}

func (p *Pack) updateGatewayNetwork(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindGatewayNetwork, "gatewayNetworkID", "gateway_network")
	if !ok {
		return
	}
	var req updateGatewayNetworkRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.IpamIPID != nil && *req.IpamIPID != textOf(res.Attrs["ipam_ip_id"]) {
		// Swapping the connection's address in place is not something any
		// measured client does: the Terraform attribute forces a replace.
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "ipam_ip_id",
			Reason:       "constraint",
			HelpMessage:  "the connection's address cannot change in place; detach and reattach instead",
		})
		return
	}
	err := p.env.Store.Update(Name, kindGatewayNetwork, res.ID, func(stored *resource.Resource) error {
		if req.EnableMasquerade != nil {
			stored.Attrs["masquerade_enabled"] = *req.EnableMasquerade
		}
		if req.PushDefaultRoute != nil {
			stored.Attrs["push_default_route"] = *req.PushDefaultRoute
		}
		stored.Updated = p.env.Now()
		return nil
	})
	if err != nil {
		writeNotFound(w, "gateway_network", res.ID)
		return
	}
	current, _ := p.env.Store.Get(Name, kindGatewayNetwork, res.ID)
	emulator.WriteJSON(w, http.StatusOK, p.gatewayNetworkView(current))
}

func (p *Pack) deleteGatewayNetwork(w http.ResponseWriter, r *http.Request) {
	res, ok := p.zonalResourceOf(w, r, kindGatewayNetwork, "gatewayNetworkID", "gateway_network")
	if !ok {
		return
	}
	view := p.gatewayNetworkView(res)
	view["status"] = "detaching"
	p.env.Store.Delete(Name, kindGatewayNetwork, res.ID)
	// The connection's address goes back where it came from: released when
	// this pack created it, merely unheld when the client booked it first —
	// the same split a NIC delete applies.
	p.releaseHeldIPAMIPs(res.ID)
	// Like the gateway's own delete, v2 answers the deleted connection and the
	// provider polls GetGatewayNetwork until 404.
	emulator.WriteJSON(w, http.StatusOK, view)
}

func (p *Pack) gatewayNetworkView(res *resource.Resource) map[string]any {
	return map[string]any{
		"id":                 res.ID,
		"created_at":         res.Created.Format(time.RFC3339),
		"updated_at":         res.Updated.Format(time.RFC3339),
		"gateway_id":         res.Attrs["gateway_id"],
		"private_network_id": res.Attrs["private_network_id"],
		"mac_address":        res.Attrs["mac_address"],
		"masquerade_enabled": res.Attrs["masquerade_enabled"],
		"status":             res.State,
		"push_default_route": res.Attrs["push_default_route"],
		"ipam_ip_id":         res.Attrs["ipam_ip_id"],
		"zone":               res.Tenant.Zone,
	}
}
