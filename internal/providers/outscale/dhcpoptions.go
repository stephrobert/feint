package outscale

import (
	"net/http"
	"net/netip"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// DHCP options: the default set every account has, and the sets a client
// creates (#172).
//
// Measured (X-2 sweep, 2026-08-08): the default set carries Default:true, a
// DomainName of "<region>.compute.internal" and DomainNameServers of exactly
// ["OutscaleProvidedDNS"] — a keyword, not an address. LogServers and NtpServers
// are omitted, not empty. Every Net references it through DhcpOptionsSetId.
//
// The lifecycle shapes are read from contracts/outscale.json. Their document
// adds two behaviours the schemas alone do not carry, both quoted from the
// operation descriptions: a create must name at least one option, and the
// default set cannot be deleted — nor can any set a Net still wears, which is
// why the Terraform provider re-points every attached Net at the `default`
// keyword before it deletes (resource_outscale_dhcp_option.go, detachDHCPs).

const kindDhcpOptions = "dhcpoptions"

// defaultDhcpOptions returns the account's default set, creating it on first
// use. Lazy rather than seeded at start-up so that a state snapshot holding one
// is not duplicated when it is restored.
//
// Under the addressing lock of the caller when called from createNet; callable
// bare from a read, where two concurrent creations would be benign but wasteful,
// so reads go through the store first.
// outscaleProvidedDNS is the keyword the platform answers where an address
// would stand, on the default set and on any set that names no server.
const outscaleProvidedDNS = "OutscaleProvidedDNS"

func (p *Pack) defaultDhcpOptions() *resource.Resource {
	for _, res := range p.env.Store.List(kindDhcpOptions, resource.Tenant{Provider: Name}) {
		if def, _ := res.Attrs["Default"].(bool); def {
			return res
		}
	}
	now := p.env.Now()
	res := resource.New(newID("dopt", p.env.NewID()), kindDhcpOptions, resource.Tenant{Provider: Name}, "available", now)
	res.Attrs = map[string]any{
		"Default":           true,
		"DomainName":        p.region + ".compute.internal",
		"DomainNameServers": []any{outscaleProvidedDNS},
		"Tags":              []any{},
	}
	p.env.Store.Put(res)
	return res
}

type readDhcpOptionsRequest struct {
	Filters        filterSet `json:"Filters"`
	ResultsPerPage *int      `json:"ResultsPerPage"`
	DryRun         *bool     `json:"DryRun"`
}

var dhcpOptionsFilters = stringFilters("DhcpOptionsSetIds", "DomainNames")

func (p *Pack) readDhcpOptions(w http.ResponseWriter, r *http.Request) {
	var req readDhcpOptionsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if p.refusePageSize(w, req.ResultsPerPage) {
		return
	}
	if p.refuseFilters(w, req.Filters, dhcpOptionsFilters) {
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
		out = append(out, dhcpOptionsView(res))
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"DhcpOptionsSets": page(out, pageSize(req.ResultsPerPage)),
		"ResponseContext": p.context(),
	})
}

// dhcpOptionsView is the wire shape of a set: the stored attributes plus the
// identifier the core owns. LogServers and NtpServers appear only when the set
// carries them, which is how the measured default set behaves — omitted, not
// empty.
func dhcpOptionsView(res *resource.Resource) map[string]any {
	view := make(map[string]any, len(res.Attrs)+1)
	for k, v := range res.Attrs {
		view[k] = v
	}
	view["DhcpOptionsSetId"] = res.ID
	return view
}

type createDhcpOptionsRequest struct {
	DomainName        string   `json:"DomainName"`
	DomainNameServers []string `json:"DomainNameServers"`
	LogServers        []string `json:"LogServers"`
	NtpServers        []string `json:"NtpServers"`
	DryRun            *bool    `json:"DryRun"`
}

