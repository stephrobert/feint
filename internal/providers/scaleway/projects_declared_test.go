package scaleway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// The declared project catalogue (#572).
//
// Until 2026-08-29 this pack held one project named `default`, and a stack whose
// `project_name` was its own production project died on the provider's FindExact
// after a truthful empty list — the obstacle for exactly the person #372 exists
// to serve. The operator declares the catalogue now, and the list still filters:
// a name nobody declared answers nothing, because answering that a project
// exists because somebody asked for it is the class #83 measured and closed on
// all three packs.

func TestListProjectsAnswersTheOnesTheOperatorDeclared(t *testing.T) {
	srv := serveWithProjects(t, "default", "platform-prod")

	all := projectsOf(t, srv, "")
	if len(all) != 2 {
		t.Fatalf("%d project(s) listed, want the two declared: %v", len(all), all)
	}
	if all[0]["name"] != "default" || all[1]["name"] != "platform-prod" {
		t.Errorf("listed %v, %v; declaration order is what a reader compares against",
			all[0]["name"], all[1]["name"])
	}

	// The filter the provider's data source uses, on the name that motivated
	// this issue.
	named := projectsOf(t, srv, "&name=platform-prod")
	if len(named) != 1 || named[0]["name"] != "platform-prod" {
		t.Fatalf("filtering by a declared name answered %v", named)
	}

	// And the half that must not become an echo: a name nobody declared is an
	// empty list, not an invented project.
	if got := projectsOf(t, srv, "&name=never-declared"); len(got) != 0 {
		t.Errorf("a name nobody declared answered %v; the emulator invented a project", got)
	}
}

// An operator who declared nothing gets exactly what was there before: one
// project called `default`, under the identifier every other product of this
// pack already scopes its answers to. A feature that changed the default answer
// would have rewritten every existing declaration's account.
func TestAnUndeclaredCatalogueIsStillTheSingleDefaultProject(t *testing.T) {
	srv := serveWithProjects(t)

	all := projectsOf(t, srv, "")
	if len(all) != 1 {
		t.Fatalf("%d project(s) with nothing declared, want 1: %v", len(all), all)
	}
	if all[0]["name"] != "default" {
		t.Errorf("the undeclared catalogue is named %v, want default", all[0]["name"])
	}
	if all[0]["id"] != "11111111-1111-1111-1111-111111111111" {
		t.Errorf("the default project's id moved to %v; every other product of this pack scopes to it",
			all[0]["id"])
	}
}

// A declared project reads back as itself. The echo GetProject performs for an
// unknown identifier is deliberate and unchanged (see projects.go), but it
// stopped being able to answer `default` for everything the moment the catalogue
// could hold more than one: the data source resolves a name through ListProjects
// and then reads it back here, so a declared project answering somebody else's
// name would hand the stack a project it never asked for.
func TestGetProjectAnswersADeclaredProjectByItsOwnName(t *testing.T) {
	srv := serveWithProjects(t, "default", "platform-prod")

	declared := projectsOf(t, srv, "&name=platform-prod")
	if len(declared) != 1 {
		t.Fatalf("the declared project did not list: %v", declared)
	}
	id, _ := declared[0]["id"].(string)

	got := getProject(t, srv, id)
	if got["name"] != "platform-prod" {
		t.Errorf("GetProject on the declared project answered name %v, want platform-prod", got["name"])
	}
	if got["id"] != id {
		t.Errorf("GetProject answered id %v for %s", got["id"], id)
	}

	// The echo, unchanged: an identifier this catalogue never held still
	// resolves, because a configuration carrying a production project UUID must
	// not die on the one thing that has nothing to do with what it is testing.
	foreign := "abcdef01-2345-4678-9abc-def012345678"
	echoed := getProject(t, srv, foreign)
	if echoed["id"] != foreign {
		t.Errorf("GetProject answered id %v for %s; the identifier a client sent is the one it must read back",
			echoed["id"], foreign)
	}
}

// The identifiers are derived from the name and never minted, for the reason
// projectEpoch is fixed: an id that moved between two reads is a permanent
// Terraform diff. Two independent servers of the same declaration must agree.
func TestADeclaredProjectKeepsItsIdentifierAcrossProcesses(t *testing.T) {
	first := projectsOf(t, serveWithProjects(t, "default", "platform-prod"), "&name=platform-prod")
	second := projectsOf(t, serveWithProjects(t, "default", "platform-prod"), "&name=platform-prod")
	if len(first) != 1 || len(second) != 1 {
		t.Fatalf("the declared project did not list twice: %v / %v", first, second)
	}
	if first[0]["id"] != second[0]["id"] {
		t.Errorf("two servers of the same declaration answered %v and %v: a stack storing state "+
			"between applies would see a permanent diff", first[0]["id"], second[0]["id"])
	}
	// And it is shaped like the UUIDs the SDK parses, which is what a client
	// requires before it reaches anything this emulator serves.
	id, _ := first[0]["id"].(string)
	if len(id) != 36 || strings.Count(id, "-") != 4 || id[14] != '4' {
		t.Errorf("the derived identifier %q is not a v4-shaped UUID", id)
	}
}

// ---- the harness -----------------------------------------------------------

// serveWithProjects is newTestServer with a declared catalogue. Written here
// rather than as a parameter on newTestServer because every other test in this
// package asserts the UNDECLARED account, and a shared helper that took the
// list would invite them to drift into declaring one.
func serveWithProjects(t *testing.T, names ...string) *httptest.Server {
	t.Helper()

	var seq int
	env := &emulator.Env{
		Store: store.New(),
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			seq++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
		},
		Projects: names,
	}
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

func projectsOf(t *testing.T, srv *httptest.Server, filter string) []map[string]any {
	t.Helper()
	res, err := http.Get(srv.URL + "/account/v3/projects?organization_id=" +
		"99999999-9999-4999-8999-999999999999" + filter)
	if err != nil {
		t.Fatalf("list projects: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("list projects answered %d", res.StatusCode)
	}
	var body struct {
		TotalCount int              `json:"total_count"`
		Projects   []map[string]any `json:"projects"`
	}
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.TotalCount != len(body.Projects) {
		t.Errorf("total_count is %d and %d project(s) came back", body.TotalCount, len(body.Projects))
	}
	return body.Projects
}

func getProject(t *testing.T, srv *httptest.Server, id string) map[string]any {
	t.Helper()
	res, err := http.Get(srv.URL + "/account/v3/projects/" + id)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("get project answered %d", res.StatusCode)
	}
	var body map[string]any
	if err := json.NewDecoder(res.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}
