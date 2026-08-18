package outscale

import (
	"errors"
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The refusals this file answers from inside the store lock (#295, #299).
var (
	errNicNotAttached  = errors.New("the interface is not attached")
	errWrongAttachment = errors.New("the request names another attachment")
	errRouteLinkMoved  = errors.New("the link left this table while the request was deciding")
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

	if _, found := p.env.Store.Get(Name, kindNet, req.NetID); !found {
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

	// Inside the store lock, on the stored Net: written back wholesale from a
	// clone, this erased a concurrent write to another field of the same Net —
	// its tags — after their 200 (#295). Update also keeps a Net deleted while
	// this request was deciding deleted, which is what Commit was here for.
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindNet, req.NetID, func(stored *resource.Resource) error {
		stored.Attrs["DhcpOptionsSetId"] = req.DhcpOptionsSetID
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		p.notFound(w, "net", req.NetID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Net":             netView(updated),
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

	// Inside the store lock, same reason as updateNet (#295).
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindSubnet, req.SubnetID, func(stored *resource.Resource) error {
		stored.Attrs["MapPublicIpOnLaunch"] = *req.MapPublicIPOnLaunch
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	if err != nil {
		p.notFound(w, "subnet", req.SubnetID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Subnet":          p.subnetView(updated),
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

	if _, found := p.env.Store.Get(Name, kindNic, req.NicID); !found {
		p.notFound(w, "nic", req.NicID)
		return
	}
	if req.SecurityGroupIDs != nil {
		// Checked by the same helper createNic uses, so a group that does not
		// exist is refused here exactly as it is there rather than stored as a
		// dangling identifier — and stored under the same key, so a read after
		// an update answers what a read after a create answers.
		if !p.checkVMSecurityGroups(w, req.SecurityGroupIDs) {
			return
		}
	}

	// Inside the store lock, same reason as updateNet (#295). The LinkNic map
	// is copied before it changes, never written through: resource.Clone is
	// shallow on Attrs values, so `link["DeleteOnVmDeletion"] = …` on the clone
	// mutated the stored map in place — visible to every concurrent reader
	// before the write-back, and left behind even when the write-back failed.
	// TestUpdateNicDoesNotMutateTheStoredLinkInPlace fails without the copy.
	var updated *resource.Resource
	err := p.env.Store.Update(Name, kindNic, req.NicID, func(stored *resource.Resource) error {
		if req.Description != nil {
			stored.Attrs["Description"] = *req.Description
		}
		if req.SecurityGroupIDs != nil {
			stored.Attrs["SecurityGroupIds"] = req.SecurityGroupIDs
		}
		if req.LinkNic != nil && req.LinkNic.DeleteOnVMDeletion != nil {
			// The attachment lives in the flat keys linkNic writes; the
			// LinkNic map is the shape a foreign snapshot restores, and it
			// stays readable rather than silently dropped — a restored state
			// is untrusted input, not an invalid one. This branch answered
			// 400 "not attached" for every NIC this pack itself attached,
			// because it read only the map nothing here ever wrote (#299).
			attachmentID := stringOf(stored.Attrs["LinkNicId"])
			link, hasMap := stored.Attrs["LinkNic"].(map[string]any)
			if attachmentID == "" && hasMap {
				attachmentID = stringOf(link["LinkNicId"])
			}
			if attachmentID == "" {
				return errNicNotAttached
			}
			// "If you are modifying the `DeleteOnVmDeletion` attribute, you
			// must specify the ID of the NIC attachment" — LinkNicToUpdate,
			// osc-sdk-go client.gen.go. An absent or foreign ID is refused,
			// not guessed around.
			if req.LinkNic.LinkNicID != attachmentID {
				return errWrongAttachment
			}
			stored.Attrs["DeleteOnVmDeletion"] = *req.LinkNic.DeleteOnVMDeletion
			if hasMap {
				fresh := make(map[string]any, len(link)+1)
				for k, v := range link {
					fresh[k] = v
				}
				fresh["DeleteOnVmDeletion"] = *req.LinkNic.DeleteOnVMDeletion
				stored.Attrs["LinkNic"] = fresh
			}
		}
		stored.Updated = p.env.Now()
		updated = stored
		return nil
	})
	switch {
	case errors.Is(err, errNicNotAttached):
		p.badRequest(w, "NicId "+req.NicID+" is not attached, so its attachment cannot be updated")
		return
	case errors.Is(err, errWrongAttachment):
		p.badRequest(w, "LinkNic.LinkNicId must name the attachment of "+req.NicID+
			": modifying DeleteOnVmDeletion requires the ID of the NIC attachment")
		return
	case err != nil:
		p.notFound(w, "nic", req.NicID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"Nic":             p.storedNicView(updated),
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
		for _, raw := range links {
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
			// the store (resource.Clone documents it), so the shrunk list, the
			// grown list and the moved link are all built fresh. And each write
			// runs inside the store lock, re-finding the link on the stored
			// table: written back wholesale from the walk's clones, this erased
			// a concurrent write to another field of either table — a route, a
			// tag — after its 200 (#295).
			var moved map[string]any
			err := p.env.Store.Update(Name, kindRouteTable, table.ID, func(stored *resource.Resource) error {
				held := listOf(stored.Attrs["LinkRouteTables"])
				for j, rawHeld := range held {
					current, _ := rawHeld.(map[string]any)
					if stringOf(current["LinkRouteTableId"]) != req.LinkRouteTableID {
						continue
					}
					rest := make([]any, 0, len(held)-1)
					rest = append(rest, held[:j]...)
					rest = append(rest, held[j+1:]...)
					stored.Attrs["LinkRouteTables"] = rest
					stored.Updated = p.env.Now()
					moved = make(map[string]any, len(current)+1)
					for k, v := range current {
						moved[k] = v
					}
					return nil
				}
				return errRouteLinkMoved
			})
			if err != nil {
				p.notFound(w, "route table link", req.LinkRouteTableID)
				return
			}
			moved["RouteTableId"] = req.RouteTableID
			err = p.env.Store.Update(Name, kindRouteTable, req.RouteTableID, func(stored *resource.Resource) error {
				held := listOf(stored.Attrs["LinkRouteTables"])
				grown := make([]any, 0, len(held)+1)
				grown = append(grown, held...)
				grown = append(grown, moved)
				stored.Attrs["LinkRouteTables"] = grown
				stored.Updated = p.env.Now()
				return nil
			})
			if err != nil {
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
