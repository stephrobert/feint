package outscale

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Internet services: the gateway a Net attaches to reach outside.
//
// Control plane only, and the limit is stated rather than papered over: linking
// one does NOT switch NAT on for the Net's subnets. nets.go leaves NAT off the
// backing networks on purpose — an emulated Subnet must not be more permissive
// than a real one — and turning it on here would need the runtime call that
// work has not been done for. So a linked internet service is a record a
// Terraform plan can create, read and destroy in the right order, and
// docs/limits.md says the packets still do not flow. The alternative — refusing
// the whole family — failed every VPC plan at its second resource.
//
// Shape measured (X-2 sweep, 2026-08-08): a linked service is
// {InternetServiceId, NetId, State:"available", Tags}; an unlinked one carries
// no NetId and no State key at all — the state belongs to the link, not to the
// gateway.

const kindInternetService = "internetservice"

func (p *Pack) createInternetService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DryRun *bool `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	now := p.env.Now()
	res := resource.New(newID("igw", p.env.NewID()), kindInternetService, resource.Tenant{Provider: Name}, "available", now)
	res.Attrs = map[string]any{
		"Tags": []any{},
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"InternetService": internetServiceView(res),
		"ResponseContext": p.context(),
	})
}

var internetServiceFilters = []string{"InternetServiceIds", "LinkNetIds"}

func (p *Pack) readInternetServices(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		ResultsPerPage int       `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, internetServiceFilters...) {
		return
	}

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindInternetService, resource.Tenant{Provider: Name}) {
		if !matchesStrings(req.Filters, "InternetServiceIds", res.ID) ||
			!matchesStrings(req.Filters, "LinkNetIds", stringOf(res.Attrs["NetId"])) {
			continue
		}
		out = append(out, internetServiceView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"InternetServices": page(out, req.ResultsPerPage),
		"ResponseContext":  p.context(),
	})
}

func (p *Pack) linkInternetService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InternetServiceID string `json:"InternetServiceId"`
		NetID             string `json:"NetId"`
		DryRun            *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindInternetService, req.InternetServiceID)
	if !found {
		p.notFound(w, "internet service", req.InternetServiceID)
		return
	}
	if _, found := p.env.Store.Get(Name, kindNet, req.NetID); !found {
		p.notFound(w, "Net", req.NetID)
		return
	}
	if held := stringOf(res.Attrs["NetId"]); held != "" && held != req.NetID {
		p.conflict(w, "the internet service "+res.ID+" is already linked to "+held)
		return
	}
	// One gateway per Net, which the real API enforces: a second link answers a
	// resource conflict, and Terraform's replace flow depends on the refusal.
	for _, other := range p.env.Store.List(kindInternetService, resource.Tenant{Provider: Name}) {
		if other.ID != res.ID && stringOf(other.Attrs["NetId"]) == req.NetID {
			p.conflict(w, "the Net "+req.NetID+" already has "+other.ID+" linked")
			return
		}
	}
	res.Attrs["NetId"] = req.NetID
	if !p.env.Store.Commit(res, p.env.Now()) {
		p.notFound(w, "internet service", req.InternetServiceID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func (p *Pack) unlinkInternetService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InternetServiceID string `json:"InternetServiceId"`
		NetID             string `json:"NetId"`
		DryRun            *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindInternetService, req.InternetServiceID)
	if !found {
		p.notFound(w, "internet service", req.InternetServiceID)
		return
	}
	if stringOf(res.Attrs["NetId"]) != req.NetID {
		p.badRequest(w, "the internet service "+res.ID+" is not linked to "+req.NetID)
		return
	}
	delete(res.Attrs, "NetId")
	if !p.env.Store.Commit(res, p.env.Now()) {
		p.notFound(w, "internet service", req.InternetServiceID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

func (p *Pack) deleteInternetService(w http.ResponseWriter, r *http.Request) {
	var req struct {
		InternetServiceID string `json:"InternetServiceId"`
		DryRun            *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	res, found := p.env.Store.Get(Name, kindInternetService, req.InternetServiceID)
	if !found {
		p.notFound(w, "internet service", req.InternetServiceID)
		return
	}
	// A linked gateway does not go: the real API refuses, and destroy order
	// (unlink, then delete) is behaviour Terraform depends on.
	if netID := stringOf(res.Attrs["NetId"]); netID != "" {
		p.conflict(w, "the internet service "+res.ID+" is still linked to "+netID)
		return
	}
	p.env.Store.Delete(Name, kindInternetService, req.InternetServiceID)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// internetServiceView emits NetId and State only when linked: measured, an
// unlinked gateway carries neither key.
func internetServiceView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"InternetServiceId": res.ID,
		"Tags":              res.Attrs["Tags"],
	}
	if netID := stringOf(res.Attrs["NetId"]); netID != "" {
		out["NetId"] = netID
		out["State"] = "available"
	}
	return out
}

// linkedInternetServiceOf finds the gateway linked to a Net, if any.
func (p *Pack) linkedInternetServiceOf(netID string) *resource.Resource {
	for _, res := range p.env.Store.List(kindInternetService, resource.Tenant{Provider: Name}) {
		if stringOf(res.Attrs["NetId"]) == netID {
			return res
		}
	}
	return nil
}
