package scaleway

import (
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// What this cloud answers to a create naming a project that does not exist.
//
// Recorded against a real fr-par account on 2026-08-21 in
// corpus/scaleway/scw-refusals.jsonl (#390), and extended on 2026-09-02 with the
// three products that recording does not cover, because the corpus redacts
// values and #391 needs the words:
//
//	403 {"type": "permissions_denied", "message": "insufficient permissions",
//	     "details": [{"resource": …, "action": …}]}
//
//	  instance/v1  CreateServer, CreateIP, CreateSecurityGroup,
//	               CreatePlacementGroup   resource project,      action read
//	  lb/v1        CreateLB, CreateIP     resource loadbalancer, action write
//	  block/v1     CreateVolume           resource volume,       action write
//	  iam/v1alpha1 CreateSSHKey           resource ssh_key,      action create
//
//	404 {"type": "not_found", "message": "resource is not found",
//	     "resource": "project", "resource_id": "<the identifier>"}
//
//	  vpc/v2 CreateVPC, vpcgw/v2 CreateIP and CreateGateway, ipam/v1 BookIP
//
// Two shapes, not seven, and what varies inside the first one is a field rather
// than a dialect: only instance/v1 names the project at all. lb, block and iam
// each name their OWN product and the action the caller was not allowed to take,
// which is a different sentence — "you may not write a volume" rather than "that
// project is not yours to read". A client branches on the status first and on
// `type` second, so answering one product in another's shape sends it down the
// wrong path with a plausible-looking body. Rule 4: the bodies are in the
// recording.
//
// Why a project is refusable at all, when docs/limits.md says identifiers are
// not checked against anything: a project is not like an image id. It is the
// boundary every other resource is filed under, the register holds the ones the
// operator declared AND the ones a client created, and an operator whose stack
// carries a production identifier declares it (`--projects name=<id>`). The rule
// that stands is the one about identifiers this emulator cannot know; a project
// it can.

// projectRefusal is how one product answers a project it does not hold. The zero
// value is the 404 shape, so a product that names no resource gets the answer
// vpc gives rather than a half-filled 403.
type projectRefusal struct {
	resource string
	action   string
}

var (
	// projectAbsent is vpc/v2, vpcgw/v2 and ipam/v1: the project is not there,
	// and this is the only shape of the two that says which identifier was not
	// found.
	projectAbsent = projectRefusal{}
	// projectDeniedToInstance is the only one that names the project itself.
	projectDeniedToInstance = projectRefusal{resource: "project", action: "read"}
	projectDeniedToLB       = projectRefusal{resource: "loadbalancer", action: "write"}
	projectDeniedToBlock    = projectRefusal{resource: "volume", action: "write"}
	projectDeniedToIAM      = projectRefusal{resource: "ssh_key", action: "create"}
)

// refuseUnknownProject answers a create whose project the register does not
// hold, and reports whether it wrote the refusal.
//
// Called before anything is stored, like every other refusal of this pack, for
// the reason createServer's own ordering names: a refusal written after the Put
// leaves a phantom behind.
//
// An empty project is never refused: it means the default, which always exists.
// That is what keeps a client configured from `feint env` clear of this refusal,
// and it is the first thing #391 said a fix would have to hold.
//
// TestACreateNamingAnUnknownProjectIsRefusedInItsProductsShape fails without
// this.
func (p *Pack) refuseUnknownProject(w http.ResponseWriter, project string, refusal projectRefusal) bool {
	if p.projectExists(project) {
		return false
	}
	if refusal.resource == "" {
		writeNotFound(w, "project", project)
		return true
	}
	// details is a slice of one element in every recording read here, and it is
	// a slice rather than an object because the SDK declares it as one
	// (scw/custom_errors.go, PermissionsDeniedError.Details).
	emulator.WriteJSON(w, http.StatusForbidden, map[string]any{
		"type":    "permissions_denied",
		"message": "insufficient permissions",
		"details": []any{map[string]any{"resource": refusal.resource, "action": refusal.action}},
	})
	return true
}
