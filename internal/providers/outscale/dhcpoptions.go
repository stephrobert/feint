package outscale

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// DHCP options, read-only for now: the default set every account has.
//
// Measured (X-2 sweep, 2026-08-08): the default set carries Default:true, a
// DomainName of "<region>.compute.internal" and DomainNameServers of exactly
// ["OutscaleProvidedDNS"] — a keyword, not an address. LogServers and NtpServers
// are omitted, not empty. Every Net references it through DhcpOptionsSetId.
//
// CreateDhcpOptions and DeleteDhcpOptions are backlog: implementable on this
// store, not yet asked for by anything this project drives.

const kindDhcpOptions = "dhcpoptions"

// defaultDhcpOptions returns the account's default set, creating it on first
// use. Lazy rather than seeded at start-up so that a state snapshot holding one
// is not duplicated when it is restored.
//
// Under the addressing lock of the caller when called from createNet; callable
// bare from a read, where two concurrent creations would be benign but wasteful,
// so reads go through the store first.
func (p *Pack) defaultDhcpOptions() *resource.Resource {
	for _, res := range p.env.Store.List(kindDhcpOptions, resource.Tenant{Provider: Name}) {
		if def, _ := res.Attrs["Default"].(bool); def {
			return res
		}
	}
	now := p.env.Now()
	res := &resource.Resource{
		ID:      newID("dopt", p.env.NewID()),
		Kind:    kindDhcpOptions,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"Default":           true,
			"DomainName":        regionName + ".compute.internal",
			"DomainNameServers": []any{"OutscaleProvidedDNS"},
			"Tags":              []any{},
		},
	}
	p.env.Store.Put(res)
	return res
}

type readDhcpOptionsRequest struct {
	Filters        filterSet `json:"Filters"`
	ResultsPerPage int       `json:"ResultsPerPage"`
	DryRun         *bool     `json:"DryRun"`
}

var dhcpOptionsFilters = []string{"DhcpOptionsSetIds", "DomainNames"}

func (p *Pack) readDhcpOptions(w http.ResponseWriter, r *http.Request) {
	var req readDhcpOptionsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, dhcpOptionsFilters...) {
		return
	}

	// The default set exists the moment anybody looks, which is how the real
	// account behaves: nobody ever created it there either.
	p.defaultDhcpOptions()

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindDhcpOptions, resource.Tenant{Provider: Name}) {
		domain := stringOf(res.Attrs["DomainName"])
		if !matchesStrings(req.Filters, "DhcpOptionsSetIds", res.ID) ||
			!matchesStrings(req.Filters, "DomainNames", domain) {
			continue
		}
		view := make(map[string]any, len(res.Attrs)+1)
		for k, v := range res.Attrs {
			view[k] = v
		}
		view["DhcpOptionsSetId"] = res.ID
		out = append(out, view)
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"DhcpOptionsSets": page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}
