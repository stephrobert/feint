package outscale

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/network"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Route tables, read-only for now: the main table every Net is born with.
//
// Shape measured against a real account (X-2 sweep, 2026-08-08): the main table
// carries exactly one route — the local one, whose DestinationIpRange is the
// Net's own block, GatewayId the literal "local", CreationMethod
// "CreateRouteTable", State "active" — and one LinkRouteTables entry with
// Main:true and no SubnetId key. A route that is not a NAT route carries no
// NatServiceId key at all: conditional emission again, the same property the
// security group rules have.
//
// CreateRouteTable, CreateRoute and the link calls are backlog: implementable,
// wanted by Terraform, not yet here. What is served is what exists in every
// Net whether the client asked or not.

const kindRouteTable = "routetable"

// newRouteTable builds a table the way the recorded creation shows one being
// born: with the local route over the Net's block already in it — an explicit
// CreateRouteTable came back carrying it, CreationMethod "CreateRouteTable",
// GatewayId "local". Only the main table carries a link at birth. Stored, not
// derived: identifiers must be stable across reads or Terraform sees a diff.
func (p *Pack) newRouteTable(netID, ipRange string, main bool) *resource.Resource {
	now := p.env.Now()
	rtbID := newID("rtb", p.env.NewID())
	links := []any{}
	if main {
		links = append(links, map[string]any{
			"LinkRouteTableId": newID("rtbassoc", p.env.NewID()),
			"Main":             true,
			"NetId":            netID,
			"RouteTableId":     rtbID,
		})
	}
	return &resource.Resource{
		ID:      rtbID,
		Kind:    kindRouteTable,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"NetId": netID,
			"Routes": []any{map[string]any{
				"CreationMethod":     "CreateRouteTable",
				"DestinationIpRange": ipRange,
				"GatewayId":          "local",
				"State":              "active",
			}},
			"LinkRouteTables":                 links,
			"RoutePropagatingVirtualGateways": []any{},
			"Tags":                            []any{},
		},
	}
}

// mainRouteTable is the table Outscale creates with every Net.
func (p *Pack) mainRouteTable(netID, ipRange string) *resource.Resource {
	return p.newRouteTable(netID, ipRange, true)
}

type readRouteTablesRequest struct {
	Filters        filterSet `json:"Filters"`
	ResultsPerPage int       `json:"ResultsPerPage"`
	DryRun         *bool     `json:"DryRun"`
}

// routeTableFilters are what a stored table can answer. The nested ones matter
// as much as the top-level: the Terraform provider reads a route back by
// filtering on its destination, and a table by the subnet its link names.
var routeTableFilters = []string{
	"RouteTableIds", "NetIds",
	"LinkRouteTableIds", "LinkSubnetIds", "LinkRouteTableMain",
	"RouteDestinationIpRanges", "RouteGatewayIds", "RouteNatServiceIds",
	"RouteCreationMethods", "RouteStates",
}

func (p *Pack) readRouteTables(w http.ResponseWriter, r *http.Request) {
	var req readRouteTablesRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, routeTableFilters...) {
		return
	}

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindRouteTable, resource.Tenant{Provider: Name}) {
		if !p.routeTableMatches(res, req.Filters) {
			continue
		}
		out = append(out, routeTableView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"RouteTables":     page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

// routeTableMatches applies every declared filter, the nested ones against the
// whole of what the table holds: a filter matches when ANY route or link
// carries the value asked for, which is the semantics the API describes and the
// provider relies on.
func (p *Pack) routeTableMatches(res *resource.Resource, f filterSet) bool {
	if !matchesStrings(f, "RouteTableIds", res.ID) ||
		!matchesStrings(f, "NetIds", stringOf(res.Attrs["NetId"])) {
		return false
	}

	var linkIDs, linkSubnets []string
	for _, raw := range listOf(res.Attrs["LinkRouteTables"]) {
		link, _ := raw.(map[string]any)
		linkIDs = append(linkIDs, stringOf(link["LinkRouteTableId"]))
		if subnet := stringOf(link["SubnetId"]); subnet != "" {
			linkSubnets = append(linkSubnets, subnet)
		}
	}
	if !matchesAny(f, "LinkRouteTableIds", linkIDs...) ||
		!matchesAny(f, "LinkSubnetIds", linkSubnets...) ||
		!matchesBool(f, "LinkRouteTableMain", isMainTable(res)) {
		return false
	}

	var destinations, gateways, nats, methods, states []string
	for _, raw := range listOf(res.Attrs["Routes"]) {
		route, _ := raw.(map[string]any)
		destinations = append(destinations, stringOf(route["DestinationIpRange"]))
		if gw := stringOf(route["GatewayId"]); gw != "" {
			gateways = append(gateways, gw)
		}
		if nat := stringOf(route["NatServiceId"]); nat != "" {
			nats = append(nats, nat)
		}
		methods = append(methods, stringOf(route["CreationMethod"]))
		states = append(states, stringOf(route["State"]))
	}
	return matchesAny(f, "RouteDestinationIpRanges", destinations...) &&
		matchesAny(f, "RouteGatewayIds", gateways...) &&
		matchesAny(f, "RouteNatServiceIds", nats...) &&
		matchesAny(f, "RouteCreationMethods", methods...) &&
		matchesAny(f, "RouteStates", states...)
}

// routeTableView leaves State off the wire on purpose: the RouteTable schema
// has no State property — the resource's own State exists for the store, not
// for the client.
func routeTableView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+1)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["RouteTableId"] = res.ID
	return out
}

