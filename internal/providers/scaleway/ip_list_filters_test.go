package scaleway_test

import (
	"net/http"
	"testing"
)

// The two filters the contract declared on ListIPs and the handler dropped
// (#277): `server` names the server an address is attached to, `detached`
// names the ones attached to nothing. Both are served conjunctively — an
// address attached to the named server is not detached, so the pair together
// narrows rather than the handler inventing which one wins.
func TestListIPsServerAndDetachedFiltersSelect(t *testing.T) {
	ts := newTestServer(t)
	a := createFlexibleIP(t, ts)
	b := createFlexibleIP(t, ts)
	serverID, _ := serverWith(t, ts, `{"name":"filter","commercial_type":"DEV1-S","image":"ubuntu_jammy","public_ips":["`+a+`"]}`)

	ids := func(query string) []string {
		t.Helper()
		status, out := do(t, ts, "GET", ipOrderZone+"/ips"+query, "")
		if status != http.StatusOK {
			t.Fatalf("GET /ips%s: %d (%v)", query, status, out)
		}
		list, _ := out["ips"].([]any)
		got := make([]string, 0, len(list))
		for _, raw := range list {
			ip, _ := raw.(map[string]any)
			id, _ := ip["id"].(string)
			got = append(got, id)
		}
		return got
	}

	if got := ids("?detached=true"); len(got) != 1 || got[0] != b {
		t.Errorf("detached=true lists %v, want only the unattached %s", got, b)
	}
	if got := ids("?detached=false"); len(got) != 1 || got[0] != a {
		t.Errorf("detached=false lists %v, want only the attached %s", got, a)
	}
	if got := ids("?server=" + serverID); len(got) != 1 || got[0] != a {
		t.Errorf("server=%s lists %v, want only the attached %s", serverID, got, a)
	}
	if got := ids("?server=00000000-0000-4000-8000-000000000000"); len(got) != 0 {
		t.Errorf("an unknown server lists %v, want none", got)
	}
	// Both together narrow: the address attached to this server is not
	// detached, so the pair answers empty rather than either filter alone.
	if got := ids("?server=" + serverID + "&detached=true"); len(got) != 0 {
		t.Errorf("server+detached lists %v, want none", got)
	}
	if status, _ := do(t, ts, "GET", ipOrderZone+"/ips?detached=banana", ""); status != http.StatusBadRequest {
		t.Errorf("detached=banana answered %d, want 400", status)
	}
}
