package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// placementGroupID creates a v1 placement group and returns its ID.
func placementGroupID(t *testing.T, ts *httptest.Server, body string) string {
	t.Helper()
	status, created := do(t, ts, "POST", zoneURL+"/placement_groups", body)
	if status != http.StatusCreated {
		t.Fatalf("create placement group: expected 201, got %d (%v)", status, created)
	}
	group, _ := created["placement_group"].(map[string]any)
	id, _ := group["id"].(string)
	if id == "" {
		t.Fatalf("create placement group: no id in %v", created)
	}
	return id
}

// placementServerID creates a server, optionally inside a group.
func placementServerID(t *testing.T, ts *httptest.Server, name, groupID string) string {
	t.Helper()
	body := `{"name":"` + name + `","commercial_type":"DEV1-S"`
	if groupID != "" {
		body += `,"placement_group":"` + groupID + `"`
	}
	body += `}`
	status, created := do(t, ts, "POST", zoneURL+"/servers", body)
	if status != http.StatusCreated {
		t.Fatalf("create server: expected 201, got %d (%v)", status, created)
	}
	server, _ := created["server"].(map[string]any)
	id, _ := server["id"].(string)
	if id == "" {
		t.Fatalf("create server: no id in %v", created)
	}
	return id
}

func TestPlacementGroupLifecycle(t *testing.T) {
	ts := newTestServer(t)

	id := placementGroupID(t, ts, `{"name":"controlplane","policy_type":"max_availability","policy_mode":"enforced","tags":["infra"]}`)

	status, got := do(t, ts, "GET", zoneURL+"/placement_groups/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", status)
	}
	group, _ := got["placement_group"].(map[string]any)

	// Create-then-read must round-trip, field for field: what 2.43.0 stores is
	// exactly this view (ResourceInstancePlacementGroupRead sets every one).
	for field, want := range map[string]any{
		"name":             "controlplane",
		"policy_type":      "max_availability",
		"policy_mode":      "enforced",
		"policy_respected": true,
		"zone":             "fr-par-1",
	} {
		if group[field] != want {
			t.Errorf("get %s: got %v, want %v", field, group[field], want)
		}
	}
	if group["organization"] == "" || group["project"] == "" {
		t.Errorf("get: missing owner fields in %v", group)
	}

	status, updated := do(t, ts, "PATCH", zoneURL+"/placement_groups/"+id, `{"name":"cp","policy_type":"low_latency"}`)
	if status != http.StatusOK {
		t.Fatalf("update: expected 200, got %d", status)
	}
	group, _ = updated["placement_group"].(map[string]any)
	if group["name"] != "cp" || group["policy_type"] != "low_latency" {
		t.Errorf("update did not stick: %v", group)
	}
	// policy_mode untouched by a PATCH that does not name it.
	if group["policy_mode"] != "enforced" {
		t.Errorf("update erased policy_mode: %v", group)
	}

	status, list := do(t, ts, "GET", zoneURL+"/placement_groups", "")
	if status != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", status)
	}
	if count, _ := list["total_count"].(float64); count != 1 {
		t.Errorf("list total_count: got %v, want 1", list["total_count"])
	}

	status, _ = do(t, ts, "DELETE", zoneURL+"/placement_groups/"+id, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", status)
	}
	status, _ = do(t, ts, "GET", zoneURL+"/placement_groups/"+id, "")
	if status != http.StatusNotFound {
		t.Fatalf("get after delete: expected 404, got %d", status)
	}
}