// routeTablesOf lists the tables belonging to one Net.
func (p *Pack) routeTablesOf(netID string) []*resource.Resource {
	out := make([]*resource.Resource, 0, 1)
	for _, res := range p.env.Store.List(kindRouteTable, resource.Tenant{Provider: Name}) {
		if stringOf(res.Attrs["NetId"]) == netID {
			out = append(out, res)
		}
	}
	return out
}

// ---- Lifecycle --------------------------------------------------------------

// isMainTable reports whether a table carries the Main link — the one that
// belongs to the Net rather than to the client, and that no client call may
// unlink or delete.
func isMainTable(res *resource.Resource) bool {
	for _, raw := range listOf(res.Attrs["LinkRouteTables"]) {
		link, _ := raw.(map[string]any)
		if main, _ := link["Main"].(bool); main {
			return true
		}
	}
	return false
}

// listOf reads a stored list whatever it crossed; a snapshot restores []any and
// the handlers store []any already, so this is mostly a nil guard.
func listOf(v any) []any {
	out, _ := v.([]any)
	return out
}

func (p *Pack) createRouteTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NetID  string `json:"NetId"`
		DryRun *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	net, found := p.env.Store.Get(Name, kindNet, req.NetID)
	if !found {
		p.notFound(w, "Net", req.NetID)
		return
	}
	res := p.newRouteTable(req.NetID, stringOf(net.Attrs["IpRange"]), false)
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"RouteTable":      routeTableView(res),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteRouteTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RouteTableID string `json:"RouteTableId"`
		DryRun       *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindRouteTable, req.RouteTableID)
	if !found {
		p.notFound(w, "route table", req.RouteTableID)
		return
	}
	// The main table is the Net's: it goes with the Net and only with it.
	if isMainTable(res) {
		p.conflict(w, "the main route table of "+stringOf(res.Attrs["NetId"])+" cannot be deleted")
		return
	}
	// A table still linked to a subnet does not go; the unlink comes first,
	// which is the order Terraform's destroy already walks.
	if len(listOf(res.Attrs["LinkRouteTables"])) > 0 {
		p.conflict(w, "the route table "+res.ID+" is still linked to a subnet")
		return
	}
	p.env.Store.Delete(Name, kindRouteTable, req.RouteTableID)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func (p *Pack) linkRouteTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		RouteTableID string `json:"RouteTableId"`
		SubnetID     string `json:"SubnetId"`
		DryRun       *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindRouteTable, req.RouteTableID)
	if !found {
		p.notFound(w, "route table", req.RouteTableID)
		return
	}
	subnet, found := p.env.Store.Get(Name, kindSubnet, req.SubnetID)
	if !found {
		p.notFound(w, "Subnet", req.SubnetID)
		return
	}
	if stringOf(subnet.Attrs["NetId"]) != stringOf(res.Attrs["NetId"]) {
		p.badRequest(w, "the Subnet "+req.SubnetID+" is not in the Net of "+res.ID)
		return
	}
	// One explicit table per subnet, which the real API enforces: a second
	// link is a conflict, not a silent replacement.
	for _, other := range p.env.Store.List(kindRouteTable, resource.Tenant{Provider: Name}) {
		for _, raw := range listOf(other.Attrs["LinkRouteTables"]) {
			link, _ := raw.(map[string]any)
			if stringOf(link["SubnetId"]) == req.SubnetID {
				p.conflict(w, "the Subnet "+req.SubnetID+" is already linked to "+other.ID)
				return
			}
		}
	}
	// The recorded response is {LinkRouteTableId, ResponseContext} alone; the
	// link itself appears in the table on the next read.
	linkID := newID("rtbassoc", p.env.NewID())
	res.Attrs["LinkRouteTables"] = append(listOf(res.Attrs["LinkRouteTables"]), map[string]any{
		"LinkRouteTableId": linkID,
		"Main":             false,
		"NetId":            stringOf(res.Attrs["NetId"]),
		"RouteTableId":     res.ID,
		"SubnetId":         req.SubnetID,
	})
	if !p.env.Store.Commit(res, p.env.Now()) {
		p.notFound(w, "route table", req.RouteTableID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"LinkRouteTableId": linkID,
		"ResponseContext":  p.context(),
	})
}

