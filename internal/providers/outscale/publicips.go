package outscale

import (
	"fmt"
	"net/http"
	"strings"

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
// LinkPublicIp and UnlinkPublicIp are served in the CONTROL PLANE ONLY: the
// record moves, no packet does. They were declined at first on the argument that
// publishing an address is the runtime's job — defensible, and wrong on the
// evidence: the provider's own examples/net_vm fixture holds an
// `outscale_public_ip_link`, so the refusal failed the apply partway through and
// left the twelve other resources unproven. A stated limit beats an apply that
// cannot finish; docs/limits.md carries it.
//
// Shape measured (X-2 sweep, 2026-08-08): an unlinked address carries exactly
// PublicIp, PublicIpId and Tags — no Vm/Nic/NatService keys, not even empty.
// One consumed by a NAT service gains NatServiceId and LinkPublicIpId; one
// linked to a machine gains VmId, NicId, NicAccountId and PrivateIp.

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

// publicIPFilters are what a stored address can answer. LinkPublicIpIds is here
// because the Terraform provider reads the link back by its own id right after
// creating it — without it, `outscale_public_ip_link` fails the apply.
var publicIPFilters = []string{"PublicIpIds", "PublicIps", "LinkPublicIpIds", "VmIds", "NicIds"}

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
			!matchesStrings(req.Filters, "PublicIps", stringOf(res.Attrs["PublicIp"])) ||
			!matchesStrings(req.Filters, "LinkPublicIpIds", stringOf(res.Attrs["LinkPublicIpId"])) ||
			!matchesStrings(req.Filters, "VmIds", stringOf(res.Attrs["VmId"])) ||
			!matchesStrings(req.Filters, "NicIds", stringOf(res.Attrs["NicId"])) {
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

// publicIPView emits a holder's fields only when there is a holder: measured, an
// unlinked address carries PublicIp, PublicIpId and Tags and nothing else, while
// one linked to a machine gains VmId, NicId, NicAccountId and PrivateIp, and one
// consumed by a NAT service gains NatServiceId. Absent and empty are not the
// same claim.
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
	if vmID := stringOf(res.Attrs["VmId"]); vmID != "" {
		out["VmId"] = vmID
		out["LinkPublicIpId"] = stringOf(res.Attrs["LinkPublicIpId"])
		out["NicId"] = stringOf(res.Attrs["NicId"])
		out["NicAccountId"] = accountID
		if privateIP := stringOf(res.Attrs["PrivateIp"]); privateIP != "" {
			out["PrivateIp"] = privateIP
		}
	}
	return out
}

// linkPublicIP attaches an address to a machine, in the control plane only.
//
// What this does NOT do is route a packet, and saying so is the point: the
// address comes from a documented-fictional block, nothing on the host carries
// it, and no NAT rule is written. docs/limits.md records it. The reason it is
// served rather than declined is measured: the provider's own examples/net_vm
// fixture holds an `outscale_public_ip_link`, so declining it fails the apply on
// the eleventh resource of thirteen — and an apply that cannot complete proves
// nothing about the twelve that worked.
//
// The fields it fills come from the read sweep: an address linked to a machine
// answers with VmId, NicId, NicAccountId and PrivateIp beside its own.
func (p *Pack) linkPublicIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		PublicIPID  string `json:"PublicIpId"`
		PublicIP    string `json:"PublicIp"`
		VMID        string `json:"VmId"`
		NicID       string `json:"NicId"`
		AllowRelink *bool  `json:"AllowRelink"`
		DryRun      *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	address := p.publicIPByRef(req.PublicIPID, req.PublicIP)
	if address == nil {
		p.notFound(w, "public IP", orDefault(req.PublicIPID, req.PublicIP))
		return
	}
	if natID := stringOf(address.Attrs["NatServiceId"]); natID != "" {
		p.conflict(w, "the public IP "+address.ID+" is held by "+natID)
		return
	}
	// AllowRelink false is the API's own default, and moving an address that is
	// already placed must be asked for rather than assumed.
	if holder := stringOf(address.Attrs["VmId"]); holder != "" && holder != req.VMID && !boolOr(req.AllowRelink, false) {
		p.conflict(w, "the public IP "+address.ID+" is already linked to "+holder+"; pass AllowRelink to move it")
		return
	}
	if req.VMID == "" && req.NicID == "" {
		p.badRequest(w, "VmId or NicId is required")
		return
	}

	privateIP, nicID := "", req.NicID
	if req.VMID != "" {
		vm, found := p.env.Store.Get(Name, kindVM, req.VMID)
		if !found || vm.State == stateTerminated {
			p.notFound(w, "Vm", req.VMID)
			return
		}
		privateIP = stringOf(vm.Attrs["PrivateIp"])
		if nicID == "" && stringOf(vm.Attrs["SubnetId"]) != "" {
			// The machine's primary interface, named the way readNics derives it.
			nicID = "eni-" + strings.TrimPrefix(vm.ID, "i-")
		}
	}
	// One address per machine, which the real API enforces: a second link on a
	// machine that already has one is a conflict, not a silent second address.
	for _, other := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		if other.ID != address.ID && req.VMID != "" && stringOf(other.Attrs["VmId"]) == req.VMID {
			p.conflict(w, "the Vm "+req.VMID+" already carries "+other.ID)
			return
		}
	}

	linkID := newID("eipassoc", p.env.NewID())
	address.Attrs["VmId"] = req.VMID
	address.Attrs["NicId"] = nicID
	address.Attrs["LinkPublicIpId"] = linkID
	if privateIP != "" {
		address.Attrs["PrivateIp"] = privateIP
	}
	if !p.env.Store.Commit(address, p.env.Now()) {
		p.notFound(w, "public IP", address.ID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"LinkPublicIpId":  linkID,
		"ResponseContext": p.context(),
	})
}

