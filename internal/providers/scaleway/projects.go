package scaleway

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/resource"
)

// The Account product's projects, and why a pack about servers serves them.
//
// A project is not a resource a stack creates: it is the thing every other
// resource is filed under, so a client resolves it BEFORE it creates anything.
// The Terraform provider's `data "scaleway_account_project"` is evaluated ahead
// of every resource in the graph, and #372 measured what that costs: two
// independent third-party stacks that walk the VPC surface —
// tf-scaleway-modules/terraform-scaleway-network and srdp-hub/srdp — die on
// `GET /account/v3/projects` with 501 before they reach a single VPC path. The
// whole recording of the first one is two exchanges, both that.
//
// It is the catalogue trap of catalog.go, one product out: the emulator owns no
// account, and it cannot decline the endpoint that describes one either.
//
// # What a client actually calls, measured rather than assumed
//
// The provider's DataSourceAccountProjectRead (terraform-provider-scaleway,
// internal/services/account/project_data_source.go, read 2026-08-27) calls
// ListProjects only when the configuration names the project by `name`, and
// then filters the answer with FindExact. It ALWAYS calls GetProject afterwards
// — on the id it just resolved, or on the configured project id when no name is
// given. So serving ListProjects alone leaves the data source failing one call
// later, which is why both are mounted. #372's "done means" names only
// ListProjects; the provider source says otherwise, and the source is what runs.
//
// # One organization, one project, and the two rules that follow
//
// The emulator hosts a single organization (projectOf: "the organization is not
// taken from the request") and a single named project, the one every unfiltered
// answer of every product is already scoped to (scopeOf). So the list is one
// element long, and the two filters over it are NOT treated the same way:
//
//   - `name` and `project_ids` FILTER. An empty list is a truthful answer to
//     "which of your projects is called X": nothing here is called X.
//   - `GetProject` RESOLVED, and echoed whatever identifier it was asked for.
//     That is over: it answers 404 for a project the register does not hold, the
//     way fr-par does, and the paragraph below says what replaced the reason.
//
// # Why the echo went, and what took its place (#391)
//
// The echo was written for a measured reason: a stack configured with a real
// project id must not die on the one thing that has nothing to do with what it
// is testing. That reason has not stopped being true. What changed is that it
// stopped being reachable from where it stood.
//
// Measured on fr-par, 2026-09-02, with a project made for the occasion and
// deleted after:
//
//	POST   /account/v3/projects              200, and two projects may share a name
//	PATCH  /account/v3/projects/{id}         200, updated_at moves
//	GET    /account/v3/projects/{unknown}    404 not_found, resource "project_id"
//	DELETE /account/v3/projects/{unknown}    404 not_found, resource "project"
//	DELETE a project that still holds a disk 412 precondition_failed,
//	                                         precondition "resource_still_in_use"
//	DELETE it once empty                     204
//
// And the creates of four products refuse a project nobody holds, in three
// different dialects (projectguard.go). Once a create refuses, an echoing
// GetProject is worse than either answer on its own: a client resolves a project
// with 200 and is then refused the create under it, which is the disagreement
// #369 had between its own create and list, one product further out.
//
// So the register holds two kinds of project and answers both the same way:
// what the operator declared (`--projects`, now `name` or `name=<id>`), and what
// a client created through CreateProject. The stack holding a production UUID
// declares it — `--projects prod=<uuid>` — which is a statement the operator
// makes rather than an echo the emulator invents, and that distinction is the
// one #83 closed on all three packs.

// projectEpoch is when the emulated project was "created". Fixed, like
// imageEpoch and for the same reason: a date that moved between two reads is a
// permanent Terraform diff, and the data source stores created_at and
// updated_at (setProjectState, provider internal/services/account/project.go).
const projectEpoch = "2025-01-01T00:00:00Z"

// defaultProjectName is what the initial project of an organization is called.
//
// Read off Scaleway's own published document rather than guessed: the portal's
// account/project/v3 schema (downloaded by upstream:sync, the ListProjects
// description) carries a worked response where the organization's first project
// is `{"name": "default", "description": "cannot_be_deleted"}`, and its Projects
// tag states "Every Scaleway Organization has a default Project". Both values
// below come from that example, which is the only measurement available here:
// no recording in corpus/ carries an account/v3 exchange.
const defaultProjectName = "default"

// defaultProjectDescription is the same example's description for that project,
// and it stays truthful about this emulator for a second reason since #391:
// DeleteProject is served now, and it refuses the declared projects — an
// operator's declaration is not a client's to delete.
const defaultProjectDescription = "cannot_be_deleted"

// kindProject is a project a client created, as opposed to one the operator
// declared. Both are in the register; only these are in the store.
const kindProject = "project"