func (p *Pack) unlinkRouteTable(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LinkRouteTableID string `json:"LinkRouteTableId"`
		DryRun           *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	for _, res := range p.env.Store.List(kindRouteTable, resource.Tenant{Provider: Name}) {
		links := listOf(res.Attrs["LinkRouteTables"])
		for i, raw := range links {
			link, _ := raw.(map[string]any)
			if stringOf(link["LinkRouteTableId"]) != req.LinkRouteTableID {
				continue
			}
			if main, _ := link["Main"].(bool); main {
				p.conflict(w, "the main link of "+res.ID+" cannot be unlinked")
				return
			}
			res.Attrs["LinkRouteTables"] = append(links[:i:i], links[i+1:]...)
			if !p.env.Store.Commit(res, p.env.Now()) {
				p.notFound(w, "route table", res.ID)
				return
			}
			emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
			return
		}
	}
	p.notFound(w, "route table link", req.LinkRouteTableID)
}

// routeRequest is CreateRoute, DeleteRoute and UpdateRoute: one shape, three
// verbs, keyed by the destination inside one table.
type routeRequest struct {
	RouteTableID       string `json:"RouteTableId"`
	DestinationIPRange string `json:"DestinationIpRange"`
	GatewayID          string `json:"GatewayId"`
	NatServiceID       string `json:"NatServiceId"`
	NicID              string `json:"NicId"`
	VMID               string `json:"VmId"`
	DryRun             *bool  `json:"DryRun"`
}

// routeTarget validates whichever target the request names and returns the key
// it lands under in the stored route. The gateway must be linked to the table's
// Net and the NAT service placed in it: a route through a gateway attached
// elsewhere is what the real API refuses.
func (p *Pack) routeTarget(w http.ResponseWriter, req *routeRequest, netID string) (string, string, bool) {
	switch {
	case req.GatewayID != "":
		gw, found := p.env.Store.Get(Name, kindInternetService, req.GatewayID)
		if !found {
			p.notFound(w, "internet service", req.GatewayID)
			return "", "", false
		}
		if stringOf(gw.Attrs["NetId"]) != netID {
			p.badRequest(w, "the internet service "+req.GatewayID+" is not linked to "+netID)
			return "", "", false
		}
		return "GatewayId", req.GatewayID, true
	case req.NatServiceID != "":
		nat, found := p.env.Store.Get(Name, kindNatService, req.NatServiceID)
		if !found {
			p.notFound(w, "NAT service", req.NatServiceID)
			return "", "", false
		}
		if stringOf(nat.Attrs["NetId"]) != netID {
			p.badRequest(w, "the NAT service "+req.NatServiceID+" is not in "+netID)
			return "", "", false
		}
		return "NatServiceId", req.NatServiceID, true
	case req.NicID != "":
		return "NicId", req.NicID, true
	case req.VMID != "":
		if _, found := p.env.Store.Get(Name, kindVM, req.VMID); !found {
			p.notFound(w, "Vm", req.VMID)
			return "", "", false
		}
		return "VmId", req.VMID, true
	default:
		p.badRequest(w, "a route needs a target: GatewayId, NatServiceId, NicId or VmId")
		return "", "", false
	}
}

