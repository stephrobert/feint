package outscale

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The in-place half of resources this pack already creates, reads and deletes
// (#172).
//
// Create, read and delete of Nets, Subnets, Nics and route-table links are
// served and driven by the provider's own `examples/net_vm` fixture. The change
// a *second* `terraform apply` makes was not: a plan that modifies a Net this
// emulator created died on an operation nobody had decided about, and the four
// sat in the untriaged column with no issue owning them.
//
// Every request shape below is read from `contracts/outscale.json`, which is
// extracted from Outscale's own document. `UpdateNet` carries exactly
// DhcpOptionsSetId and NetId; `UpdateSubnet` MapPublicIpOnLaunch and SubnetId;
// `UpdateNic` a NicId plus the three things an interface can change; and
// `UpdateRouteTableLink` re-points a link at another table. None of it is
// invented, which is the rule this pack lives under.

// updateNet re-points a Net at another DHCP options set.
//
// The set has to exist. Accepting an unknown identifier would store a Net whose
// DhcpOptionsSetId answers a read nobody can resolve — the emulator inventing a
// relation the real cloud refuses, which is worse than a 400 because the apply
// succeeds and the drift appears later.
func (p *Pack) updateNet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NetID            string `json:"NetId"`
		DhcpOptionsSetID string `json:"DhcpOptionsSetId"`
		DryRun           *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.NetID == "" {
		p.badRequest(w, "NetId is required")
		return
	}
	if req.DhcpOptionsSetID == "" {
		p.badRequest(w, "DhcpOptionsSetId is required")
		return
	}

	res, found := p.env.Store.Get(Name, kindNet, req.NetID)
	if !found {
		p.notFound(w, "net", req.NetID)
		return
	}

	// Under the addressing lock, paired with the scan in deleteDhcpOptions:
	// without it a set can be deleted between the existence check below and the
	// Commit, and the Net ends up wearing an identifier that resolves to
	// nothing — the exact relation the existence check exists to refuse.
	unlock := p.lockAddresses()
	defer unlock()

	// `default` is a keyword, not an identifier: their document defines it on
	// this very field ("or `default` if you want to associate the default
	// one"), and the Terraform provider sends it verbatim when it detaches a
	// set before deleting it. Resolved here to the account's set, because what
	// a Net carries — and what every read answers — is always a dopt- id.
	// TestADhcpOptionsSetDoesNotDeleteUnderANet fails without this.
	if req.DhcpOptionsSetID == "default" {
		req.DhcpOptionsSetID = p.defaultDhcpOptions().ID
	} else if _, ok := p.env.Store.Get(Name, kindDhcpOptions, req.DhcpOptionsSetID); !ok {
		p.notFound(w, "dhcp options set", req.DhcpOptionsSetID)
		return
	}

	res.Attrs["DhcpOptionsSetId"] = req.DhcpOptionsSetID
	// Commit rather than Put: the Net may have been deleted while this request
	// was decoding, and Put would bring it back carrying the new value.
	if !p.env.Store.Commit(res, p.env.Now()) {
		p.notFound(w, "net", req.NetID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Net":             netView(res),
		"ResponseContext": p.context(),
	})
}

