package scaleway_test

import (
	"fmt"
	"net/http"
	"testing"
)

// The lifecycle a client walks, in the order it walks it, and every status is
// one fr-par answered on 2026-09-02 (#391).
//
// The register exists because the creates refuse a project nobody holds. Refusing
// without a way to make one real would have left the multi-project behaviour
// this emulator was built with no door at all, so the door is the one the cloud
// has: CreateProject.
func TestCreateProjectMintsAProjectTheCreatesThenAccept(t *testing.T) {
	ts := newTestServer(t)
	const ghost = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

	// Before: the create is refused, in instance/v1's shape.
	status, refused := do(t, ts, "POST", zoneURL+"/servers",
		`{"name":"early","commercial_type":"DEV1-S","project":"`+ghost+`"}`)
	if status != http.StatusForbidden {
		t.Fatalf("a create naming a project nobody holds answered %d, want 403 (%v)", status, refused)
	}

	// 200 and not 201, measured. The SDK's generated method expects it, and a
	// 201 here would send a client down its created-elsewhere path.
	status, created := do(t, ts, "POST", "/account/v3/projects",
		`{"name":"platform","organization_id":"`+defaultOrganizationID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("create project: expected 200, got %d (%v)", status, created)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("no id in %v", created)
	}
	if created["status"] != "active" {
		t.Errorf("status is %v, want active", created["status"])
	}

	// After: the same create is accepted, and the resource is filed under it.
	status, made := do(t, ts, "POST", zoneURL+"/servers",
		`{"name":"late","commercial_type":"DEV1-S","project":"`+id+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("a create under a project just made answered %d (%v)", status, made)
	}
	server, _ := made["server"].(map[string]any)
	if server["project"] != id {
		t.Errorf("the server was filed under %v, not the project it named", server["project"])
	}

	// And it reads back, which is the half a create alone does not prove: a
	// GetProject that refused what CreateProject had just minted is the
	// disagreement #369 removed one product out.
	status, read := do(t, ts, "GET", "/account/v3/projects/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("GetProject on a project just made answered %d (%v)", status, read)
	}
	if read["name"] != "platform" {
		t.Errorf("GetProject answered name %v, want platform", read["name"])
	}

	// Two projects may carry one name, measured: the same request twice on
	// fr-par answered 200 twice with two identifiers. Refusing the second would
	// refuse what the cloud accepts.
	status, twin := do(t, ts, "POST", "/account/v3/projects",
		`{"name":"platform","organization_id":"`+defaultOrganizationID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("the same name a second time answered %d (%v)", status, twin)
	}
	if twin["id"] == id {
		t.Errorf("the second project carries the first one's identifier %v", twin["id"])
	}
}

// A rename moves updated_at, and an operator's declaration is not a client's to
// rename.
func TestUpdateProjectRenamesACreatedProjectAndRefusesADeclaredOne(t *testing.T) {
	ts := newTestServer(t)
	id := projectMade(t, ts, "before")

	status, renamed := do(t, ts, "PATCH", "/account/v3/projects/"+id, `{"name":"after"}`)
	if status != http.StatusOK {
		t.Fatalf("rename: expected 200, got %d (%v)", status, renamed)
	}
	if renamed["name"] != "after" {
		t.Errorf("the project came back named %v", renamed["name"])
	}

	// The declared one — here the implicit default — is not a record, so the
	// route answers the way it answers a project it does not hold.
	status, _ = do(t, ts, "PATCH", "/account/v3/projects/"+defaultProjectIdentifier, `{"name":"renamed"}`)
	if status != http.StatusNotFound {
		t.Errorf("renaming the operator's declared project answered %d, want 404", status)
	}
}

// The 412 is what this route exists for, and it is the measurement that made it
// worth serving: fr-par refuses a project that still holds a disk with
// precondition_failed / resource_still_in_use, and a client that read a 204 and
// moved on would leave resources behind believing they went with it.
//
// 412 and not 400, which is what writePreconditionFailed answers for instance/v1
// — the same body under two statuses, both measured, and a client branches on
// the status before it reads the body.
func TestDeleteProjectRefusesOneThatStillHoldsSomething(t *testing.T) {
	ts := newTestServer(t)
	id := projectMade(t, ts, "holding")

	status, created := do(t, ts, "POST", zoneURL+"/volumes",
		fmt.Sprintf(`{"name":"held","volume_type":"l_ssd","size":10000000000,"project":%q}`, id))
	if status != http.StatusCreated {
		t.Fatalf("create a volume in it: %d (%v)", status, created)
	}
	volume, _ := created["volume"].(map[string]any)
	volumeID, _ := volume["id"].(string)

	status, refused := do(t, ts, "DELETE", "/account/v3/projects/"+id, "")
	if status != http.StatusPreconditionFailed {
		t.Fatalf("deleting a project that still holds a volume answered %d, want 412 (%v)", status, refused)
	}
	if refused["precondition"] != "resource_still_in_use" {
		t.Errorf("precondition is %v, want resource_still_in_use", refused["precondition"])
	}
	if refused["type"] != "precondition_failed" {
		t.Errorf("type is %v, want precondition_failed", refused["type"])
	}

	// Emptied, it goes — the accepting half, without which a guard that refuses
	// everything would pass this test and break the product.
	if status, _ := do(t, ts, "DELETE", zoneURL+"/volumes/"+volumeID, ""); status != http.StatusNoContent {
		t.Fatalf("delete the volume: %d", status)
	}
	if status, body := do(t, ts, "DELETE", "/account/v3/projects/"+id, ""); status != http.StatusNoContent {
		t.Fatalf("deleting an empty project answered %d (%v)", status, body)
	}

	// And it is gone from the register, so a create naming it is refused again.
	status, _ = do(t, ts, "POST", zoneURL+"/volumes",
		fmt.Sprintf(`{"name":"after","volume_type":"l_ssd","size":10000000000,"project":%q}`, id))
	if status != http.StatusForbidden {
		t.Errorf("a create under a deleted project answered %d, want 403", status)
	}

	// A project nobody holds answers 404 on the delete, and its `resource` is
	// "project" where GetProject says "project_id". Two spellings of one word on
	// two routes of one product, both measured.
	status, absent := do(t, ts, "DELETE", "/account/v3/projects/"+id, "")
	if status != http.StatusNotFound {
		t.Fatalf("deleting a project twice answered %d, want 404", status)
	}
	if absent["resource"] != "project" {
		t.Errorf("resource is %v, want project — GetProject says project_id and this route does not", absent["resource"])
	}
}

