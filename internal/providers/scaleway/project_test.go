package scaleway_test

import (
	"net/http"
	"testing"
)

// A project is an isolation boundary, not a label. A list that NAMES a project
// must answer that project alone, or a client reads resources it does not own
// and Terraform plans a destroy for something another project created.
//
// This test used to assert one thing more, and that thing was wrong: that an
// unfiltered list answers the default project only. It answers the whole
// account, measured (#369), and TestAnUnfilteredListAnswersEveryProjectLikeTheCloud
// now carries that half. What is left here is the half the cloud agrees with.
func TestListsAreScopedToTheProject(t *testing.T) {
	ts := newTestServer(t)

	second := projectMade(t, ts, "second")

	kinds := []struct {
		name   string
		path   string
		create string
		field  string
	}{
		{"servers", "/servers", `{"name":"scoped","commercial_type":"DEV1-S","project":"` + second + `"}`, "servers"},
		{"ips", "/ips", `{"project":"` + second + `"}`, "ips"},
		{"volumes", "/volumes", `{"name":"scoped","volume_type":"l_ssd","project":"` + second + `"}`, "volumes"},
		{"security_groups", "/security_groups", `{"name":"scoped","project":"` + second + `"}`, "security_groups"},
	}

	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			status, created := do(t, ts, "POST", zoneURL+k.path, k.create)
			if status != http.StatusCreated {
				t.Fatalf("create in %s: expected 201, got %d (%v)", second, status, created)
			}

			id, _ := firstID(created)
			if id == "" {
				t.Fatalf("no id in the create response: %v", created)
			}

			// A list naming the default project must not see it. It may
			// legitimately own resources of its own, so the assertion is on
			// this id, not on an empty list.
			_, listed := do(t, ts, "GET", zoneURL+k.path+"?project="+defaultProjectID, "")
			for _, item := range listed[k.field].([]any) {
				if m, _ := item.(map[string]any); m["id"] == id {
					t.Errorf("a list naming the default project sees %s of another project", id)
				}
			}

			// Its own project must.
			_, listed = do(t, ts, "GET", zoneURL+k.path+"?project="+second, "")
			found := false
			for _, item := range listed[k.field].([]any) {
				if m, _ := item.(map[string]any); m["id"] == id {
					found = true
				}
			}
			if !found {
				t.Errorf("the owning project does not see its own resource: %v", listed)
			}
		})
	}
}

// An organization filter alone spans every project: it is the account, one level
// above the projects it contains.
func TestOrganizationScopeSpansProjects(t *testing.T) {
	ts := newTestServer(t)

	if status, _ := do(t, ts, "POST", zoneURL+"/servers", `{"name":"a","commercial_type":"DEV1-S"}`); status != http.StatusCreated {
		t.Fatalf("create in the default project: got %d", status)
	}
	body := `{"name":"b","commercial_type":"DEV1-S","project":"` + projectMade(t, ts, "second") + `"}`
	if status, _ := do(t, ts, "POST", zoneURL+"/servers", body); status != http.StatusCreated {
		t.Fatalf("create in the other project: got %d", status)
	}

	_, listed := do(t, ts, "GET", zoneURL+"/servers?organization=99999999-9999-4999-8999-999999999999", "")
	servers, _ := listed["servers"].([]any)
	if len(servers) != 2 {
		t.Errorf("an organization-wide list returned %d servers, want 2: %v", len(servers), listed)
	}
}

// The organization is never the project. Reusing one identifier for both is the
// shortcut that hides the difference until a client talks to the real API.
func TestOrganizationIsNotTheProject(t *testing.T) {
	ts := newTestServer(t)

	_, server := serverWith(t, ts, `{"name":"ids","commercial_type":"DEV1-S"}`)
	if server["project"] == server["organization"] {
		t.Errorf("project and organization are the same value: %v", server["project"])
	}
	if server["organization"] == nil || server["organization"] == "" {
		t.Errorf("the server carries no organization: %v", server)
	}

	// A key is an IAM object: same rule, different field names.
	status, key := do(t, ts, "POST", "/iam/v1alpha1/ssh-keys",
		`{"name":"k","public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIOt7Knja0KTVDt1HPz09qrmbCjB8Zf8icc3p2eU9ubqy feint@test"}`)
	// 200, measured on the wire: see sshKeyCreateStatus.
	if status != http.StatusOK {
		t.Fatalf("create ssh key: expected 200, got %d (%v)", status, key)
	}
	if key["project_id"] == key["organization_id"] {
		t.Errorf("the key reuses one id for project and organization: %v", key["project_id"])
	}
}

// firstID digs the id out of a create response, whatever envelope the product
// uses: {"server":{...}}, {"ip":{...}}, {"security_group":{...}}.
func firstID(body map[string]any) (string, bool) {
	if id, ok := body["id"].(string); ok && id != "" {
		return id, true
	}
	for _, v := range body {
		if m, ok := v.(map[string]any); ok {
			if id, ok := m["id"].(string); ok && id != "" {
				return id, true
			}
		}
	}
	return "", false
}

// defaultProjectID is the project a create with no project lands in. Spelled out
// here rather than imported: this is the external test package, and the value is
// part of what a client sees.
const defaultProjectID = "11111111-1111-1111-1111-111111111111"

// An unfiltered list answers every project of the account, which is what the
// cloud answers and what this emulator used to get backwards.
//
// Measured on fr-par, 2026-09-02 (#369), with a second project made for the
// occasion and deleted after: a volume created in it came back from
// GET /volumes with no filter, stayed hidden from
// GET /volumes?project=<the default>, and came back again from
// GET /volumes?organization=<the org>.
//
// Why it matters beyond a filter: `createServer` honours the project a request
// names, so a server created under a named project was invisible to the very
// next unfiltered list. The create and the list disagreed, and a client that
// creates then lists — which is every client — read an empty fleet.
//
// The emulator has no credential, so "the caller's project" had to come from
// somewhere. The cloud settled which of the two coherent answers it takes: the
// create is authoritative. It accepted a create naming another project and then
// listed what it had made.
func TestAnUnfilteredListAnswersEveryProjectLikeTheCloud(t *testing.T) {
	ts := newTestServer(t)

	second := projectMade(t, ts, "second")
	status, created := do(t, ts, "POST", zoneURL+"/servers",
		`{"name":"elsewhere","commercial_type":"DEV1-S","project":"`+second+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("create in %s: expected 201, got %d (%v)", second, status, created)
	}
	id, _ := firstID(created)
	if id == "" {
		t.Fatalf("no id in the create response: %v", created)
	}

	seen := func(query string) bool {
		t.Helper()
		_, listed := do(t, ts, "GET", zoneURL+"/servers"+query, "")
		for _, item := range listed["servers"].([]any) {
			if m, _ := item.(map[string]any); m["id"] == id {
				return true
			}
		}
		return false
	}

	if !seen("") {
		t.Error("an unfiltered list hides a server the same emulator just created: " +
			"the create honoured the project and the list did not")
	}
	if seen("?project=" + defaultProjectID) {
		t.Error("a list naming the default project answers another project's server")
	}
	if !seen("?organization=" + defaultOrganizationID) {
		t.Error("an organization filter hides a project of that same organization")
	}
}

// defaultOrganizationID is the one organization this emulator hosts.
const defaultOrganizationID = "99999999-9999-4999-8999-999999999999"