func (p *Pack) createRoute(w http.ResponseWriter, r *http.Request) {
	var req routeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindRouteTable, req.RouteTableID)
	if !found {
		p.notFound(w, "route table", req.RouteTableID)
		return
	}
	if _, err := network.ParseCIDR(req.DestinationIPRange); err != nil {
		p.badRequest(w, "DestinationIpRange: "+err.Error())
		return
	}
	for _, raw := range listOf(res.Attrs["Routes"]) {
		route, _ := raw.(map[string]any)
		if stringOf(route["DestinationIpRange"]) == req.DestinationIPRange {
			p.conflict(w, "the destination "+req.DestinationIPRange+" already has a route in "+res.ID)
			return
		}
	}
	key, value, ok := p.routeTarget(w, &req, stringOf(res.Attrs["NetId"]))
	if !ok {
		return
	}
	// The recorded created route: CreationMethod "CreateRoute", State
	// "active", the target under its own key, nothing else.
	res.Attrs["Routes"] = append(listOf(res.Attrs["Routes"]), map[string]any{
		"CreationMethod":     "CreateRoute",
		"DestinationIpRange": req.DestinationIPRange,
		key:                  value,
		"State":              "active",
	})
	if !p.env.Store.Commit(res, p.env.Now()) {
		p.notFound(w, "route table", req.RouteTableID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"RouteTable":      routeTableView(res),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteRoute(w http.ResponseWriter, r *http.Request) {
	var req routeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindRouteTable, req.RouteTableID)
	if !found {
		p.notFound(w, "route table", req.RouteTableID)
		return
	}
	routes := listOf(res.Attrs["Routes"])
	for i, raw := range routes {
		route, _ := raw.(map[string]any)
		if stringOf(route["DestinationIpRange"]) != req.DestinationIPRange {
			continue
		}
		// The local route is the Net's own and never goes; the real API
		// refuses, whatever the table.
		if stringOf(route["GatewayId"]) == "local" {
			p.conflict(w, "the local route of "+res.ID+" cannot be deleted")
			return
		}
		res.Attrs["Routes"] = append(routes[:i:i], routes[i+1:]...)
		if !p.env.Store.Commit(res, p.env.Now()) {
			p.notFound(w, "route table", req.RouteTableID)
			return
		}
		emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
		return
	}
	p.notFound(w, "route to", req.DestinationIPRange)
}

func (p *Pack) updateRoute(w http.ResponseWriter, r *http.Request) {
	var req routeRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindRouteTable, req.RouteTableID)
	if !found {
		p.notFound(w, "route table", req.RouteTableID)
		return
	}
	key, value, ok := p.routeTarget(w, &req, stringOf(res.Attrs["NetId"]))
	if !ok {
		return
	}
	for i, raw := range listOf(res.Attrs["Routes"]) {
		route, _ := raw.(map[string]any)
		if stringOf(route["DestinationIpRange"]) != req.DestinationIPRange {
			continue
		}
		if stringOf(route["GatewayId"]) == "local" {
			p.conflict(w, "the local route of "+res.ID+" cannot be redirected")
			return
		}
		// Replaced whole rather than patched: a route is its destination and
		// its target, and leaving the old target key beside the new one would
		// answer a route pointing two ways at once.
		replaced := map[string]any{
			"CreationMethod":     stringOf(route["CreationMethod"]),
			"DestinationIpRange": req.DestinationIPRange,
			key:                  value,
			"State":              "active",
		}
		routes := listOf(res.Attrs["Routes"])
		routes[i] = replaced
		res.Attrs["Routes"] = routes
		if !p.env.Store.Commit(res, p.env.Now()) {
			p.notFound(w, "route table", req.RouteTableID)
			return
		}
		emulator.WriteJSON(w, http.StatusOK, map[string]any{
			"RouteTable":      routeTableView(res),
			"ResponseContext": p.context(),
		})
		return
	}
	p.notFound(w, "route to", req.DestinationIPRange)
}
