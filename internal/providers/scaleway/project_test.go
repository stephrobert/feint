package scaleway_test

import (
	"net/http"
	"testing"
)

// otherProject is a second project of the same organization. Any UUID that is
// not the default one does: what matters is that resources created under it
// stay invisible from the default project.
const otherProject = "22222222-2222-4222-8222-222222222222"

// A project is an isolation boundary, not a label. Every list must be scoped to
// the project the caller named, or a client reads resources it does not own and
// Terraform plans a destroy for something another project created.
func TestListsAreScopedToTheProject(t *testing.T) {
	ts := newTestServer(t)

	kinds := []struct {
		name   string
		path   string
		create string
		field  string
	}{
		{"servers", "/servers", `{"name":"scoped","commercial_type":"DEV1-S","project":"` + otherProject + `"}`, "servers"},
		{"ips", "/ips", `{"project":"` + otherProject + `"}`, "ips"},
		{"volumes", "/volumes", `{"name":"scoped","volume_type":"l_ssd","project":"` + otherProject + `"}`, "volumes"},
		{"security_groups", "/security_groups", `{"name":"scoped","project":"` + otherProject + `"}`, "security_groups"},
	}

	for _, k := range kinds {
		t.Run(k.name, func(t *testing.T) {
			status, created := do(t, ts, "POST", zoneURL+k.path, k.create)
			if status != http.StatusCreated {
				t.Fatalf("create in %s: expected 201, got %d (%v)", otherProject, status, created)
			}

			id, _ := firstID(created)
			if id == "" {
				t.Fatalf("no id in the create response: %v", created)
			}

			// The default project must not see it. It may legitimately own
			// resources of its own, so the assertion is on this id, not on an
			// empty list.
			_, listed := do(t, ts, "GET", zoneURL+k.path, "")
			for _, item := range listed[k.field].([]any) {
				if m, _ := item.(map[string]any); m["id"] == id {
					t.Errorf("the default project sees %s of another project", id)
				}
			}

			// Its own project must.
			_, listed = do(t, ts, "GET", zoneURL+k.path+"?project="+otherProject, "")
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
	body := `{"name":"b","commercial_type":"DEV1-S","project":"` + otherProject + `"}`
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
