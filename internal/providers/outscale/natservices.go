package outscale

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// NAT services: the egress a private subnet buys with a public IP.
//
// Control plane only, same stated limit as the internet services: creating one
// does not make packets flow, and docs/limits.md says so. What it does model is
// the resource algebra the real API enforces and Terraform depends on — a NAT
// service consumes an allocated public IP (the address answers with
// NatServiceId from then on, measured), refuses an address already consumed,
// and releases it on delete.
//
// Shape measured (X-2 sweep, 2026-08-08): {NatServiceId, NetId,
// PublicIps:[{PublicIp, PublicIpId}], State:"available", SubnetId, Tags}.

const kindNatService = "natservice"

type createNatServiceRequest struct {
	SubnetID    string `json:"SubnetId"`
	PublicIPID  string `json:"PublicIpId"`
	ClientToken string `json:"ClientToken"`
	DryRun      *bool  `json:"DryRun"`
}

func (p *Pack) createNatService(w http.ResponseWriter, r *http.Request) {
	var req createNatServiceRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.SubnetID == "" || req.PublicIPID == "" {
		p.badRequest(w, "SubnetId and PublicIpId are required")
		return
	}
	subnet, found := p.env.Store.Get(Name, kindSubnet, req.SubnetID)
	if !found {
		p.notFound(w, "Subnet", req.SubnetID)
		return
	}
	address, found := p.env.Store.Get(Name, kindPublicIP, req.PublicIPID)
	if !found {
		p.notFound(w, "public IP", req.PublicIPID)
		return
	}
	if holder := stringOf(address.Attrs["NatServiceId"]); holder != "" {
		p.conflict(w, "the public IP "+address.ID+" is already held by "+holder)
		return
	}

	now := p.env.Now()
	res := resource.New(newID("nat", p.env.NewID()), kindNatService, resource.Tenant{Provider: Name}, "available", now)
	res.Attrs = map[string]any{
		"NetId":    stringOf(subnet.Attrs["NetId"]),
		"SubnetId": req.SubnetID,
		"PublicIps": []any{map[string]any{
			"PublicIp":   stringOf(address.Attrs["PublicIp"]),
			"PublicIpId": address.ID,
		}},
		"Tags": []any{},
	}
	p.env.Store.Put(res)

	// The address is marked held. Commit rather than Put: a concurrent delete
	// of the address must not be resurrected by this write.
	address.Attrs["NatServiceId"] = res.ID
	address.Attrs["LinkPublicIpId"] = newID("eipassoc", p.env.NewID())
	if !p.env.Store.Commit(address, now) {
		// The address vanished between the check and the write; the NAT service
		// cannot hold what no longer exists.
		p.env.Store.Delete(Name, kindNatService, res.ID)
		p.notFound(w, "public IP", req.PublicIPID)
		return
	}

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"NatService":      natServiceView(res),
		"ResponseContext": p.context(),
	})
}

var natServiceFilters = []string{"NatServiceIds", "NetIds", "SubnetIds", "States"}

func (p *Pack) readNatServices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		ResultsPerPage int       `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, natServiceFilters...) {
		return
	}

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindNatService, resource.Tenant{Provider: Name}) {
		if !matchesStrings(req.Filters, "NatServiceIds", res.ID) ||
			!matchesStrings(req.Filters, "NetIds", stringOf(res.Attrs["NetId"])) ||
			!matchesStrings(req.Filters, "SubnetIds", stringOf(res.Attrs["SubnetId"])) ||
			!matchesStrings(req.Filters, "States", res.State) {
			continue
		}
		out = append(out, natServiceView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"NatServices":     page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deleteNatService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NatServiceID string `json:"NatServiceId"`
		DryRun       *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindNatService, req.NatServiceID)
	if !found {
		p.notFound(w, "NAT service", req.NatServiceID)
		return
	}
	p.env.Store.Delete(Name, kindNatService, req.NatServiceID)

	// The address is released, exactly as the real teardown releases it. Best
	// effort by construction: if the address is gone too, there is nothing to
	// release.
	for _, address := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		if stringOf(address.Attrs["NatServiceId"]) == res.ID {
			delete(address.Attrs, "NatServiceId")
			delete(address.Attrs, "LinkPublicIpId")
			_ = p.env.Store.Commit(address, p.env.Now())
		}
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func natServiceView(res *resource.Resource) map[string]any {
	out := make(map[string]any, len(res.Attrs)+2)
	for k, v := range res.Attrs {
		out[k] = v
	}
	out["NatServiceId"] = res.ID
	out["State"] = res.State
	return out
}

// natServicesOf lists the NAT services placed in one subnet.
func (p *Pack) natServicesOf(subnetID string) []*resource.Resource {
	out := make([]*resource.Resource, 0, 1)
	for _, res := range p.env.Store.List(kindNatService, resource.Tenant{Provider: Name}) {
		if stringOf(res.Attrs["SubnetId"]) == subnetID {
			out = append(out, res)
		}
	}
	return out
}