// createdProjectDescription is what a project this emulator minted carries. The
// cloud answers "" for a create that named no description, measured 2026-09-02.
const createdProjectDescription = ""

// # The declared catalogue (#572)
//
// Until 2026-08-29 this file held exactly one project, named `default`, and a
// stack whose `project_name` was anything else died on the provider's FindExact
// after a truthful empty list. That is a real obstacle for the case #372 exists
// to serve — a platform team pointing an existing stack at the emulator is
// precisely the person whose project name is their own.
//
// The operator now declares them (`feint serve --projects`, `cloud.projects` in
// feint.yaml). The list still FILTERS: a name nobody declared answers an empty
// list, because the alternative — answer that a project exists because somebody
// asked for it — is the class #83 measured and closed on all three packs.
//
// Identifiers are derived from the name, never minted per run, for the reason
// projectEpoch is fixed: an id that moved between two reads is a permanent
// Terraform diff. `default` keeps the constant every other product of this pack
// already scopes its answers to (defaultProject in servers.go), so a declaration
// that names it changes nothing about what was there before.

// projectID answers the identifier of a declared project.
//
// `default` is the constant, because every unfiltered answer of every product in
// this pack is already scoped to it (scopeOf) and moving it would renumber the
// whole emulated account. Every other name is hashed into a v4-shaped UUID,
// deterministic across restarts and across processes: two `feint serve` runs of
// the same declaration answer the same identifiers, which is what a stack that
// stores state between applies requires.
func projectID(name string) string {
	if name == defaultProjectName {
		return defaultProject
	}
	sum := sha256.Sum256([]byte("feint/scaleway/project/" + name))
	hexed := hex.EncodeToString(sum[:16])
	// Shaped like the UUIDs Scaleway mints — version 4, variant 10 — because
	// clients parse them. A 32-character hex string that is not a UUID is
	// refused by the SDK before it reaches anything this emulator serves.
	return fmt.Sprintf("%s-%s-4%s-%x%s-%s",
		hexed[0:8], hexed[8:12], hexed[13:16],
		(hexed[16]&0x3)|0x8, hexed[17:20], hexed[20:32])
}

// declaredProjects answers the catalogue this emulator holds, in declaration
// order.
//
// An operator who declared nothing gets the single default project, which is
// what every existing feint.yaml, every existing test and every existing
// recording expects. The fallback is here rather than at the parse site so that
// a pack reading env.Projects never has to ask whether the empty case means
// "none" or "the default" — it means the default, once, in this function.
func (p *Pack) declaredProjects() []emulator.Project {
	if len(p.env.Projects) == 0 {
		return []emulator.Project{{Name: defaultProjectName}}
	}
	return p.env.Projects
}

// identifierOf answers the identifier a declared project carries: the one the
// operator stated, or the one derived from its name.
func identifierOf(declared emulator.Project) string {
	if declared.ID != "" {
		return declared.ID
	}
	return projectID(declared.Name)
}

// projectNamed answers the declared name carrying that identifier, and whether
// the catalogue holds it at all.
func (p *Pack) projectNamed(id string) (string, bool) {
	for _, declared := range p.declaredProjects() {
		if identifierOf(declared) == id {
			return declared.Name, true
		}
	}
	return "", false
}

// projectExists is the one question every other product of this pack asks about
// a project, and the reason this file has a register at all.
//
// Two sources, one answer: what the operator declared, and what a client created
// here. An empty identifier is the default project, which always exists — that
// is what keeps a client configured from `feint env` clear of the refusal, which
// is the first thing #391 said a fix would have to hold.
func (p *Pack) projectExists(id string) bool {
	if id == "" {
		return true
	}
	if _, ok := p.projectNamed(id); ok {
		return true
	}
	_, found := p.env.Store.Get(Name, kindProject, id)
	return found
}

// projectRecord answers the view of a project the register holds, declared or
// created, and whether it holds it.
func (p *Pack) projectRecord(id string) (map[string]any, bool) {
	if name, ok := p.projectNamed(id); ok {
		return projectView(id, name), true
	}
	if res, found := p.env.Store.Get(Name, kindProject, id); found {
		return p.createdProjectView(res), true
	}
	return nil, false
}

// createdProjectView renders a project this emulator minted. Same field order as
// projectView, and the two dates come from the resource rather than from the
// epoch: a client that creates a project and reads it back must see the moment
// it asked for, and a rename has to move updated_at (measured).
func (p *Pack) createdProjectView(res *resource.Resource) map[string]any {
	name, _ := res.Attrs["name"].(string)
	description, _ := res.Attrs["description"].(string)
	return map[string]any{
		"id":              res.ID,
		"name":            name,
		"organization_id": defaultOrganization,
		"created_at":      res.Created.UTC().Format(time.RFC3339Nano),
		"updated_at":      res.Updated.UTC().Format(time.RFC3339Nano),
		"description":     description,
		"qualification":   nil,
		"status":          "active",
	}
}