func (p *Pack) createDhcpOptions(w http.ResponseWriter, r *http.Request) {
	var req createDhcpOptionsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	// At least one option. Their document states it on each of the four fields
	// ("You must specify at least one of the following parameters: DomainName,
	// DomainNameServers, LogServers, or NtpServers"), and the Terraform
	// provider refuses the same request client-side. Without this an empty
	// create stores a set that configures nothing and can never be read back as
	// anything the real cloud would hold.
	// TestCreateDhcpOptionsRequiresAtLeastOneOption fails without it.
	if req.DomainName == "" && len(req.DomainNameServers) == 0 &&
		len(req.LogServers) == 0 && len(req.NtpServers) == 0 {
		p.badRequest(w, "you must specify at least one of the following parameters: "+
			"DomainName, DomainNameServers, LogServers, or NtpServers")
		return
	}

	// Every name server is an address, and the cloud says so before it stores
	// anything. Measured on 2026-08-21 against a real account
	// (corpus/outscale/oapi-cli-refusals.jsonl): CreateDhcpOptions with a
	// DomainNameServers entry that is not an IPv4 address answers 400
	// InvalidParameterValue "The IPv4 address '<value>' is invalid.", where this
	// pack answered 200 and stored the string — which then reads back as a name
	// server no machine can use.
	// TestCreateDhcpOptionsRefusesAServerThatIsNotAnAddress fails without it.
	for _, server := range req.DomainNameServers {
		// The keyword the platform answers on the default set is not an
		// address and is not refused: it is the one value of this field that
		// names the cloud's own resolver.
		if server == outscaleProvidedDNS {
			continue
		}
		if addr, err := netip.ParseAddr(server); err != nil || !addr.Is4() {
			p.badRequest(w, "the IPv4 address "+server+" is invalid")
			return
		}
	}

	now := p.env.Now()
	attrs := map[string]any{
		"Default": false,
		"Tags":    []any{},
	}
	if req.DomainName != "" {
		attrs["DomainName"] = req.DomainName
	}
	// "If no IPs are specified, the OutscaleProvidedDNS value is set by
	// default" — the keyword, exactly as the default set carries it.
	servers := req.DomainNameServers
	if len(servers) == 0 {
		servers = []string{outscaleProvidedDNS}
	}
	attrs["DomainNameServers"] = servers
	if len(req.LogServers) > 0 {
		attrs["LogServers"] = req.LogServers
	}
	if len(req.NtpServers) > 0 {
		attrs["NtpServers"] = req.NtpServers
	}

	res := resource.New(newID("dopt", p.env.NewID()), kindDhcpOptions, resource.Tenant{Provider: Name}, "available", now)
	res.Attrs = attrs
	p.env.Store.Put(res)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"DhcpOptionsSet":  dhcpOptionsView(res),
		"ResponseContext": p.context(),
	})
}

type deleteDhcpOptionsRequest struct {
	DhcpOptionsSetID string `json:"DhcpOptionsSetId"`
	DryRun           *bool  `json:"DryRun"`
}

func (p *Pack) deleteDhcpOptions(w http.ResponseWriter, r *http.Request) {
	var req deleteDhcpOptionsRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.DhcpOptionsSetID == "" {
		p.badRequest(w, "DhcpOptionsSetId is required")
		return
	}

	res, found := p.env.Store.Get(Name, kindDhcpOptions, req.DhcpOptionsSetID)
	if !found {
		p.notFound(w, "DHCP options set", req.DhcpOptionsSetID)
		return
	}
	// "You cannot delete the `default` set" — their document, under
	// DeleteDhcpOptions, in bold. A conflict rather than a bad request: the
	// argument is well-formed and names a real set, it is the set's own role
	// that forbids the operation, and osc.IsConflict is the helper a client
	// branches on for a refused delete.
	// TestTheDefaultDhcpOptionsSetDoesNotDelete fails without this.
	if isDefault, _ := res.Attrs["Default"].(bool); isDefault {
		p.conflict(w, "the DHCP options set "+req.DhcpOptionsSetID+
			" is the account's default set, which cannot be deleted")
		return
	}

	// A set a Net still wears does not go: "Before deleting a DHCP options
	// set, you must disassociate it from the Nets you associated it with"
	// (same document). The Terraform provider counts on the order rather than
	// the refusal — it re-points every attached Net first — but a raw client
	// deleting an attached set must be refused, or ReadNets would answer an
	// identifier that resolves to nothing.
	//
	// Under the addressing lock, which is what serialises this scan against
	// updateNet re-pointing a Net between the check and the delete.
	// TestADhcpOptionsSetDoesNotDeleteUnderANet fails without the guard.
	unlock := p.lockAddresses()
	defer unlock()
	for _, net := range p.env.Store.List(kindNet, resource.Tenant{Provider: Name}) {
		if stringOf(net.Attrs["DhcpOptionsSetId"]) == req.DhcpOptionsSetID {
			p.conflict(w, "the DHCP options set "+req.DhcpOptionsSetID+
				" is still associated with the Net "+net.ID)
			return
		}
	}
	p.env.Store.Delete(Name, kindDhcpOptions, req.DhcpOptionsSetID)

	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"ResponseContext": p.context(),
	})
}
