package outscale

import (
	"errors"
	"net/http"
	"sort"

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
	ResultsPerPage *int      `json:"ResultsPerPage"`
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
	if p.refusePageSize(w, req.ResultsPerPage) {
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
		out = append(out, routeTableReadView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"RouteTables":     page(out, pageSize(req.ResultsPerPage)),
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

// routeTableReadView is routeTableView with the routes ordered the way a *read*
// answers them: by destination, ascending (#379).
//
// **The create and the read do not agree, and that is measured rather than
// assumed.** On 2026-08-21, against a real account, one table carrying the Net's
// own local route and one route to an internet service answered:
//
//	CreateRoute       Routes = [ <the Net's range>, 0.0.0.0/0 ]   append order
//	ReadRouteTables   Routes = [ 0.0.0.0/0, <the Net's range> ]   destination order
//
// So the create echoes the table as it stood plus what was just added, and the
// read sorts. This pack sorted in neither place, which made the read diverge;
// sorting in both would have made the create diverge instead. A client that
// stores the create's answer and then reads sees the two swap, and that is the
// cloud's own behaviour rather than something to improve on here: an emulator
// that tidied it up would be hiding the plan diff a real stack meets.
//
// Sorted on a copy, because the stored slice is the record of what was added
// and a view has no business reordering it in the store. Ties keep their
// relative order, which cannot arise today — a table holds one route per
// destination, enforced by createRoute — and would be the least surprising
// answer if it did.
//
// TestARouteTableAnswersItsRoutesInDestinationOrder fails without this, and
// ReplayInvariants declares both orders so a recording of the real cloud holds
// them too.
func routeTableReadView(res *resource.Resource) map[string]any {
	out := routeTableView(res)
	routes := listOf(res.Attrs["Routes"])
	if len(routes) < 2 {
		return out
	}
	sorted := append([]any(nil), routes...)
	sort.SliceStable(sorted, func(i, j int) bool {
		a, _ := sorted[i].(map[string]any)
		b, _ := sorted[j].(map[string]any)
		return stringOf(a["DestinationIpRange"]) < stringOf(b["DestinationIpRange"])
	})
	out["Routes"] = sorted
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
	//
	// Appended inside Store.Update, under the lock: Terraform links one table
	// to its subnets ten at a time, and the clone-append-Commit shape this had
	// loses an acknowledged link exactly as it lost a security group rule
	// (#289). The one-link-per-subnet scan re-runs on this table's own links,
	// because two identical concurrent links both pass the scan above.
	linkID := newID("rtbassoc", p.env.NewID())
	err := p.env.Store.Update(Name, kindRouteTable, res.ID, func(stored *resource.Resource) error {
		links := listOf(stored.Attrs["LinkRouteTables"])
		for _, raw := range links {
			link, _ := raw.(map[string]any)
			if stringOf(link["SubnetId"]) == req.SubnetID {
				return errAlreadyLinked
			}
		}
		merged := make([]any, len(links), len(links)+1)
		copy(merged, links)
		stored.Attrs["LinkRouteTables"] = append(merged, map[string]any{
			"LinkRouteTableId": linkID,
			"Main":             false,
			"NetId":            stringOf(stored.Attrs["NetId"]),
			"RouteTableId":     stored.ID,
			"SubnetId":         req.SubnetID,
		})
		stored.Updated = p.env.Now()
		return nil
	})
	switch {
	case errors.Is(err, errAlreadyLinked):
		p.conflict(w, "the Subnet "+req.SubnetID+" is already linked to "+res.ID)
		return
	case err != nil:
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
		holds := false
		for _, raw := range listOf(res.Attrs["LinkRouteTables"]) {
			link, _ := raw.(map[string]any)
			if stringOf(link["LinkRouteTableId"]) == req.LinkRouteTableID {
				holds = true
				break
			}
		}
		if !holds {
			continue
		}
		// The link is re-found and removed under the lock: the List clone
		// above is stale by construction, and committing it wholesale is the
		// lost-update shape #289 measured on rules.
		err := p.env.Store.Update(Name, kindRouteTable, res.ID, func(stored *resource.Resource) error {
			links := listOf(stored.Attrs["LinkRouteTables"])
			kept := make([]any, 0, len(links))
			removed := false
			for _, raw := range links {
				link, _ := raw.(map[string]any)
				if stringOf(link["LinkRouteTableId"]) == req.LinkRouteTableID {
					if main, _ := link["Main"].(bool); main {
						return errMainLink
					}
					removed = true
					continue
				}
				kept = append(kept, raw)
			}
			if !removed {
				return errLinkMissing
			}
			stored.Attrs["LinkRouteTables"] = kept
			stored.Updated = p.env.Now()
			return nil
		})
		switch {
		case errors.Is(err, errMainLink):
			p.conflict(w, "the main link of "+res.ID+" cannot be unlinked")
			return
		case err != nil:
			// Gone between the scan and the lock — deleted table or already
			// unlinked; either way the link no longer exists.
			p.notFound(w, "route table link", req.LinkRouteTableID)
			return
		}
		emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
		return
	}
	p.notFound(w, "route table link", req.LinkRouteTableID)
}

// The sentinels that carry a check out of a Store.Update change function, so
// the handler can answer the HTTP shape the check calls for.
var (
	errAlreadyLinked = errors.New("the subnet is already linked")
	errMainLink      = errors.New("the main link cannot go")
	errLinkMissing   = errors.New("the link does not exist")
	errRouteExists   = errors.New("the destination already has a route")
	errRouteMissing  = errors.New("the destination has no route")
	errLocalRoute    = errors.New("the local route cannot go")
)

// routeRequest is CreateRoute, DeleteRoute and UpdateRoute: one shape, three
// verbs, keyed by the destination inside one table.
type routeRequest struct {
	RouteTableID       string `json:"RouteTableId"`
	DestinationIPRange string `json:"DestinationIpRange"`
	GatewayID          string `json:"GatewayId"`
	NatServiceID       string `json:"NatServiceId"`
	// A route whose next hop is the other side of a peering. Declared by their
	// SDK's CreateRouteRequest and refused here until a realistic configuration
	// asked for it: two peered Nets are useless without the route that points at
	// the peering, and the Terraform provider sends exactly this.
	NetPeeringID string `json:"NetPeeringId"`
	NicID        string `json:"NicId"`
	VMID         string `json:"VmId"`
	DryRun       *bool  `json:"DryRun"`
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
	case req.NetPeeringID != "":
		peering, found := p.env.Store.Get(Name, kindNetPeering, req.NetPeeringID)
		if !found {
			p.notFound(w, "Net peering", req.NetPeeringID)
			return "", "", false
		}
		// The table's Net has to be one of the peering's two ends. A route
		// through somebody else's peering is the mistake this checks for, and
		// it is the same shape as the gateway check above.
		source := stringOf(peering.Attrs["SourceNetId"])
		accepter := stringOf(peering.Attrs["AccepterNetId"])
		if netID != source && netID != accepter {
			p.badRequest(w, "the Net peering "+req.NetPeeringID+" does not have "+netID+" at either end")
			return "", "", false
		}
		return "NetPeeringId", req.NetPeeringID, true
	case req.NicID != "":
		return "NicId", req.NicID, true
	case req.VMID != "":
		if _, found := p.env.Store.Get(Name, kindVM, req.VMID); !found {
			p.notFound(w, "Vm", req.VMID)
			return "", "", false
		}
		return "VmId", req.VMID, true
	default:
		p.badRequest(w, "a route needs a target: GatewayId, NatServiceId, NetPeeringId, NicId or VmId")
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
	key, value, ok := p.routeTarget(w, &req, stringOf(res.Attrs["NetId"]))
	if !ok {
		return
	}
	// The duplicate check and the append run under the store lock: ten
	// concurrent CreateRoute on one table is Terraform's default parallelism,
	// and the clone-append-Commit shape this had loses acknowledged routes the
	// way it lost security group rules (#289).
	// TestConcurrentRouteCreatesKeepEveryAcknowledgedRoute fails without this.
	var final *resource.Resource
	err := p.env.Store.Update(Name, kindRouteTable, res.ID, func(stored *resource.Resource) error {
		routes := listOf(stored.Attrs["Routes"])
		for _, raw := range routes {
			route, _ := raw.(map[string]any)
			if stringOf(route["DestinationIpRange"]) == req.DestinationIPRange {
				return errRouteExists
			}
		}
		merged := make([]any, len(routes), len(routes)+1)
		copy(merged, routes)
		// The recorded created route: CreationMethod "CreateRoute", State
		// "active", the target under its own key, nothing else.
		stored.Attrs["Routes"] = append(merged, map[string]any{
			"CreationMethod":     "CreateRoute",
			"DestinationIpRange": req.DestinationIPRange,
			key:                  value,
			"State":              "active",
		})
		stored.Updated = p.env.Now()
		final = stored
		return nil
	})
	switch {
	case errors.Is(err, errRouteExists):
		p.conflict(w, "the destination "+req.DestinationIPRange+" already has a route in "+res.ID)
		return
	case err != nil:
		p.notFound(w, "route table", req.RouteTableID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"RouteTable":      routeTableView(final),
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
	// Found and removed under the lock, same reasoning as createRoute (#289).
	var final *resource.Resource
	err := p.env.Store.Update(Name, kindRouteTable, res.ID, func(stored *resource.Resource) error {
		routes := listOf(stored.Attrs["Routes"])
		kept := make([]any, 0, len(routes))
		removed := false
		for _, raw := range routes {
			route, _ := raw.(map[string]any)
			if stringOf(route["DestinationIpRange"]) == req.DestinationIPRange {
				// The local route is the Net's own and never goes; the real
				// API refuses, whatever the table.
				if stringOf(route["GatewayId"]) == "local" {
					return errLocalRoute
				}
				removed = true
				continue
			}
			kept = append(kept, raw)
		}
		if !removed {
			return errRouteMissing
		}
		stored.Attrs["Routes"] = kept
		stored.Updated = p.env.Now()
		final = stored
		return nil
	})
	switch {
	case errors.Is(err, errLocalRoute):
		p.conflict(w, "the local route of "+res.ID+" cannot be deleted")
		return
	case errors.Is(err, errRouteMissing):
		p.notFound(w, "route to", req.DestinationIPRange)
		return
	case err != nil:
		p.notFound(w, "route table", req.RouteTableID)
		return
	}
	// The table as it now stands, without the route (#381). The real API
	// answers it — DeleteRouteResponse declares RouteTable, osc-sdk-go
	// client.gen.go — and it is how a client refreshes its state in one call
	// instead of two. This pack answered the envelope alone, so a client
	// reading the answer got nil here and a table there. Measured on a real
	// account on 2026-08-21.
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"RouteTable":      routeTableView(final),
		"ResponseContext": p.context(),
	})
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
	// Found and replaced under the lock, and into a fresh slice: writing
	// routes[i] on the clone mutates the backing array every other reader's
	// clone shares, which is a torn write even before the Commit lands (#289).
	var final *resource.Resource
	err := p.env.Store.Update(Name, kindRouteTable, res.ID, func(stored *resource.Resource) error {
		routes := listOf(stored.Attrs["Routes"])
		replaced := false
		merged := make([]any, 0, len(routes))
		for _, raw := range routes {
			route, _ := raw.(map[string]any)
			if stringOf(route["DestinationIpRange"]) != req.DestinationIPRange {
				merged = append(merged, raw)
				continue
			}
			if stringOf(route["GatewayId"]) == "local" {
				return errLocalRoute
			}
			// Replaced whole rather than patched: a route is its destination
			// and its target, and leaving the old target key beside the new
			// one would answer a route pointing two ways at once.
			merged = append(merged, map[string]any{
				"CreationMethod":     stringOf(route["CreationMethod"]),
				"DestinationIpRange": req.DestinationIPRange,
				key:                  value,
				"State":              "active",
			})
			replaced = true
		}
		if !replaced {
			return errRouteMissing
		}
		stored.Attrs["Routes"] = merged
		stored.Updated = p.env.Now()
		final = stored
		return nil
	})
	switch {
	case errors.Is(err, errLocalRoute):
		p.conflict(w, "the local route of "+res.ID+" cannot be redirected")
		return
	case errors.Is(err, errRouteMissing):
		p.notFound(w, "route to", req.DestinationIPRange)
		return
	case err != nil:
		p.notFound(w, "route table", req.RouteTableID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"RouteTable":      routeTableView(final),
		"ResponseContext": p.context(),
	})
}