func (p *Pack) unlinkPublicIP(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LinkPublicIPID string `json:"LinkPublicIpId"`
		PublicIP       string `json:"PublicIp"`
		DryRun         *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	for _, address := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		linked := stringOf(address.Attrs["LinkPublicIpId"])
		if linked == "" || stringOf(address.Attrs["VmId"]) == "" {
			continue
		}
		if linked != req.LinkPublicIPID && stringOf(address.Attrs["PublicIp"]) != req.PublicIP {
			continue
		}
		p.releasePublicIP(address)
		emulator.WriteJSON(w, http.StatusOK, map[string]any{"ResponseContext": p.context()})
		return
	}
	p.notFound(w, "public IP link", orDefault(req.LinkPublicIPID, req.PublicIP))
}

// releasePublicIP drops the machine-side link, leaving the address allocated.
func (p *Pack) releasePublicIP(address *resource.Resource) {
	delete(address.Attrs, "VmId")
	delete(address.Attrs, "NicId")
	delete(address.Attrs, "LinkPublicIpId")
	delete(address.Attrs, "PrivateIp")
	_ = p.env.Store.Commit(address, p.env.Now())
}

// publicIPByRef resolves an address by id or by value, the two ways every
// public-IP request may name one.
func (p *Pack) publicIPByRef(id, value string) *resource.Resource {
	if id != "" {
		res, _ := p.env.Store.Get(Name, kindPublicIP, id)
		return res
	}
	if value == "" {
		return nil
	}
	for _, res := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		if stringOf(res.Attrs["PublicIp"]) == value {
			return res
		}
	}
	return nil
}

// publicIPOf is the address linked to a machine, empty when it has none. The Vm
// view publishes it, because a real Vm carrying an EIP answers with one and the
// Terraform provider reads it back.
func (p *Pack) publicIPOf(vmID string) string {
	for _, res := range p.env.Store.List(kindPublicIP, resource.Tenant{Provider: Name}) {
		if stringOf(res.Attrs["VmId"]) == vmID {
			return stringOf(res.Attrs["PublicIp"])
		}
	}
	return ""
}
