package scaleway

import (
	"net/http"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The server as instance/v2alpha1 renders it, and the one operation a client
// drives on that door.
//
// # Why this file exists
//
// The Terraform provider released 2.83.0 on 14 September 2026 carrying
// `fix(instance): detach private network interface before deleting it`
// (upstream #4354), and every `terraform destroy` against this emulator broke:
//
//	Error: scaleway-sdk-go: http error 501 Not Implemented: feint does not serve
//	/instance/v2alpha1/zones/fr-par-1/servers/{id}/detach-private-network-interface
//
// This is the same afternoon privatenics_v2alpha1.go describes for 2.81.0, one
// release further on, and the drift machinery behaved the same way: the failure
// named the missing path instead of answering something wrong.
//
// # What the refusal said, and why it stopped being true
//
// The operation was in Declined(), under a reason that covered the whole alpha
// API: "no client this project drives reaches for these operations — a claim
// that held for the whole API until provider 2.81.0 moved private network
// interfaces and placement groups onto it". The sentence names its own expiry
// date, and 2.83.0 reached it.
//
// Only Detach is withdrawn from that list. `feint proxy` recorded a full apply
// plus destroy on 2.83.0: 35 v2alpha1 calls, of which the detach twice, and
// AttachServerPrivateNetworkInterface **never**. Serving the symmetric half
// because it is symmetric would be surface nothing drives, which is the trade
// this repository refuses on purpose — so the reason is split rather than
// deleted, and Attach keeps it.
//
// # What a detach does, and what it does not
//
// It dissociates; it does not delete. That is measured rather than reasoned,
// and the first version of this file got it wrong in the direction that still
// passed every suite. `feint proxy` on 2.83.0, four calls in order:
//
//	200  POST   …/servers/{id}/detach-private-network-interface
//	200  GET    …/private-network-interfaces/{id}     <- still there
//	204  DELETE …/private-network-interfaces/{id}     <- the client deletes it
//	404  GET    …/private-network-interfaces/{id}
//
// which is `detach private network interface before deleting it` spelled out as
// two calls. Deleting at the detach — what reusing releaseNIC did — answered the
// client's read with a 404, so it never sent its own DELETE, and the leg went
// green for a reason that was not the stated one. Both behaviours pass the
// suite; only one matches what the client does.
//
// The SDK said so too, without any measurement:
// DetachAndDeletePrivateNetworkInterface exists as a separate operation, which
// is only meaningful if the plain one leaves the interface in place — and that
// folded form stays declined, because no client sends it.
//
// # The status enum is not the v1 one
//
// instance/v1 says `running`; instance/v2alpha1 says **`started`**, and its enum
// holds no `running` at all (contracts/scaleway.json,
// instance/v2alpha1…Server.Status). Echoing the stored state would answer a
// value the API description forbids, and the contract check would refuse the
// response before any client could complain.
//
// TestAServerRendersTheStatusVocabularyOfItsOwnApi fails without the mapping.

// serversV2Path is the server family's door on the alpha API. Only one route
// under it is mounted, and the file header says why the others are not.
const serversV2Path = "/instance/v2alpha1/zones/{zone}/servers"

// serverStatusV2 translates instance/v1's state vocabulary into the one
// instance/v2alpha1 declares.
//
// `stopped in place` has no v2alpha1 spelling. It becomes `stopped` rather than
// `unknown_status`: the machine is off either way, and answering "unknown" for
// a state this emulator knows exactly would be the lie the project exists to
// avoid. A state nobody mapped answers `unknown_status`, which is the enum's own
// escape hatch rather than an invented one.
func serverStatusV2(state string) string {
	switch state {
	case "running":
		return "started"
	case "stopped", "stopped in place":
		return "stopped"
	case "starting", "stopping", "locked":
		return state
	default:
		return "unknown_status"
	}
}

// serverArchitectureV2 answers the enum's own vocabulary. The pack stores
// `x86_64`, which is a member; anything else says so rather than inventing.
func serverArchitectureV2(arch string) string {
	switch arch {
	case "x86_64", "aarch64":
		return arch
	default:
		return "unknown_architecture"
	}
}

// serverVolumeTypeV2 translates instance/v1's volume vocabulary into
// instance/v2alpha1's, which is a different set and not a superset:
//
//	v1        l_ssd, b_ssd, sbs_volume, scratch, unified
//	v2alpha1  l_ssd, sbs, scratch, unknown_volume_type
//
// `sbs_volume` is spelled `sbs` there, and the contract check caught the
// difference before any client did — the same class as `running` / `started`
// above, met twice more in one response.
//
// `b_ssd` and `unified` have no v2alpha1 spelling. They answer
// `unknown_volume_type`, the enum's own escape hatch, rather than being mapped
// onto `sbs` because the products look alike: this pack serves what the API
// describes, and a correspondence nobody measured is exactly the invented format
// rule 4 forbids. A client reading `unknown_volume_type` learns that the
// emulator will not guess; a client reading a guessed `sbs` learns something
// possibly false.
//
// TestAServerRendersTheVolumeVocabularyOfItsOwnApi fails without this.
func serverVolumeTypeV2(kind string) string {
	switch kind {
	case "sbs_volume":
		return "sbs"
	case "l_ssd", "scratch", "sbs":
		return kind
	default:
		return "unknown_volume_type"
	}
}

// interfaceStatusV2 is the vocabulary of an INTERFACE, which is not the
// vocabulary of a server: `unknown_status, available, syncing` on the public
// side, where a server says `started`. Answering the server's own status here
// was the first version of this file, and the contract check refused it.
func interfaceStatusV2(state string) string {
	switch state {
	case "available", "syncing":
		return state
	default:
		return "available"
	}
}

// serverV2View renders a stored server in the shape instance/v2alpha1 declares.
//
// The fields come from the contract rather than from memory
// (contracts/scaleway.json, instance/v2alpha1…Server: nineteen of them), and
// every one is answered: the omission gate (#88) judges a served response
// against what its document declares, and a field left out of a response the
// emulator does carry is exactly what it reports.
//
// Two of them are empty by modelling rather than by oversight, and both are
// already stated elsewhere in this pack: this emulator runs no filesystem
// product, and it mints no Windows RDP password.
func (p *Pack) serverV2View(res *resource.Resource) map[string]any {
	name, _ := res.Attrs["name"].(string)
	serverType, _ := res.Attrs["commercial_type"].(string)
	arch, _ := res.Attrs["arch"].(string)
	detail, _ := res.Attrs["state_detail"].(string)

	out := map[string]any{
		"id":                         res.ID,
		"name":                       name,
		"project_id":                 res.Tenant.Project,
		"zone":                       res.Tenant.Zone,
		"tags":                       orEmpty(tagsOf(res)),
		"server_type":                serverType,
		"status":                     serverStatusV2(res.State),
		"status_detail":              detail,
		"architecture":               serverArchitectureV2(arch),
		"created_at":                 res.Created.Format(time.RFC3339),
		"updated_at":                 res.Updated.Format(time.RFC3339),
		"rescue_mode":                false,
		"placement_group_id":         serverPlacementGroupID(res),
		"boot_volume_id":             serverBootVolumeID(res),
		"volumes":                    p.serverVolumesV2(res),
		"private_network_interfaces": p.serverInterfacesV2(res),
		// No filesystem product is emulated, so a server attaches none. Empty
		// rather than absent: the field is declared, and a client iterating it
		// must find a list.
		"filesystems": []any{},
		// Minted by the real cloud for Windows images this emulator does not
		// serve. Null rather than an object of empty strings, which would read
		// as a password that exists.
		"windows_rdp_password":     nil,
		"public_network_interface": p.serverPublicInterfaceV2(res),
	}
	return out
}

// serverPlacementGroupID answers the bare identifier v2alpha1 declares, where
// v1's own view serves an object. Stored under the v1 response key, so the two
// doors read one value rather than keeping two that can disagree.
func serverPlacementGroupID(res *resource.Resource) any {
	switch v := res.Attrs[attrServerPlacementGroup].(type) {
	case string:
		if v == "" {
			return nil
		}
		return v
	default:
		return nil
	}
}

// serverBootVolumeID is the identifier of volume "0", which is where this pack
// puts the root volume at create time.
func serverBootVolumeID(res *resource.Resource) any {
	vols, ok := res.Attrs["volumes"].(map[string]any)
	if !ok {
		return nil
	}
	root, ok := vols["0"].(map[string]any)
	if !ok {
		return nil
	}
	if id, ok := root["id"].(string); ok && id != "" {
		return id
	}
	return nil
}

// serverVolumesV2 renders the attached volumes as the two fields v2alpha1
// declares for them: an identifier and a type. The v1 view carries far more,
// and copying it here would answer a shape the API description does not have.
func (p *Pack) serverVolumesV2(res *resource.Resource) []any {
	stored, ok := res.Attrs["volumes"].(map[string]any)
	if !ok {
		return []any{}
	}
	out := make([]any, 0, len(stored))
	for _, raw := range stored {
		vol, ok := raw.(map[string]any)
		if !ok {
			continue
		}
		id, _ := vol["id"].(string)
		if id == "" {
			continue
		}
		kind, _ := vol["volume_type"].(string)
		out = append(out, map[string]any{"id": id, "volume_type": serverVolumeTypeV2(kind)})
	}
	return out
}

// serverInterfacesV2 renders the server's private interfaces in the nested
// shape, which is the v2alpha1 interface minus the fields that only make sense
// on the top-level resource.
func (p *Pack) serverInterfacesV2(res *resource.Resource) []any {
	out := make([]any, 0)
	for _, nic := range p.env.Store.List(kindPrivateNIC, res.Tenant) {
		if nic.Runtime[runtimeServerKey] != res.ID {
			continue
		}
		view := p.privateNetworkInterfaceView(nic)
		out = append(out, map[string]any{
			"id":                 view["id"],
			"private_network_id": view["private_network_id"],
			"mac_address":        view["mac_address"],
			"status":             view["status"],
			"ip_ids":             view["ip_ids"],
			// Declared on the nested shape and not on the top-level one. This
			// pack attaches groups to servers rather than to interfaces, so
			// there is no per-interface group to name.
			"security_group_id": nil,
		})
	}
	return out
}

// serverPublicInterfaceV2 renders the public side. A server with no public
// address has none, and answering an object of empty fields would describe an
// interface that does not exist.
func (p *Pack) serverPublicInterfaceV2(res *resource.Resource) any {
	ips := p.publicIPsOf(res)
	if len(ips) == 0 {
		return nil
	}
	return map[string]any{
		"ips": ips,
		// The emulator publishes no reverse for a server's own address, mints
		// no MAC on the public side, and attaches groups to the server rather
		// than to this interface.
		"dns":               nil,
		"mac_address":       nil,
		"security_group_id": nil,
		// The interface vocabulary, not the server one.
		"status": interfaceStatusV2("available"),
	}
}

type detachPrivateNetworkInterfaceRequest struct {
	PrivateNetworkInterfaceID string `json:"private_network_interface_id"`
}

// detachPrivateNetworkInterface dissociates an interface from its server.
//
// The interface survives: DetachAndDeletePrivateNetworkInterface is a separate
// upstream operation, which would be meaningless if this one deleted. The
// provider deletes afterwards through the door it already used, and the store is
// shared, so both doors see one object.
//
// TestADetachedInterfaceSurvivesItsDetach fails if this deletes, and
// TestADetachLeavesTheInterfaceOffItsServer fails if it does nothing.
func (p *Pack) detachPrivateNetworkInterface(w http.ResponseWriter, r *http.Request) {
	zone, ok := zoneOf(w, r)
	if !ok {
		return
	}
	serverID := r.PathValue("id")
	server, found := p.env.Store.Get(Name, kindServer, serverID)
	if !found || server.Tenant.Zone != zone {
		writeNotFound(w, "server", serverID)
		return
	}

	var req detachPrivateNetworkInterfaceRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "private_network_interface_id",
			Reason:       "constraint",
			HelpMessage:  err.Error(),
		})
		return
	}
	if req.PrivateNetworkInterfaceID == "" {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "private_network_interface_id",
			Reason:       "required",
		})
		return
	}

	nic, found := p.env.Store.Get(Name, kindPrivateNIC, req.PrivateNetworkInterfaceID)
	if !found || nic.Tenant.Zone != zone {
		writeNotFound(w, "private_nic", req.PrivateNetworkInterfaceID)
		return
	}
	// An interface of another server is not this server's to detach. The
	// identifier comes from a request body, so it is checked against what the
	// store says rather than trusted because it resolved.
	//
	// TestADetachRefusesAnInterfaceOfAnotherServer fails without this.
	if nic.Runtime[runtimeServerKey] != serverID {
		writeNotFound(w, "private_nic", req.PrivateNetworkInterfaceID)
		return
	}

	// The per-server hold deletePrivateNetworkInterface takes, for the same
	// reason: a detach crossing an attach on one machine is the race the driver
	// serialises rather than the store.
	unlock := p.binding().Serialise(serverID)
	defer unlock()

	// Dissocier, pas supprimer, and the client's own next two calls are why:
	// it reads the interface back (200) and then DELETEs it itself. Deleting here
	// answered that read with a 404 and the client skipped its DELETE — green for
	// a reason that was not the stated one.
	//
	// detachMachineFromNetwork first, because the names of both ends live on the
	// resource and a detach that cannot name its machine is a detach that never
	// happens. The addresses stay with the interface: it is still a NIC, just not
	// this server's, and releaseNIC's address handling belongs to the delete.
	p.detachMachineFromNetwork(r.Context(), nic)
	_ = p.env.Store.Update(Name, kindPrivateNIC, nic.ID, func(stored *resource.Resource) error {
		delete(stored.Runtime, runtimeServerKey)
		stored.Updated = p.env.Now()
		return nil
	})

	server, found = p.env.Store.Get(Name, kindServer, serverID)
	if !found {
		writeNotFound(w, "server", serverID)
		return
	}
	emulator.WriteJSON(w, http.StatusOK, p.serverV2View(server))
}
