package scaleway

import (
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

// projectView renders one Project, field for field as the SDK declares it
// (account/v3/account_sdk.go, type Project) and in the order the document's
// x-properties-order gives.
//
// id is a parameter rather than the constant because GetProject echoes what it
// was asked for; see the file comment.
func projectView(id string) map[string]any {
	return map[string]any{
		"id":              id,
		"name":            defaultProjectName,
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

	projects := []any{projectView(defaultProject)}
	if name := q.Get("name"); name != "" && name != defaultProjectName {
		projects = nil
	}
	if ids := csvValues(q, "project_ids"); len(ids) > 0 && !contains(ids, defaultProject) {
		projects = nil
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
	emulator.WriteJSON(w, http.StatusOK, projectView(id))
}