// TestASpreadPolicyOnTheSingleHostReadsUnrespected is the control behind the
// decision to serve this family at all (#285). The declined era's reason was
// "any policy would be reported satisfied whatever it asked"; serving is
// honest only while that sentence stays false. Every emulated machine runs on
// the single host that started feint, so a max_availability group with two
// running members is not spread, and the group endpoint must say so.
func TestASpreadPolicyOnTheSingleHostReadsUnrespected(t *testing.T) {
	ts := newTestServer(t)

	id := placementGroupID(t, ts, `{"name":"spread","policy_type":"max_availability"}`)
	first := placementServerID(t, ts, "cp-1", id)
	second := placementServerID(t, ts, "cp-2", id)

	respected := func() bool {
		t.Helper()
		status, got := do(t, ts, "GET", zoneURL+"/placement_groups/"+id, "")
		if status != http.StatusOK {
			t.Fatalf("get: expected 200, got %d", status)
		}
		group, _ := got["placement_group"].(map[string]any)
		value, _ := group["policy_respected"].(bool)
		return value
	}

	// Stopped servers sit on no hypervisor (the server view answers
	// location: null for them), so nothing is violated yet.
	if !respected() {
		t.Fatal("a spread policy with no running member reads unrespected; nothing is placed yet")
	}

	for _, server := range []string{first, second} {
		status, _ := do(t, ts, "POST", zoneURL+"/servers/"+server+"/action", `{"action":"poweron"}`)
		if status != http.StatusOK && status != http.StatusAccepted {
			t.Fatalf("poweron: unexpected status %d", status)
		}
	}

	// Two running members, one host: the policy is not respected, and saying
	// otherwise is the exact lie the family was declined over.
	if respected() {
		t.Fatal("a max_availability group with two running members on the single host reads respected: the record has become a promise nothing honours")
	}

	// low_latency is the other policy, and one host delivers it by
	// construction.
	status, _ := do(t, ts, "PATCH", zoneURL+"/placement_groups/"+id, `{"policy_type":"low_latency"}`)
	if status != http.StatusOK {
		t.Fatalf("update: expected 200, got %d", status)
	}
	if !respected() {
		t.Fatal("a low_latency group on one host reads unrespected; grouped on the same hardware is the one thing this emulator does deliver")
	}
}

// TestServerEmbeddedPlacementGroupPinsPolicyRespectedFalse: the SDK's own
// comment (PlacementGroup.PolicyRespected, instance/v1/instance_sdk.go): "In
// the server endpoints the value is always false as it is deprecated." The
// server view mirrors the recorded API, not the computed truth.
func TestServerEmbeddedPlacementGroupPinsPolicyRespectedFalse(t *testing.T) {
	ts := newTestServer(t)

	id := placementGroupID(t, ts, `{"name":"spread"}`)
	server := placementServerID(t, ts, "cp-1", id)

	status, got := do(t, ts, "GET", zoneURL+"/servers/"+server, "")
	if status != http.StatusOK {
		t.Fatalf("get server: expected 200, got %d", status)
	}
	view, _ := got["server"].(map[string]any)
	group, ok := view["placement_group"].(map[string]any)
	if !ok {
		t.Fatalf("server.placement_group is not an object: %v", view["placement_group"])
	}
	if group["id"] != id {
		t.Errorf("server.placement_group.id: got %v, want %s", group["id"], id)
	}
	if respected, _ := group["policy_respected"].(bool); respected {
		t.Error("server-embedded policy_respected must stay false: the SDK documents the server endpoints always answer false")
	}

	// And the group endpoint keeps the correct value at the same moment: one
	// stopped member violates nothing.
	status, got = do(t, ts, "GET", zoneURL+"/placement_groups/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get group: expected 200, got %d", status)
	}
	group, _ = got["placement_group"].(map[string]any)
	if respected, _ := group["policy_respected"].(bool); !respected {
		t.Error("group endpoint must carry the computed value, not the server endpoints' deprecated false")
	}
}