// registeredProjects answers every project the account holds, declared first and
// then created, in the order each was stated or minted.
func (p *Pack) registeredProjects() []map[string]any {
	out := make([]map[string]any, 0, len(p.declaredProjects()))
	for _, declared := range p.declaredProjects() {
		out = append(out, projectView(identifierOf(declared), declared.Name))
	}
	for _, res := range p.env.Store.List(kindProject, resource.Tenant{Provider: Name}) {
		out = append(out, p.createdProjectView(res))
	}
	return out
}

// projectView renders one Project, field for field as the SDK declares it
// (account/v3/account_sdk.go, type Project) and in the order the document's
// x-properties-order gives.
//
// id and name are parameters rather than constants: id because GetProject
// echoes what it was asked for (see the file comment), name because the
// catalogue has held more than one since #572.
func projectView(id, name string) map[string]any {
	return map[string]any{
		"id":              id,
		"name":            name,
		"organization_id": defaultOrganization,
		"created_at":      projectEpoch,
		"updated_at":      projectEpoch,
		"description":     defaultProjectDescription,
		// Qualification is *Qualification in the SDK — a pointer, and nothing
		// here has ever asked the account what it is used for. Null is the
		// answer for a project whose qualification was never set.
		"qualification": nil,
		// ProjectStatus is a value in the SDK, and the document's enum is
		// unknown_status / active / deleting. A project that answers reads is
		// active; deleting is a state nothing here can enter.
		"status": "active",
	}
}

// listProjects answers the organization's projects.
//
// organization_id is required by the document (`required: true` on the query
// parameter) and is checked for PRESENCE only, never for equality. The equality
// is the mistake listSSHKeys already made and measured: `scw` names its
// configured organization on every list, nothing obliges that configuration to
// spell this emulator's constant, and comparing told the CLI that the key it
// had just created did not exist. projectOf carries the same rule for creates.
//
// TestListProjectsRefusesACallWithNoOrganization and
// TestListProjectsAnswersWhateverOrganizationIsNamed fail without this.
func (p *Pack) listProjects(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	if q.Get("organization_id") == "" {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "organization_id",
			Reason:       "required",
			HelpMessage:  "the Account API lists the projects of one organization",
		})
		return
	}

	// The document's enum is created_at_{asc,desc} and name_{asc,desc}, default
	// created_at_asc. Validated and then not applied, which is honest for a
	// one-element list and is the reason the check is here at all: a value
	// outside the enum is refused by name the way every other list of this pack
	// refuses one (orderAsked), rather than silently answering some other order.
	if _, _, ok := orderAsked(w, r, "order_by", "created_at_asc", func(field string) bool {
		return field == "created_at" || field == "name"
	}); !ok {
		return
	}

	// The two filters still FILTER, over the register rather than over a
	// constant. An empty answer remains the truthful one for a name this account
	// does not hold — the point of #572 is that the operator can make it hold
	// that name, and the point of #391 is that a client can too, by creating it.
	wantName := q.Get("name")
	wantIDs := csvValues(q, "project_ids")
	projects := []any{}
	for _, view := range p.registeredProjects() {
		id, _ := view["id"].(string)
		name, _ := view["name"].(string)
		if wantName != "" && wantName != name {
			continue
		}
		if len(wantIDs) > 0 && !contains(wantIDs, id) {
			continue
		}
		projects = append(projects, view)
	}

	page := parsePage(r)
	start, end := page.slice(len(projects))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"total_count": len(projects),
		"projects":    append([]any{}, projects[start:end]...),
	})
}

// getProject resolves one project by identifier, and refuses one the register
// does not hold.
//
// It used to echo any identifier, and the file comment carries why that went.
// The refusal's own shape is measured rather than borrowed from the sibling
// below: fr-par answers `resource: "project_id"` here and `resource: "project"`
// on the delete, 2026-09-02. Two spellings of the same word on two routes of one
// product is exactly the kind of thing rule 4 exists for — nobody would invent
// it, and a client keying on it would be told the wrong field.
//
// TestGetProjectRefusesAnIdentifierNobodyDeclared and
// TestGetProjectAnswersADeclaredProjectByItsOwnName fail without this.
func (p *Pack) getProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if view, ok := p.projectRecord(id); ok {
		emulator.WriteJSON(w, http.StatusOK, view)
		return
	}
	writeNotFound(w, "project_id", id)
}

