package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const v2alphaNICs = "/instance/v2alpha1/zones/fr-par-1/private-network-interfaces"

// attachedNIC creates a server, a private network and the interface between
// them, and hands back the three identifiers plus the v1 answer.
func attachedNIC(t *testing.T, ts *httptest.Server, name, subnet string) (serverID, pnID string, nic map[string]any) {
	t.Helper()
	pnID, _ = privateNetwork(t, ts, `{"name":"`+name+`","subnets":["`+subnet+`"]}`)
	serverID, _ = serverWith(t, ts, `{"name":"`+name+`","commercial_type":"DEV1-S"}`)
	status, created := do(t, ts, "POST",
		zoneURL+"/servers/"+serverID+"/private_nics", `{"private_network_id":"`+pnID+`"}`)
	if status != http.StatusCreated {
		t.Fatalf("attach: expected 201, got %d (%v)", status, created)
	}
	nic, _ = created["private_nic"].(map[string]any)
	return serverID, pnID, nic
}

func v2alphaList(t *testing.T, ts *httptest.Server, query string) map[string]any {
	t.Helper()
	status, body := do(t, ts, "GET", v2alphaNICs+query, "")
	if status != http.StatusOK {
		t.Fatalf("GET %s: expected 200, got %d (%v)", v2alphaNICs+query, status, body)
	}
	return body
}

// The interface a client creates through instance/v1 is the one it reads through
// instance/v2alpha1.
//
// This is the whole point of the file under test, and the defect that made it
// necessary: Terraform provider 2.81.0 creates through v1 and reads through
// v2alpha1, so two stores — or two shapes that disagree — leave the provider
// unable to see what it just made. Identifiers are compared, not merely counted.
func TestAnInterfaceCreatedOnV1IsReadOnV2alpha1(t *testing.T) {
	ts := newTestServer(t)
	serverID, pnID, nic := attachedNIC(t, ts, "v2alpha", "10.170.0.0/24")

	body := v2alphaList(t, ts, "")
	list, _ := body["private_network_interfaces"].([]any)
	if len(list) != 1 {
		t.Fatalf("v2alpha1 lists %d interface(s), v1 created one: %v", len(list), body)
	}
	got, _ := list[0].(map[string]any)

	for field, want := range map[string]any{
		"id":                 nic["id"],
		"server_id":          serverID,
		"private_network_id": pnID,
		"mac_address":        nic["mac_address"],
		"status":             "available",
	} {
		if got[field] != want {
			t.Errorf("%s = %v, the v1 answer says %v", field, got[field], want)
		}
	}
	// The address is the same one, reached through the field v2alpha1 names.
	if ids, _ := got["ip_ids"].([]any); len(ids) != 1 {
		t.Errorf("ip_ids = %v, want the single address v1 booked", got["ip_ids"])
	}
	// Present and null: the SDK's pagination loop ends on this field, and a
	// missing one decodes to the same zero value as an empty string, which would
	// make it ask for one more page forever.
	if _, present := body["next_page_token"]; !present {
		t.Error("next_page_token is absent; the SDK reads it to know it is done")
	}
	if body["next_page_token"] != nil {
		t.Errorf("next_page_token = %v on a complete page, want null", body["next_page_token"])
	}
}

// A filter that is sent is a filter that is applied.
//
// The recorded client sends server_ids and project_id on every call. A handler
// that accepted them and answered with the whole zone would look correct against
// one server, and hand the provider another server's interfaces as soon as a
// second exists — which is what a realistic stack has.
func TestTheV2alpha1FiltersSelect(t *testing.T) {
	ts := newTestServer(t)
	first, _, firstNIC := attachedNIC(t, ts, "first", "10.171.0.0/24")
	second, _, _ := attachedNIC(t, ts, "second", "10.172.0.0/24")

	only := v2alphaList(t, ts, "?server_ids="+first)
	list, _ := only["private_network_interfaces"].([]any)
	if len(list) != 1 {
		t.Fatalf("server_ids selected %d interface(s), want the one of %s", len(list), first)
	}
	if got, _ := list[0].(map[string]any); got["id"] != firstNIC["id"] {
		t.Errorf("server_ids returned %v, want %v", got["id"], firstNIC["id"])
	}

	// An unknown project owns nothing, and an empty list is the answer — not
	// every interface in the zone.
	none := v2alphaList(t, ts, "?project_id=99999999-9999-9999-9999-999999999999")
	if list, _ := none["private_network_interfaces"].([]any); len(list) != 0 {
		t.Errorf("a foreign project sees %d interface(s), want none", len(list))
	}

	all := v2alphaList(t, ts, "")
	if list, _ := all["private_network_interfaces"].([]any); len(list) != 2 {
		t.Errorf("the unfiltered list has %d interface(s), want 2 (%s and %s)", len(list), first, second)
	}
}

// A NIC deleted through v1 is gone from v2alpha1, because there is one object.
func TestDeletingOnV1EmptiesTheV2alpha1List(t *testing.T) {
	ts := newTestServer(t)
	serverID, _, nic := attachedNIC(t, ts, "gone", "10.173.0.0/24")

	id, _ := nic["id"].(string)
	if status, body := do(t, ts, "DELETE",
		zoneURL+"/servers/"+serverID+"/private_nics/"+id, ""); status != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d (%v)", status, body)
	}

	body := v2alphaList(t, ts, "")
	if list, _ := body["private_network_interfaces"].([]any); len(list) != 0 {
		t.Errorf("v2alpha1 still lists %d interface(s) after a v1 delete", len(list))
	}
	if body["total_count"] != float64(0) {
		t.Errorf("total_count = %v after a v1 delete, want 0", body["total_count"])
	}
}

// Pagination hands back a token that returns the rest, and stops.
//
// The loop terminates on next_page_token being null. A handler that always
// answered with a token would make the SDK read forever; one that never answered
// with a token would silently truncate a list at the page size, which is the
// defect paging.go documents for instance/v1's `per_page`.
func TestTheV2alpha1PageTokenWalksTheWholeList(t *testing.T) {
	ts := newTestServer(t)
	for _, spec := range []struct{ name, subnet string }{
		{"one", "10.174.1.0/24"},
		{"two", "10.174.2.0/24"},
		{"three", "10.174.3.0/24"},
	} {
		attachedNIC(t, ts, spec.name, spec.subnet)
	}

	seen := map[string]bool{}
	query := "?page_size=2"
	for range 5 {
		body := v2alphaList(t, ts, query)
		list, _ := body["private_network_interfaces"].([]any)
		for _, entry := range list {
			nic, _ := entry.(map[string]any)
			id, _ := nic["id"].(string)
			if seen[id] {
				t.Errorf("interface %s came back on two pages", id)
			}
			seen[id] = true
		}
		token, ok := body["next_page_token"].(string)
		if !ok {
			break
		}
		query = "?page_size=2&page_token=" + token
	}
	if len(seen) != 3 {
		t.Errorf("the walk saw %d interface(s) of 3", len(seen))
	}
}
