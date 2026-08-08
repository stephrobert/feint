package outscale

import (
	"fmt"
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// Public IPs: allocate, list, release.
//
// The addresses come from 203.0.113.0/24 — TEST-NET-3, reserved by RFC 5737 for
// documentation and never routed. An emulator that handed out addresses from a
// real public block would let a test half-work against the real internet, which
// is worse than an address that visibly goes nowhere. ReadPublicIpRanges
// publishes the same block, so the catalogue and the allocator cannot disagree.
//
// LinkPublicIp and UnlinkPublicIp are declined, not backlog: publishing an
// address on a machine is the runtime's job (machines.go publishes what the
// driver reports), and a link that stamps an address the interface does not
// carry would make ReadVms describe connectivity that does not exist — the
// exact lie this project exists to avoid. When the runtime learns to carry a
// routed address, the link calls come off the declined list.
//
// Shape measured (X-2 sweep, 2026-08-08): an unlinked address carries exactly
// PublicIp, PublicIpId and Tags — no Vm/Nic/NatService keys, not even empty.
// One consumed by a NAT service gains NatServiceId and LinkPublicIpId.

const kindPublicIP = "publicip"

// publicIPBase is the fictional block addresses are allocated from,
// sequentially from .1: deterministic, so a test can pin the first address.
const publicIPBase = "203.0.113."

func (p *Pack) createPublicIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		DryRun *bool `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}

	// Serialized like every other allocation: two concurrent creates must not
	// pick the same address.
	p.addresses.Lock()
	taken := make(map[string]bool, 8)
	for _, res := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		taken[stringOf(res.Attrs["PublicIp"])] = true
	}
	address := ""
	for host := 1; host < 255; host++ {
		candidate := fmt.Sprintf("%s%d", publicIPBase, host)
		if !taken[candidate] {
			address = candidate
			break
		}
	}
	if address == "" {
		p.addresses.Unlock()
		p.conflict(w, "the emulated public block "+publicIPBase+"0/24 is exhausted; release an address first")
		return
	}
	now := p.env.Now()
	res := &resource.Resource{
		ID:      newID("eipalloc", p.env.NewID()),
		Kind:    kindPublicIP,
		Tenant:  resource.Tenant{Provider: Name},
		State:   "available",
		Created: now,
		Updated: now,
		Attrs: map[string]any{
			"PublicIp": address,
			"Tags":     []any{},
		},
	}
	p.env.Store.Put(res)
	p.addresses.Unlock()

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"PublicIp":        publicIPView(res),
		"ResponseContext": p.context(),
	})
}

var publicIPFilters = []string{"PublicIpIds", "PublicIps"}

func (p *Pack) readPublicIPs(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Filters        filterSet `json:"Filters"`
		ResultsPerPage int       `json:"ResultsPerPage"`
		DryRun         *bool     `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refuseUnsupported(w, req.Filters, publicIPFilters...) {
		return
	}

	out := make([]map[string]any, 0)
	for _, res := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		if !matchesStrings(req.Filters, "PublicIpIds", res.ID) ||
			!matchesStrings(req.Filters, "PublicIps", stringOf(res.Attrs["PublicIp"])) {
			continue
		}
		out = append(out, publicIPView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"PublicIps":       page(out, req.ResultsPerPage),
		"ResponseContext": p.context(),
	})
}

func (p *Pack) deletePublicIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicIPID string `json:"PublicIpId"`
		PublicIP   string `json:"PublicIp"`
		DryRun     *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	// The request names the address by id or by value, and the API accepts
	// either; by id wins when both are sent.
	var res *resource.Resource
	if req.PublicIPID != "" {
		res, _ = p.env.Store.Get(Name, kindPublicIP, req.PublicIPID)
	} else if req.PublicIP != "" {
		for _, candidate := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
			if stringOf(candidate.Attrs["PublicIp"]) == req.PublicIP {
				res = candidate
				break
			}
		}
	}
	if res == nil {
		p.notFound(w, "public IP", orDefault(req.PublicIPID, req.PublicIP))
		return
	}
	// An address a NAT service holds does not go; the real API answers the same
	// and the NAT service's teardown is what releases it.
	if natID := stringOf(res.Attrs["NatServiceId"]); natID != "" {
		p.conflict(w, "the public IP "+res.ID+" is held by "+natID)
		return
	}
	p.env.Store.Delete(Name, kindPublicIP, res.ID)
	emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
}

// publicIPView emits the NAT fields only when a NAT service holds the address:
// measured, an unlinked address carries no such keys at all.
func publicIPView(res *resource.Resource) map[string]any {
	out := map[string]any{
		"PublicIpId": res.ID,
		"PublicIp":   stringOf(res.Attrs["PublicIp"]),
		"Tags":       res.Attrs["Tags"],
	}
	if natID := stringOf(res.Attrs["NatServiceId"]); natID != "" {
		out["NatServiceId"] = natID
		out["LinkPublicIpId"] = stringOf(res.Attrs["LinkPublicIpId"])
	}
	return out
}