// createProjectRequest is CreateProjectRequest, cut to what this pack honours.
// OrganizationID is read and not compared, for the reason listProjects gives
// about organization_id: a client names its own configured organization, and
// comparing told `scw iam ssh-key list` that a key it had just made was gone.
type createProjectRequest struct {
	Name           string `json:"name"`
	Description    string `json:"description"`
	OrganizationID string `json:"organization_id"`
}

// createProject mints a project a client can then file resources under.
//
// 200, not 201: measured on fr-par 2026-09-02, and it is the status the SDK's
// generated method expects. Two projects may carry the same name — the same
// measurement made one twice and got two identifiers — so there is no uniqueness
// check here, and inventing one would refuse a request the cloud accepts.
//
// TestCreateProjectMintsAProjectTheCreatesThenAccept fails without this.
func (p *Pack) createProject(w http.ResponseWriter, r *http.Request) {
	var req createProjectRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	if req.Name == "" {
		writeInvalidArguments(w, ArgumentError{
			ArgumentName: "name",
			Reason:       "required",
			HelpMessage:  "required key not provided",
		})
		return
	}
	now := p.env.Now()
	res := resource.New(p.env.NewID(), kindProject, resource.Tenant{Provider: Name}, "active", now)
	res.Attrs = map[string]any{
		"name":        req.Name,
		"description": orDefault(req.Description, createdProjectDescription),
	}
	p.env.Store.Put(res)
	emulator.WriteJSON(w, http.StatusOK, p.createdProjectView(res))
}

// updateProjectRequest is UpdateProjectRequest. Both fields are pointers: the
// SDK sends only what the client asked to change, and an absent name is not a
// name set to empty.
type updateProjectRequest struct {
	Name        *string `json:"name"`
	Description *string `json:"description"`
}

// updateProject renames a project, or restates its description.
//
// A declared project is refused rather than renamed, and the refusal is the
// register's own: an operator's declaration is a statement made outside the API,
// and a client renaming it would leave `--projects` describing something that no
// longer answers. The status is the one the cloud gives a project it does not
// hold, because from the API's side a declared project is not a record.
//
// TestUpdateProjectRenamesACreatedProjectAndRefusesADeclaredOne fails without
// this.
func (p *Pack) updateProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	res, found := p.env.Store.Get(Name, kindProject, id)
	if !found {
		writeNotFound(w, "project_id", id)
		return
	}
	var req updateProjectRequest
	if err := emulator.DecodeJSON(r, &req); err != nil {
		writeInvalidArguments(w, ArgumentError{ArgumentName: "body", Reason: "format", HelpMessage: err.Error()})
		return
	}
	clone := res.Clone()
	if req.Name != nil {
		clone.Attrs["name"] = *req.Name
	}
	if req.Description != nil {
		clone.Attrs["description"] = *req.Description
	}
	clone.Updated = p.env.Now()
	p.env.Store.Commit(res, clone, p.env.Now())
	emulator.WriteJSON(w, http.StatusOK, p.createdProjectView(clone))
}

// deleteProject removes a project nothing is filed under.
//
// The 412 is the measurement this route exists for. fr-par refuses a project
// that still holds a disk with precondition_failed / resource_still_in_use, and
// a client that reads a 204 and moves on would leave resources behind believing
// they went with it. Answering 204 unconditionally is the plausible-wrong answer
// this repository exists to avoid.
//
// TestDeleteProjectRefusesOneThatStillHoldsSomething fails without this.
func (p *Pack) deleteProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if _, declared := p.projectNamed(id); declared {
		// The operator's declaration outlives a client, the way the account's
		// own `default` does upstream: its description is literally
		// "cannot_be_deleted".
		writeProjectStillInUse(w)
		return
	}
	res, found := p.env.Store.Get(Name, kindProject, id)
	if !found {
		writeNotFound(w, "project", id)
		return
	}
	if held := p.resourcesUnder(id); held > 0 {
		writeProjectStillInUse(w)
		return
	}
	p.env.Store.Delete(Name, kindProject, res.ID)
	w.WriteHeader(http.StatusNoContent)
}

// resourcesUnder counts what is filed under a project, across every kind this
// pack stores. Written as a sweep of the store rather than a list of kinds:
// a list would be a second inventory to keep, and the kind that gets forgotten
// is the one that makes a delete answer 204 over a resource still standing.
func (p *Pack) resourcesUnder(project string) int {
	held := 0
	for _, res := range p.env.Store.All() {
		if res.Tenant.Provider != Name || res.Kind == kindProject {
			continue
		}
		if res.Tenant.Project == project {
			held++
		}
	}
	return held
}
