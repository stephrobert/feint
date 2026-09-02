package scaleway_test

import (
	"net/http"
	"testing"
)

// The Account product's projects (#372).
//
// The wall these tests stand in front of is not a field: it is a 501 that made
// every third-party VPC stack unreachable, because `data
// "scaleway_account_project"` is evaluated before any resource in the graph.
// So the assertions are the ones the real client's own code makes, read off
// terraform-provider-scaleway's DataSourceAccountProjectRead: list by name,
// find the exact name, then get by the identifier that came back.

const projectsURL = "/account/v3/projects"

// The organization the emulated project belongs to, spelled here rather than
// imported: a test that read the pack's own constant would agree with it
// whatever it became, which is the assertion that proves nothing.
const emulatedOrganization = "99999999-9999-4999-8999-999999999999"

// The whole point of the issue, in one exchange: the door answers at all.
func TestListProjectsAnswersTheProjectEveryOtherAnswerIsScopedTo(t *testing.T) {
	ts := newTestServer(t)

	status, body := do(t, ts, "GET", projectsURL+"?organization_id="+emulatedOrganization, "")
	if status != http.StatusOK {
		t.Fatalf("list projects: expected 200, got %d (%v)", status, body)
	}
	projects, _ := body["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("expected the one emulated project, got %v", body)
	}
	got, _ := projects[0].(map[string]any)

	// Field for field against the SDK's Project struct: a client that decodes
	// into it must find every key, null included.
	for _, key := range []string{"id", "name", "organization_id", "created_at", "updated_at", "description", "qualification", "status"} {
		if _, present := got[key]; !present {
			t.Errorf("Project omits %s, which the SDK declares: %v", key, got)
		}
	}
	if got["name"] != "default" {
		t.Errorf("the initial project of an organization is named default upstream, this one is %v", got["name"])
	}
	if got["organization_id"] != emulatedOrganization {
		t.Errorf("organization_id is %v, not the emulated organization", got["organization_id"])
	}
	if got["status"] != "active" {
		t.Errorf("status is %v; a project answering reads is active", got["status"])
	}
	if total, _ := body["total_count"].(float64); int(total) != 1 {
		t.Errorf("total_count is %v, and the SDK's pagination loop reads it", body["total_count"])
	}
}

// organization_id is `required: true` on the document's own parameter, and a
// call without one is a call that names no account.
//
// The refusal is what the falsification removes: without it the handler answers
// 200 to a request the API refuses.
func TestListProjectsRefusesACallWithNoOrganization(t *testing.T) {
	ts := newTestServer(t)

	status, body := do(t, ts, "GET", projectsURL, "")
	if status != http.StatusBadRequest {
		t.Fatalf("list projects with no organization: expected 400, got %d (%v)", status, body)
	}
	if body["type"] != "invalid_arguments" {
		t.Errorf("the refusal is not the SDK's invalid_arguments shape: %v", body)
	}
}

// The organization is checked for presence and never for equality, which is the
// rule listSSHKeys already carries with its measurement: `scw` names its own
// configured organization on every list, and nothing obliges that configuration
// to spell this emulator's constant. Compared, the CLI is told the account it
// is looking at is empty.
func TestListProjectsAnswersWhateverOrganizationIsNamed(t *testing.T) {
	ts := newTestServer(t)

	status, body := do(t, ts, "GET", projectsURL+"?organization_id=8a3c1f00-0000-4000-8000-000000000000", "")
	if status != http.StatusOK {
		t.Fatalf("list projects: expected 200, got %d (%v)", status, body)
	}
	if projects, _ := body["projects"].([]any); len(projects) != 1 {
		t.Fatalf("a foreign organization id emptied the list: %v", body)
	}
}