// updateSubnet flips whether a machine launched into the subnet gets a public
// address by default.
func (p *Pack) updateSubnet(w http.ResponseWriter, r *http.Request) {
	var req struct {
		SubnetID            string `json:"SubnetId"`
		MapPublicIPOnLaunch *bool  `json:"MapPublicIpOnLaunch"`
		DryRun              *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.SubnetID == "" {
		p.badRequest(w, "SubnetId is required")
		return
	}
	// A pointer, and required by the document: absent and false are different
	// requests, and reading a missing field as false would silently turn the
	// flag off on every call that forgot it.
	if req.MapPublicIPOnLaunch == nil {
		p.badRequest(w, "MapPublicIpOnLaunch is required")
		return
	}

	res, found := p.env.Store.Get(Name, kindSubnet, req.SubnetID)
	if !found {
		p.notFound(w, "subnet", req.SubnetID)
		return
	}
	res.Attrs["MapPublicIpOnLaunch"] = *req.MapPublicIPOnLaunch
	if !p.env.Store.Commit(res, p.env.Now()) {
		p.notFound(w, "subnet", req.SubnetID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Subnet":          p.subnetView(res),
		"ResponseContext": p.context(),
	})
}

// updateNic changes what an interface carries: its description, the groups it
// wears, and whether its attachment dies with the machine.
//
// Each field is optional and only what is sent is written. A PATCH that reset
// the fields a caller left out would be the emulator answering a request nobody
// made, and Terraform sends exactly the attributes that changed.
func (p *Pack) updateNic(w http.ResponseWriter, r *http.Request) {
	var req struct {
		NicID            string   `json:"NicId"`
		Description      *string  `json:"Description"`
		SecurityGroupIDs []string `json:"SecurityGroupIds"`
		LinkNic          *struct {
			LinkNicID          string `json:"LinkNicId"`
			DeleteOnVMDeletion *bool  `json:"DeleteOnVmDeletion"`
		} `json:"LinkNic"`
		DryRun *bool `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.NicID == "" {
		p.badRequest(w, "NicId is required")
		return
	}

	res, found := p.env.Store.Get(Name, kindNic, req.NicID)
	if !found {
		p.notFound(w, "nic", req.NicID)
		return
	}

	if req.Description != nil {
		res.Attrs["Description"] = *req.Description
	}
	if req.SecurityGroupIDs != nil {
		// Checked by the same helper createNic uses, so a group that does not
		// exist is refused here exactly as it is there rather than stored as a
		// dangling identifier — and stored under the same key, so a read after
		// an update answers what a read after a create answers.
		if !p.checkVMSecurityGroups(w, req.SecurityGroupIDs) {
			return
		}
		res.Attrs["SecurityGroupIds"] = req.SecurityGroupIDs
	}
	if req.LinkNic != nil && req.LinkNic.DeleteOnVMDeletion != nil {
		link, _ := res.Attrs["LinkNic"].(map[string]any)
		if link == nil {
			p.badRequest(w, "NicId "+req.NicID+" is not attached, so its attachment cannot be updated")
			return
		}
		link["DeleteOnVmDeletion"] = *req.LinkNic.DeleteOnVMDeletion
		res.Attrs["LinkNic"] = link
	}

	if !p.env.Store.Commit(res, p.env.Now()) {
		p.notFound(w, "nic", req.NicID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Nic":             p.storedNicView(res),
		"ResponseContext": p.context(),
	})
}

// updateRouteTableLink re-points an existing link at another route table, which
// is how a subnet changes its routing without losing it for the time a delete
// and a create would take.
func (p *Pack) updateRouteTableLink(w http.ResponseWriter, r *http.Request) {
	var req struct {
		LinkRouteTableID string `json:"LinkRouteTableId"`
		RouteTableID     string `json:"RouteTableId"`
		DryRun           *bool  `json:"DryRun"`
	}
	if err := emulator.DecodeJSON(r, &req); err != nil {
		p.badRequest(w, err.Error())
		return
	}
	if req.LinkRouteTableID == "" || req.RouteTableID == "" {
		p.badRequest(w, "LinkRouteTableId and RouteTableId are both required")
		return
	}

	target, found := p.env.Store.Get(Name, kindRouteTable, req.RouteTableID)
	if !found {
		p.notFound(w, "route table", req.RouteTableID)
		return
	}

	// The link lives on whichever table currently holds it, so it is found by
	// walking them rather than by asking the caller which one it was on: a
	// client that knew that would not need this call.
	for _, table := range p.env.Store.List(kindRouteTable, resource.Tenant{Provider: Name}) {
		links := listOf(table.Attrs["LinkRouteTables"])
		for i, raw := range links {
			link, _ := raw.(map[string]any)
			if stringOf(link["LinkRouteTableId"]) != req.LinkRouteTableID {
				continue
			}
			if table.ID == req.RouteTableID {
				// Already there. Answering success rather than moving nothing
				// twice, because a re-plan that produces the same link must not
				// look like a change.
				emulator.WriteJSON(w, http.StatusOK, map[string]any{
					"LinkRouteTableId": req.LinkRouteTableID,
					"ResponseContext":  p.context(),
				})
				return
			}
			if stringOf(target.Attrs["NetId"]) != stringOf(table.Attrs["NetId"]) {
				p.conflict(w, "route table "+req.RouteTableID+" is in another Net than the link being moved")
				return
			}

			// The link moves whole: only RouteTableId changes, and every other
			// key — Main, NetId, and the presence or absence of SubnetId —
			// travels with it. A first version rebuilt the link with Main:false
			// and an unconditional SubnetId, which left a Net with no main
			// table the moment its main link was re-pointed: that is the exact
			// call `outscale_main_route_table_link` makes, and its read filters
			// on LinkRouteTableMain=true before finding the link back by
			// LinkRouteTableId. TestUpdateRouteTableLinkMovesTheMainLinkWhole
			// fails without this.
			//
			// Copies, not mutations: nested values inside Attrs are shared with
			// the store until Commit (resource.Clone documents it), so both the
			// shrunk list and the moved link are built fresh.
			rest := make([]any, 0, len(links)-1)
			rest = append(rest, links[:i]...)
			rest = append(rest, links[i+1:]...)
			table.Attrs["LinkRouteTables"] = rest
			if !p.env.Store.Commit(table, p.env.Now()) {
				p.notFound(w, "route table", table.ID)
				return
			}
			moved := make(map[string]any, len(link))
			for k, v := range link {
				moved[k] = v
			}
			moved["RouteTableId"] = req.RouteTableID
			target.Attrs["LinkRouteTables"] = append(listOf(target.Attrs["LinkRouteTables"]), moved)
			if !p.env.Store.Commit(target, p.env.Now()) {
				p.notFound(w, "route table", req.RouteTableID)
				return
			}
			emulator.WriteJSON(w, http.StatusOK, map[string]any{
				"LinkRouteTableId": req.LinkRouteTableID,
				"ResponseContext":  p.context(),
			})
			return
		}
	}
	p.notFound(w, "route table link", req.LinkRouteTableID)
}
