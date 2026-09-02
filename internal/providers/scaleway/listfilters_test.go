package scaleway_test

import (
	"fmt"
	"net/http"
	"testing"
)

// The two spellings of the page size, and why both have to work.
//
// instance/v1 sends `per_page`; vpc/v2 and ipam/v1, which are newer, send
// `page_size`. This package has one paging helper for both, and it read only the
// second — so every list route of instance/v1 served fifty items whatever the
// client asked. The suite never varied a page size, so nothing caught it.
func TestListHonoursBothPageSizeSpellings(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	for i := range 5 {
		body := fmt.Sprintf(`{"name":"web-%d","commercial_type":"DEV1-S"}`, i)
		if status, _ := do(t, ts, "POST", zone+"/servers", body); status != http.StatusCreated {
			t.Fatalf("seed %d: status %d", i, status)
		}
	}

	for _, query := range []string{"?per_page=2", "?page_size=2"} {
		status, body := do(t, ts, "GET", zone+"/servers"+query, "")
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", query, status)
		}
		servers, _ := body["servers"].([]any)
		if len(servers) != 2 {
			t.Fatalf("%s returned %d servers, want 2", query, len(servers))
		}
		// The window changes, the count does not: the SDK's pagination loop
		// terminates on total_count.
		if total, _ := body["total_count"].(float64); int(total) != 5 {
			t.Fatalf("%s: total_count is %v, want 5", query, body["total_count"])
		}
	}
}

// A filter that is ignored is worse than one that is refused: the caller gets an
// answer shaped exactly like the right one. `scw instance server list
// state=running` used to return every stopped server there was.
func TestListFiltersAreApplied(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	mustCreate := func(body string) string {
		t.Helper()
		status, out := do(t, ts, "POST", zone+"/servers", body)
		if status != http.StatusCreated {
			t.Fatalf("create: status %d (%v)", status, out)
		}
		server, _ := out["server"].(map[string]any)
		id, _ := server["id"].(string)
		return id
	}

	// The names are the SDK's own example, not decoration: "server1" must return
	// "server100" and "server1" but not "foo". Names of one letter make equality
	// and substring indistinguishable, which is how an equality filter passed
	// its own test.
	running := mustCreate(`{"name":"srv1","commercial_type":"DEV1-S","tags":["team-a","ci"]}`)
	mustCreate(`{"name":"srv100","commercial_type":"DEV1-S","tags":["team-a"]}`)
	mustCreate(`{"name":"foo","commercial_type":"GP1-XS"}`)

	if status, _ := do(t, ts, "POST", zone+"/servers/"+running+"/action", `{"action":"poweron"}`); status != http.StatusAccepted {
		t.Fatalf("poweron: status %d", status)
	}

	count := func(query string) int {
		t.Helper()
		status, body := do(t, ts, "GET", zone+"/servers"+query, "")
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", query, status)
		}
		servers, _ := body["servers"].([]any)
		return len(servers)
	}

	cases := []struct {
		query string
		want  int
	}{
		{"", 3},
		{"?state=running", 1},
		{"?state=stopped", 2},
		{"?state=nonsense", 0},
		{"?commercial_type=DEV1-S", 2},
		{"?commercial_type=GP1-XS", 1},
		{"?tags=team-a", 2},
		{"?tags=team-a,ci", 1},
		{"?tags=team-a,absent", 0},
		// The SDK's example, verbatim: "server1" returns "server100" and
		// "server1" but not "foo".
		{"?name=srv1", 2},
		{"?name=srv100", 1},
		{"?name=foo", 1},
		{"?name=absent", 0},
		// Filters combine rather than override, which is the only reading that
		// makes a list route usable in a script.
		{"?name=srv1&state=running", 1},
		{"?name=srv1&state=stopped", 1},
		{"?name=foo&state=running", 0},
	}
	for _, tc := range cases {
		if got := count(tc.query); got != tc.want {
			t.Errorf("list%s returned %d servers, want %d", tc.query, got, tc.want)
		}
	}
}

