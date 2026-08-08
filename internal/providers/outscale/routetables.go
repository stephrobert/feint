package outscale

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
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

// mainRouteTable is the table Outscale creates with every Net. Stored, not
// derived: its ids must be stable across reads or Terraform sees a diff.
func (p *Pack) mainRouteTable(netID, ipRange string) *resource.Resource {
	now := p.env.Now()
	rtbID := newID("rtb", p.env.NewID())
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
			"LinkRouteTables": []any{map[string]any{
				"LinkRouteTableId": newID("rtbassoc", p.env.NewID()),
				"Main":             true,
				"NetId":            netID,
				"RouteTableId":     rtbID,
			}},
			"RoutePropagatingVirtualGateways": []any{},
			"Tags":                            []any{},
		},
	}
}

type readRouteTablesRequest struct {
	Filters        filterSet `json:"Filters"`
	ResultsPerPage int       `json:"ResultsPerPage"`
	DryRun         *bool     `json:"DryRun"`
}

var routeTableFilters = []string{"RouteTableIds", "NetIds"}

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
		netID := stringOf(res.Attrs["NetId"])
		if !matchesStrings(req.Filters, "RouteTableIds", res.ID) ||
			!matchesStrings(req.Filters, "NetIds", netID) {
			continue
		}
		out = append(out, routeTableView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"RouteTables":     page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
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
