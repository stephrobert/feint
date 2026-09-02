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

	// The echo is gone (#391): an identifier this register does not hold is
	// refused, the way fr-par refuses it. The reason the echo existed has not
	// stopped being true, and the test below is where it went — the operator
	// declares the identifier their stack carries.
	foreign := "abcdef01-2345-4678-9abc-def012345678"
	if status := getProjectStatus(t, srv, foreign); status != http.StatusNotFound {
		t.Errorf("GetProject answered %d for an identifier nobody declared, want 404", status)
	}
}

// The way out the echo used to be: an operator whose stack carries a production
// project identifier declares it, and the register holds that identifier rather
// than one derived from a name.
//
// This is what keeps #372's case alive after #391 refused unknown projects. The
// two stacks #372 measured die on GET /account/v3/projects/{id} before they
// reach a single VPC path, and a 404 there would have put them back where they
// were. `--projects platform-prod=<their uuid>` is the door, and it is the
// operator's own statement rather than an echo the emulator invents — which is
// the distinction #83 closed on all three packs.
func TestADeclaredIdentifierIsTheOneAStackHolds(t *testing.T) {
	const held = "abcdef01-2345-4678-9abc-def012345678"

	var seq int
	env := &emulator.Env{
		Store: store.New(),
		Now:   func() time.Time { return time.Unix(1700000000, 0).UTC() },
		NewID: func() string {
			seq++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
		},
		Projects: []emulator.Project{{Name: "platform-prod", ID: held}},
	}
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	got := getProject(t, ts, held)
	if got["id"] != held {
		t.Errorf("GetProject answered id %v, want the declared %s", got["id"], held)
	}
	if got["name"] != "platform-prod" {
		t.Errorf("GetProject answered name %v, want platform-prod", got["name"])
	}

	// And the creates accept it, which is the half a data source alone would not
	// prove: resolving a project a create then refuses is the disagreement #369
	// removed one product out.
	res, err := http.Post(ts.URL+"/instance/v1/zones/fr-par-1/servers", "application/json",
		strings.NewReader(`{"name":"held","commercial_type":"DEV1-S","project":"`+held+`"}`))
	if err != nil {
		t.Fatalf("create under the declared identifier: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusCreated {
		t.Errorf("a create under the declared identifier answered %d, want 201", res.StatusCode)
	}
}

// getProjectStatus is getProject without the 200 it insists on.
func getProjectStatus(t *testing.T, srv *httptest.Server, id string) int {
	t.Helper()
	res, err := http.Get(srv.URL + "/account/v3/projects/" + id)
	if err != nil {
		t.Fatalf("get project: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	return res.StatusCode
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
		Projects: declaredFrom(names),
	}
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// declaredFrom turns the names a test writes into the declaration the Env now
// carries. Names alone, no identifiers: a test that wants to state one writes
// emulator.Project itself, which is what TestADeclaredIdentifierIsTheOneAStackHolds
// does.
func declaredFrom(names []string) []emulator.Project {
	out := make([]emulator.Project, 0, len(names))
	for _, name := range names {
		out = append(out, emulator.Project{Name: name})
	}
	return out
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
