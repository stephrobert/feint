package scaleway

import (
	"net/http"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/network"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The custom routes of a VPC: what `scaleway_vpc_route` manages, CRUD-complete
// through CreateRoute, GetRoute, UpdateRoute, DeleteRoute.
//
// They are records the client owns. No runtime mode programs a custom nexthop
// today, and docs/limits.md says so: a route to a NAT instance round-trips for
// Terraform and is not a path packets follow. That limit is stated rather than
// hidden because the alternative — declining the whole family — blocked
// `scaleway_vpc_route` outright, and lb and vpcgw sit behind it on the roadmap.
// What a routing-enabled VPC actually delivers between its own networks is the
// machine driver's peering, reconciled by EnableRouting (vpc.go), not by these
// records.
//
// Shapes come from the SDK (api/vpc/v2/vpc_sdk.go): Route and its requests.
// ListRoutesWithNexthop, the read of the full table, stays declined in pack.go:
// the portal's API document does not describe it yet.

const kindVPCRoute = "vpc/route"

type createRouteRequest struct {
	Description             string   `json:"description"`
	Tags                    []string `json:"tags"`
	VpcID                   string   `json:"vpc_id"`
	Destination             string   `json:"destination"`
	NexthopResourceID       *string  `json:"nexthop_resource_id"`
	NexthopPrivateNetworkID *string  `json:"nexthop_private_network_id"`
	NexthopVpcConnectorID   *string  `json:"nexthop_vpc_connector_id"`
}

type updateRouteRequest struct {
	Description             *string   `json:"description"`
	Tags                    *[]string `json:"tags"`
	Destination             *string   `json:"destination"`
	NexthopResourceID       *string   `json:"nexthop_resource_id"`
	NexthopPrivateNetworkID *string   `json:"nexthop_private_network_id"`
	NexthopVpcConnectorID   *string   `json:"nexthop_vpc_connector_id"`
}

func (p *Pack) createRoute(w http.ResponseWriter, r *http.Request) {
	region, ok := regionOf(w, r)
	if !ok {
		return
	}

	var req createRouteRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	vpc, found := p.env.Store.Get(Name, kindVPC, req.VpcID)
	if !found || vpc.Tenant.Zone != region {
		writeNotFound(w, "vpc", req.VpcID)
		return
	}
	destination, err := network.ParseCIDR(req.Destination)
	if err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "destination", Reason: "format", HelpMessage: err.Error()})
		return
	}
	// A connector nexthop names a product this pack declines (the VPC
	// connectors), so a route through one would reference a resource that
	// cannot exist here.
	if req.NexthopVpcConnectorID != nil && *req.NexthopVpcConnectorID != "" {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "nexthop_vpc_connector_id",
			Reason:       "constraint",
			HelpMessage:  "VPC connectors are not served by this emulator",
		})
		return
	}
	if req.NexthopPrivateNetworkID != nil && *req.NexthopPrivateNetworkID != "" {
		pn, found := p.env.Store.Get(Name, kindPrivateNetwork, *req.NexthopPrivateNetworkID)
		if !found || pn.Attrs["vpc_id"] != vpc.ID {
			writeNotFound(w, "private_network", *req.NexthopPrivateNetworkID)
			return
		}
	}

	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindVPCRoute, vpc.Tenant, "available", now)
	res.Attrs = map[string]any{
		"description": req.Description,
		"tags":        orEmpty(req.Tags),
		"vpc_id":      vpc.ID,
		"destination": destination.String(),
	}
	if req.NexthopResourceID != nil && *req.NexthopResourceID != "" {
		res.Attrs["nexthop_resource_id"] = *req.NexthopResourceID
	}
	if req.NexthopPrivateNetworkID != nil && *req.NexthopPrivateNetworkID != "" {
		res.Attrs["nexthop_private_network_id"] = *req.NexthopPrivateNetworkID
	}
	p.env.Store.Put(res)

	emulator.WriteJSON(w, vpcCreateStatus, routeView(res))
}

func (p *Pack) getRoute(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindVPCRoute, "routeID", "route")
	if !ok {
		return
	}
	emulator.WriteJSON(w, http.StatusOK, routeView(res))
}

