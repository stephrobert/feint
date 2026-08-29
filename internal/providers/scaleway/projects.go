package scaleway

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"

	"github.com/stephrobert/feint/internal/core/emulator"
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
//   - `GetProject` RESOLVES, and a 404 there is a wall rather than an answer.
//     It echoes whatever identifier is asked for, which is docs/limits.md's
//     "Identifiers are not checked against anything" rule — the same decision
//     getImage takes for an image UUID the catalogue never minted, and taken
//     for the same reason: a stack configured with a real project id must not
//     die on the one thing that has nothing to do with what it is testing.
//
// The asymmetry is deliberate and is the whole design of this file.

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
// and it is truthful about this emulator too: DeleteProject is declined, so the
// project cannot be deleted here either.
const defaultProjectDescription = "cannot_be_deleted"

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
func (p *Pack) declaredProjects() []string {
	if len(p.env.Projects) == 0 {
		return []string{defaultProjectName}
	}
	return p.env.Projects
}

// projectNamed answers the declared name carrying that identifier, and whether
// the catalogue holds it at all.
func (p *Pack) projectNamed(id string) (string, bool) {
	for _, name := range p.declaredProjects() {
		if projectID(name) == id {
			return name, true
		}
	}
	return "", false
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

	// The two filters still FILTER, over the declared catalogue rather than over
	// a constant. An empty answer remains the truthful one for a name this
	// account does not hold — the whole point of #572 is that the operator can
	// make it hold that name, not that the emulator invents it.
	wantName := q.Get("name")
	wantIDs := csvValues(q, "project_ids")
	projects := []any{}
	for _, declared := range p.declaredProjects() {
		id := projectID(declared)
		if wantName != "" && wantName != declared {
			continue
		}
		if len(wantIDs) > 0 && !contains(wantIDs, id) {
			continue
		}
		projects = append(projects, projectView(id, declared))
	}

	page := parsePage(r)
	start, end := page.slice(len(projects))
	emulator.WriteJSON(w, http.StatusOK, map[string]any{
		"total_count": len(projects),
		"projects":    append([]any{}, projects[start:end]...),
	})
}

// getProject resolves one project by identifier, and answers for any of them.
//
// The echo is the point, not a shortcut: docs/limits.md's "Identifiers are not
// checked against anything" is what keeps a configuration holding a production
// project UUID working against this emulator, and a project id is the identifier
// a stack is most likely to carry — projectOf files every resource under
// whatever the request named, so the id asked for here is a live isolation
// boundary in this store whether or not anything was created in it yet.
//
// TestGetProjectAnswersAnIdentifierItNeverMinted fails without this.
func (p *Pack) getProject(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if id == "" {
		writeNotFound(w, "project", id)
		return
	}
	// A declared project answers with its own name; anything else still echoes,
	// under the first declared name. The echo is unchanged and deliberate — see
	// the file comment — but it stopped being able to answer `default` for
	// everything the moment the catalogue could hold more than one, and a
	// project the operator DID declare must read back as itself or the data
	// source resolves a name nobody asked for.
	//
	// TestGetProjectAnswersADeclaredProjectByItsOwnName fails without this.
	if name, ok := p.projectNamed(id); ok {
		emulator.WriteJSON(w, http.StatusOK, projectView(id, name))
		return
	}
	emulator.WriteJSON(w, http.StatusOK, projectView(id, p.declaredProjects()[0]))
}