// The retype Terraform performs, and the restriction the SDK documents on it.
//
// UpdateServer declared commercial_type and volumes and read neither: the answer
// was 200, nothing changed, and the next plan asked for the same change forever.
func TestUpdateServerAppliesCommercialType(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/servers", `{"name":"demo","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)

	status, out = do(t, ts, "PATCH", zone+"/servers/"+id, `{"commercial_type":"DEV1-M"}`)
	if status != http.StatusOK {
		t.Fatalf("retype: status %d (%v)", status, out)
	}
	updated, _ := out["server"].(map[string]any)
	if updated["commercial_type"] != "DEV1-M" {
		t.Fatalf("the type is %v after the update, want DEV1-M", updated["commercial_type"])
	}

	// "Cannot be changed if the Instance is not in `stopped` state", says the
	// SDK. A test that resizes a running server and sees it work would ship code
	// the real API rejects.
	if status, _ := do(t, ts, "POST", zone+"/servers/"+id+"/action", `{"action":"poweron"}`); status != http.StatusAccepted {
		t.Fatalf("poweron: status %d", status)
	}
	status, _ = do(t, ts, "PATCH", zone+"/servers/"+id, `{"commercial_type":"DEV1-S"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("retyping a running server answered %d, want 400", status)
	}
}

// The volume map replaces rather than merges, and names a volume that must
// exist. Accepting an unknown id would attach nothing and answer 200.
func TestUpdateServerAttachesAndDetachesVolumes(t *testing.T) {
	ts := newTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/servers", `{"name":"demo","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	id, _ := server["id"].(string)
	volumes, _ := server["volumes"].(map[string]any)
	root, _ := volumes["0"].(map[string]any)
	rootID, _ := root["id"].(string)

	status, out = do(t, ts, "POST", zone+"/volumes", `{"name":"extra","volume_type":"l_ssd","size":10000000000}`)
	if status != http.StatusCreated {
		t.Fatalf("create volume: status %d (%v)", status, out)
	}
	vol, _ := out["volume"].(map[string]any)
	extraID, _ := vol["id"].(string)

	body := fmt.Sprintf(`{"volumes":{"0":{"id":%q},"1":{"id":%q}}}`, rootID, extraID)
	status, out = do(t, ts, "PATCH", zone+"/servers/"+id, body)
	if status != http.StatusOK {
		t.Fatalf("attach: status %d (%v)", status, out)
	}
	updated, _ := out["server"].(map[string]any)
	attached, _ := updated["volumes"].(map[string]any)
	if len(attached) != 2 {
		t.Fatalf("the server carries %d volumes after the attach, want 2", len(attached))
	}

	// Dropped from the map means detached, which is what a plan that removes a
	// volume expects.
	body = fmt.Sprintf(`{"volumes":{"0":{"id":%q}}}`, rootID)
	status, out = do(t, ts, "PATCH", zone+"/servers/"+id, body)
	if status != http.StatusOK {
		t.Fatalf("detach: status %d", status)
	}
	updated, _ = out["server"].(map[string]any)
	attached, _ = updated["volumes"].(map[string]any)
	if len(attached) != 1 {
		t.Fatalf("the server carries %d volumes after the detach, want 1", len(attached))
	}

	// The response is rebuilt from the request, so counting its entries proves
	// only that the request was echoed: it has the right size whether or not
	// anything was detached. The volume itself is what has to be re-read. An
	// audit removed the detachment loop entirely and this test still passed.
	status, out = do(t, ts, "GET", zone+"/volumes/"+extraID, "")
	if status != http.StatusOK {
		t.Fatalf("reading the detached volume: status %d", status)
	}
	detached, _ := out["volume"].(map[string]any)
	if server := detached["server"]; server != nil {
		t.Fatalf("the volume dropped from the map still belongs to %v", server)
	}

	status, _ = do(t, ts, "PATCH", zone+"/servers/"+id,
		`{"volumes":{"0":{"id":"eeeeeeee-eeee-eeee-eeee-eeeeeeeeeeee"}}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("attaching a volume that does not exist answered %d, want 400", status)
	}
}