// name and project_ids filter, and an empty list is the truthful answer to
// "which of your projects is called that": nothing here is.
func TestListProjectsFiltersRatherThanInvents(t *testing.T) {
	ts := newTestServer(t)

	cases := []struct {
		query string
		want  int
	}{
		{"&name=default", 1},
		{"&name=production", 0},
		{"&project_ids=11111111-1111-1111-1111-111111111111", 1},
		{"&project_ids=22222222-2222-4222-8222-222222222222", 0},
	}
	for _, c := range cases {
		status, body := do(t, ts, "GET", projectsURL+"?organization_id="+emulatedOrganization+c.query, "")
		if status != http.StatusOK {
			t.Fatalf("list%s: expected 200, got %d (%v)", c.query, status, body)
		}
		projects, _ := body["projects"].([]any)
		if len(projects) != c.want {
			t.Errorf("list%s: expected %d project(s), got %d", c.query, c.want, len(projects))
		}
	}
}

// order_by is declared by the document, so a value outside its enum is refused
// by name rather than served under some other order — the #277 rule, applied to
// a list this pack builds without a store behind it.
func TestListProjectsRefusesAnUnknownOrder(t *testing.T) {
	ts := newTestServer(t)

	status, body := do(t, ts, "GET", projectsURL+"?organization_id="+emulatedOrganization+"&order_by=price_asc", "")
	if status != http.StatusBadRequest {
		t.Fatalf("unknown order_by: expected 400, got %d (%v)", status, body)
	}
	status, body = do(t, ts, "GET", projectsURL+"?organization_id="+emulatedOrganization+"&order_by=name_desc", "")
	if status != http.StatusOK {
		t.Fatalf("declared order_by: expected 200, got %d (%v)", status, body)
	}
}

// The data source's second call, and the one #372's "done means" does not
// mention: DataSourceAccountProjectRead always ends on GetProject, on the id it
// resolved or on the configured one. A 404 here is a wall one call after the
// one this issue removed.
//
// The identifier used to be echoed rather than checked, and this test asserted
// that. #391 reversed it: fr-par answers 404 for a project it does not hold
// (measured 2026-09-02), and once the creates of four products refuse an unknown
// project, a GetProject that resolves one is worse than either answer alone —
// the client resolves with 200 and is refused the create under it, which is the
// disagreement #369 removed one product further in.
//
// The reason the echo existed did not stop being true, and it did not disappear:
// it moved to a declaration. TestADeclaredIdentifierIsTheOneAStackHolds is the
// stack carrying a production UUID, working.
//
// The `resource` field is "project_id" here and "project" on the delete. Two
// spellings of one word on two routes of one product, both measured, and rule 4
// says to copy them rather than pick one.
func TestGetProjectRefusesAnIdentifierNobodyDeclared(t *testing.T) {
	ts := newTestServer(t)

	const foreign = "6170692e-7363-616c-6577-61792e636f6d"
	status, body := do(t, ts, "GET", projectsURL+"/"+foreign, "")
	if status != http.StatusNotFound {
		t.Fatalf("get a project this register does not hold: expected 404, got %d (%v)", status, body)
	}
	if body["type"] != "not_found" {
		t.Errorf("error type is %v, want not_found", body["type"])
	}
	if body["resource"] != "project_id" {
		t.Errorf("resource is %v, want project_id — the delete says \"project\" and this route does not", body["resource"])
	}
	if body["resource_id"] != foreign {
		t.Errorf("resource_id is %v, want the identifier that was asked for", body["resource_id"])
	}
}

// The pair the real client walks, in the order it walks it.
func TestTheDataSourceSequenceResolvesEndToEnd(t *testing.T) {
	ts := newTestServer(t)

	status, listed := do(t, ts, "GET", projectsURL+"?organization_id="+emulatedOrganization+"&name=default", "")
	if status != http.StatusOK {
		t.Fatalf("list by name: expected 200, got %d (%v)", status, listed)
	}
	projects, _ := listed["projects"].([]any)
	if len(projects) != 1 {
		t.Fatalf("FindExact needs exactly one match, got %d", len(projects))
	}
	first, _ := projects[0].(map[string]any)
	id, _ := first["id"].(string)

	status, got := do(t, ts, "GET", projectsURL+"/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get the project the list named: expected 200, got %d (%v)", status, got)
	}
	if got["id"] != id || got["name"] != first["name"] {
		t.Errorf("the read disagrees with the list it followed: %v against %v", got, first)
	}
}