func TestPlacementGroupMembershipTravelsTheWholeLifecycle(t *testing.T) {
	ts := newTestServer(t)

	id := placementGroupID(t, ts, `{"name":"web"}`)
	inGroup := placementServerID(t, ts, "web-1", id)
	loose := placementServerID(t, ts, "web-2", "")

	// The group computes its members from the servers.
	status, got := do(t, ts, "GET", zoneURL+"/placement_groups/"+id+"/servers", "")
	if status != http.StatusOK {
		t.Fatalf("get servers: expected 200, got %d", status)
	}
	servers, _ := got["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("members: got %d, want 1 (%v)", len(servers), got)
	}
	member, _ := servers[0].(map[string]any)
	if member["id"] != inGroup || member["name"] != "web-1" {
		t.Errorf("member: got %v", member)
	}

	// PUT /servers replaces the whole membership, both directions at once.
	status, got = do(t, ts, "PUT", zoneURL+"/placement_groups/"+id+"/servers", `{"servers":["`+loose+`"]}`)
	if status != http.StatusOK {
		t.Fatalf("set servers: expected 200, got %d (%v)", status, got)
	}
	servers, _ = got["servers"].([]any)
	if len(servers) != 1 {
		t.Fatalf("members after set: got %d, want 1", len(servers))
	}
	member, _ = servers[0].(map[string]any)
	if member["id"] != loose {
		t.Errorf("member after set: got %v, want %s", member["id"], loose)
	}
	// The evicted server's own view agrees.
	_, view := do(t, ts, "GET", zoneURL+"/servers/"+inGroup, "")
	server, _ := view["server"].(map[string]any)
	if server["placement_group"] != nil {
		t.Errorf("evicted server still wears the group: %v", server["placement_group"])
	}

	// UpdateServer with null detaches (the provider's destroy path); with an
	// ID it attaches — the provider only sends that on a stopped server, and
	// the emulator refuses it running, the same guard the provider applies
	// client-side.
	status, _ = do(t, ts, "PATCH", zoneURL+"/servers/"+loose, `{"placement_group":null}`)
	if status != http.StatusOK {
		t.Fatalf("detach via update: expected 200, got %d", status)
	}
	status, got = do(t, ts, "GET", zoneURL+"/placement_groups/"+id+"/servers", "")
	if status != http.StatusOK {
		t.Fatalf("get servers: expected 200, got %d", status)
	}
	if servers, _ := got["servers"].([]any); len(servers) != 0 {
		t.Fatalf("members after detach: got %d, want 0", len(servers))
	}

	status, _ = do(t, ts, "PATCH", zoneURL+"/servers/"+loose, `{"placement_group":"`+id+`"}`)
	if status != http.StatusOK {
		t.Fatalf("attach stopped server: expected 200, got %d", status)
	}
	do(t, ts, "POST", zoneURL+"/servers/"+loose+"/action", `{"action":"poweron"}`)
	status, refused := do(t, ts, "PATCH", zoneURL+"/servers/"+loose, `{"placement_group":"`+id+`"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("attach running server: expected 400, got %d (%v)", status, refused)
	}

	// Deleting the group frees the members without touching them.
	status, _ = do(t, ts, "DELETE", zoneURL+"/placement_groups/"+id, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete group: expected 204, got %d", status)
	}
	_, view = do(t, ts, "GET", zoneURL+"/servers/"+loose, "")
	server, _ = view["server"].(map[string]any)
	if server["placement_group"] != nil {
		t.Errorf("deleted group still shows on its ex-member: %v", server["placement_group"])
	}
}

func TestPlacementGroupErrorShapes(t *testing.T) {
	ts := newTestServer(t)

	// Unknown group: not_found, the type the SDK maps onto
	// ResourceNotFoundError.
	status, body := do(t, ts, "GET", zoneURL+"/placement_groups/00000000-dead-4000-8000-000000000000", "")
	if status != http.StatusNotFound || body["type"] != "not_found" {
		t.Errorf("get unknown: got %d %v", status, body)
	}

	// A policy outside the enum: invalid_arguments naming the field.
	status, body = do(t, ts, "POST", zoneURL+"/placement_groups", `{"name":"x","policy_type":"everywhere"}`)
	if status != http.StatusBadRequest || body["type"] != "invalid_arguments" {
		t.Errorf("bad policy_type: got %d %v", status, body)
	}

	// A server create naming a group that does not exist refuses before
	// storing anything: no phantom server survives the 400.
	status, body = do(t, ts, "POST", zoneURL+"/servers", `{"name":"s","commercial_type":"DEV1-S","placement_group":"00000000-dead-4000-8000-000000000000"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("create with unknown group: expected 400, got %d (%v)", status, body)
	}
	_, list := do(t, ts, "GET", zoneURL+"/servers", "")
	if servers, _ := list["servers"].([]any); len(servers) != 0 {
		t.Errorf("a refused create left a phantom server: %v", list)
	}

	// Wrong zone: the group is zonal, and fr-par-2 must not see fr-par-1's.
	id := placementGroupID(t, ts, `{"name":"zonal"}`)
	status, _ = do(t, ts, "GET", "/instance/v1/zones/fr-par-2/placement_groups/"+id, "")
	if status != http.StatusNotFound {
		t.Errorf("cross-zone get: expected 404, got %d", status)
	}
}

// The v2alpha1 door, which is where Terraform provider 2.81.0 does its CRUD:
// one store, two shapes, and the fields the alpha does not carry stay
// readable through v1 on the same ID.
func TestPlacementGroupV2AlphaSharesTheStoreWithV1(t *testing.T) {
	ts := newTestServer(t)
	v2 := "/instance/v2alpha1/zones/fr-par-1"

	status, created := do(t, ts, "POST", v2+"/placement-groups", `{"name":"cp","policy_type":"max_availability","project_id":"","tags":[]}`)
	if status != http.StatusCreated {
		t.Fatalf("v2 create: expected 201, got %d (%v)", status, created)
	}
	if created["placement_group"] != nil {
		t.Fatalf("v2 create must answer the bare object, no envelope: %v", created)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("v2 create: no id in %v", created)
	}
	for _, field := range []string{"project_id", "created_at", "updated_at", "policy_type", "tags", "zone"} {
		if _, present := created[field]; !present {
			t.Errorf("v2 create: missing %s in %v", field, created)
		}
	}
	if _, present := created["policy_mode"]; present {
		t.Errorf("v2 view must not carry policy_mode, the alpha dropped it: %v", created)
	}

	// The provider's create flow continues through v1: PATCH policy_mode,
	// then GET for policy_mode and policy_respected — same ID, other door.
	status, _ = do(t, ts, "PATCH", zoneURL+"/placement_groups/"+id, `{"policy_mode":"enforced"}`)
	if status != http.StatusOK {
		t.Fatalf("v1 update of a v2-created group: expected 200, got %d", status)
	}
	status, got := do(t, ts, "GET", zoneURL+"/placement_groups/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("v1 get of a v2-created group: expected 200, got %d", status)
	}
	group, _ := got["placement_group"].(map[string]any)
	if group["policy_mode"] != "enforced" {
		t.Errorf("v1 read: policy_mode got %v, want enforced", group["policy_mode"])
	}
	if _, present := group["policy_respected"]; !present {
		t.Errorf("v1 read: missing policy_respected in %v", group)
	}

	// The data source's name lookup.
	status, list := do(t, ts, "GET", v2+"/placement-groups?name=cp", "")
	if status != http.StatusOK {
		t.Fatalf("v2 list: expected 200, got %d", status)
	}
	groups, _ := list["placement_groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("v2 list by name: got %d, want 1 (%v)", len(groups), list)
	}
	if _, present := list["next_page_token"]; !present {
		t.Errorf("v2 list: missing next_page_token in %v", list)
	}

	status, _ = do(t, ts, "DELETE", v2+"/placement-groups/"+id, "")
	if status != http.StatusNoContent {
		t.Fatalf("v2 delete: expected 204, got %d", status)
	}
	status, _ = do(t, ts, "GET", zoneURL+"/placement_groups/"+id, "")
	if status != http.StatusNotFound {
		t.Fatalf("v1 get after v2 delete: expected 404, got %d — one store, or two half-truths", status)
	}
}