// Every product that files a resource under a project refuses one nobody holds,
// and each answers in its own shape.
//
// The shapes are the measurement, not the statuses alone: a client branches on
// `type` and then reads `details`, and only instance/v1 names the project there.
// lb, block and iam each name their OWN product and the action refused, which is
// a different sentence — and answering one product in another's words is the
// invented format rule 4 forbids.
func TestACreateNamingAnUnknownProjectIsRefusedInItsProductsShape(t *testing.T) {
	const ghost = "aaaaaaaa-bbbb-4ccc-8ddd-eeeeeeeeeeee"

	for _, c := range []struct {
		name     string
		path     string
		body     string
		status   int
		resource string
		action   string
	}{
		{
			name: "instance CreateServer", path: zoneURL + "/servers",
			body:   `{"name":"g","commercial_type":"DEV1-S","project":"` + ghost + `"}`,
			status: http.StatusForbidden, resource: "project", action: "read",
		},
		{
			name: "instance CreateIP", path: zoneURL + "/ips",
			body:   `{"project":"` + ghost + `"}`,
			status: http.StatusForbidden, resource: "project", action: "read",
		},
		{
			name: "instance CreateSecurityGroup", path: zoneURL + "/security_groups",
			body:   `{"name":"g","project":"` + ghost + `"}`,
			status: http.StatusForbidden, resource: "project", action: "read",
		},
		{
			name: "instance CreatePlacementGroup", path: zoneURL + "/placement_groups",
			body:   `{"name":"g","project":"` + ghost + `"}`,
			status: http.StatusForbidden, resource: "project", action: "read",
		},
		{
			name: "lb CreateIP", path: "/lb/v1/zones/fr-par-1/ips",
			body:   `{"project_id":"` + ghost + `"}`,
			status: http.StatusForbidden, resource: "loadbalancer", action: "write",
		},
		{
			name: "block CreateVolume", path: "/block/v1/zones/fr-par-1/volumes",
			body:   `{"name":"g","project_id":"` + ghost + `","from_empty":{"size":10000000000}}`,
			status: http.StatusForbidden, resource: "volume", action: "write",
		},
		{
			name: "iam CreateSSHKey", path: "/iam/v1alpha1/ssh-keys",
			body: `{"name":"g","project_id":"` + ghost + `",` +
				`"public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIGhostKeyThatIsNotRealAAAAAAAAAAAAAAAAAAAAAA g"}`,
			status: http.StatusForbidden, resource: "ssh_key", action: "create",
		},
		{
			name: "vpc CreateVPC", path: "/vpc/v2/regions/fr-par/vpcs",
			body:   `{"name":"g","project_id":"` + ghost + `"}`,
			status: http.StatusNotFound,
		},
		{
			name: "vpcgw CreateIP", path: "/vpc-gw/v2/zones/fr-par-1/ips",
			body:   `{"project_id":"` + ghost + `"}`,
			status: http.StatusNotFound,
		},
		{
			name: "ipam BookIP", path: "/ipam/v1/regions/fr-par/ips",
			body:   `{"project_id":"` + ghost + `","source":{"zonal":"fr-par-1"}}`,
			status: http.StatusNotFound,
		},
	} {
		t.Run(c.name, func(t *testing.T) {
			ts := newTestServer(t)
			status, body := do(t, ts, "POST", c.path, c.body)
			if status != c.status {
				t.Fatalf("status %d, want %d (%v)", status, c.status, body)
			}
			if c.status == http.StatusNotFound {
				if body["type"] != "not_found" {
					t.Errorf("type is %v, want not_found", body["type"])
				}
				if body["resource"] != "project" {
					t.Errorf("resource is %v, want project", body["resource"])
				}
				if body["resource_id"] != ghost {
					t.Errorf("resource_id is %v, want the identifier that was named", body["resource_id"])
				}
				return
			}
			if body["type"] != "permissions_denied" {
				t.Errorf("type is %v, want permissions_denied", body["type"])
			}
			details, _ := body["details"].([]any)
			if len(details) != 1 {
				t.Fatalf("details holds %d entries, want 1 (%v)", len(details), body)
			}
			d, _ := details[0].(map[string]any)
			if d["resource"] != c.resource {
				t.Errorf("details.resource is %v, want %s", d["resource"], c.resource)
			}
			if d["action"] != c.action {
				t.Errorf("details.action is %v, want %s", d["action"], c.action)
			}
		})
	}
}

// defaultProjectIdentifier is the project a request naming none is filed under.
const defaultProjectIdentifier = "11111111-1111-1111-1111-111111111111"