func (p *Pack) updateRoute(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindVPCRoute, "routeID", "route")
	if !ok {
		return
	}

	var req updateRouteRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	destination := ""
	if req.Destination != nil {
		parsed, err := network.ParseCIDR(*req.Destination)
		if err != nil {
			writeInvalidArguments(w, ArgumentError{ArgumentName: "destination", Reason: "format", HelpMessage: err.Error()})
			return
		}
		destination = parsed.String()
	}
	if req.NexthopVpcConnectorID != nil && *req.NexthopVpcConnectorID != "" {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "nexthop_vpc_connector_id",
			Reason:       "constraint",
			HelpMessage:  "VPC connectors are not served by this emulator",
		})
		return
	}
	if req.NexthopPrivateNetworkID != nil && *req.NexthopPrivateNetworkID != "" {
		pn, found := p.env.Store.Get(Name, kindPrivateNetwork, *req.NexthopPrivateNetworkID)
		if !found || pn.Attrs["vpc_id"] != res.Attrs["vpc_id"] {
			writeNotFound(w, "private_network", *req.NexthopPrivateNetworkID)
			return
		}
	}

	// Inside Store.Update rather than mutate-then-Put: Put resurrects a route
	// deleted while this PATCH was decoding, and its whole-Attrs write-back
	// from a stale clone loses a concurrent writer's field (#289).
	var final *resource.Resource
	err := p.env.Store.Update(Name, kindVPCRoute, res.ID, func(stored *resource.Resource) error {
		if req.Destination != nil {
			stored.Attrs["destination"] = destination
		}
		if req.Description != nil {
			stored.Attrs["description"] = *req.Description
		}
		if req.Tags != nil {
			stored.Attrs["tags"] = orEmpty(*req.Tags)
		}
		if req.NexthopResourceID != nil {
			setOrDelete(stored.Attrs, "nexthop_resource_id", *req.NexthopResourceID)
		}
		if req.NexthopPrivateNetworkID != nil {
			setOrDelete(stored.Attrs, "nexthop_private_network_id", *req.NexthopPrivateNetworkID)
		}
		stored.Updated = p.env.Now()
		final = stored
		return nil
	})
	if err != nil {
		writeNotFound(w, "route", res.ID)
		return
	}

	emulator.WriteJSON(w, http.StatusOK, routeView(final))
}

func (p *Pack) deleteRoute(w http.ResponseWriter, r *http.Request) {
	res, ok := p.resourceOf(w, r, kindVPCRoute, "routeID", "route")
	if !ok {
		return
	}
	p.env.Store.Delete(Name, kindVPCRoute, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

// routesOfVPC lists the stored custom routes of a VPC, for the delete refusal.
func (p *Pack) routesOfVPC(vpcID string) []*resource.Resource {
	all := p.env.Store.List(kindVPCRoute, resource.Tenant{Provider: Name})
	out := make([]*resource.Resource, 0, len(all))
	for _, res := range all {
		if res.Attrs["vpc_id"] == vpcID {
			out = append(out, res)
		}
	}
	return out
}

func routeView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"id":                         res.ID,
		"description":                res.Attrs["description"],
		"tags":                       res.Attrs["tags"],
		"vpc_id":                     res.Attrs["vpc_id"],
		"destination":                res.Attrs["destination"],
		"nexthop_resource_id":        nil,
		"nexthop_private_network_id": nil,
		"nexthop_vpc_connector_id":   nil,
		"created_at":                 res.Created.Format(time.RFC3339),
		"updated_at":                 res.Updated.Format(time.RFC3339),
		"is_read_only":               false,
		"type":                       "custom",
		"region":                     res.Tenant.Zone,
	}
	if id, _ := res.Attrs["nexthop_resource_id"].(string); id != "" {
		out["nexthop_resource_id"] = id
	}
	if id, _ := res.Attrs["nexthop_private_network_id"].(string); id != "" {
		out["nexthop_private_network_id"] = id
	}
	return out
}

func setOrDelete(attrs map[string]any, key, value string) {
	if value == "" {
		delete(attrs, key)
		return
	}
	attrs[key] = value
}
