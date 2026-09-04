package scaleway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// The two Day-2 findings that needed no runtime (#676), held against what a
// real account answered on 2026-09-04.

// TestANullReverseClearsIt: the SDK sends null to clear a reverse, a real
// account answers 200 and reads null, and so does an empty string. A write
// accepted and dropped was the shape of #654 one product over.
func TestANullReverseClearsIt(t *testing.T) {
	ts := newTestServer(t)
	status, created := do(t, ts, "POST", instanceZone+"/ips", `{}`)
	if status != http.StatusCreated {
		t.Fatalf("create ip: status %d (%v)", status, created)
	}
	ip, _ := created["ip"].(map[string]any)
	id, _ := ip["id"].(string)
	if ip["reverse"] != nil {
		t.Fatalf("a fresh ip carries reverse %v, and a real account answers null", ip["reverse"])
	}
	path := instanceZone + "/ips/" + id

	status, out := do(t, ts, "PATCH", path, `{"reverse":"web.platform.example"}`)
	if status != http.StatusOK || out["ip"].(map[string]any)["reverse"] != "web.platform.example" {
		t.Fatalf("set reverse: status %d, %v", status, out["ip"])
	}
	// Absent leaves it alone: a tag update must not clear a reverse.
	do(t, ts, "PATCH", path, `{"tags":["day2"]}`)
	_, read := do(t, ts, "GET", path, "")
	if read["ip"].(map[string]any)["reverse"] != "web.platform.example" {
		t.Fatalf("an update that did not name the reverse changed it: %v", read["ip"])
	}
	// Null clears, and the read says so.
	status, out = do(t, ts, "PATCH", path, `{"reverse":null}`)
	if status != http.StatusOK {
		t.Fatalf("PATCH reverse null: status %d (%v)", status, out)
	}
	_, read = do(t, ts, "GET", path, "")
	if got := read["ip"].(map[string]any)["reverse"]; got != nil {
		t.Fatalf("after {\"reverse\": null} the read still carries %v: the write was accepted and nothing happened (#654, #676)", got)
	}
	// And so does the empty string, which a real account normalises to null.
	do(t, ts, "PATCH", path, `{"reverse":"again.platform.example"}`)
	do(t, ts, "PATCH", path, `{"reverse":""}`)
	_, read = do(t, ts, "GET", path, "")
	if got := read["ip"].(map[string]any)["reverse"]; got != nil {
		t.Fatalf("after {\"reverse\": \"\"} the read carries %v, and a real account reads null", got)
	}
}

// backendBench builds a balancer with one backend holding one server.
func backendBench(t *testing.T) (ts *httptest.Server, backendID string) {
	t.Helper()
	ts = newTestServer(t)
	v4ID, _ := lbIP(t, ts, `{}`)
	status, lb := do(t, ts, "POST", lbURL+"/lbs", fmt.Sprintf(`{"name":"front","type":"LB-S","ip_ids":[%q]}`, v4ID))
	if status != http.StatusOK {
		t.Fatalf("create lb: status %d (%v)", status, lb)
	}
	lbID, _ := lb["id"].(string)
	status, backend := do(t, ts, "POST", lbURL+"/lbs/"+lbID+"/backends",
		`{"name":"web","forward_protocol":"tcp","forward_port":80,"server_ip":["10.0.0.1"],
		  "health_check":{"port":80,"check_delay":30000,"check_timeout":5000,"check_max_retries":2,
		                  "https_config":{"uri":"/healthz","code":200}}}`)
	if status != http.StatusOK {
		t.Fatalf("create backend: status %d (%v)", status, backend)
	}
	backendID, _ = backend["id"].(string)
	return ts, backendID
}

func poolOf(t *testing.T, doc map[string]any) []any {
	t.Helper()
	pool, _ := doc["pool"].([]any)
	return pool
}

// TestAddingABackendServerPutsItFirstAndRefusesADuplicate is the measured
// POST: the new address goes first, and an address the pool already holds
// refuses the whole request by name — pool unchanged, even for the other
// addresses of the batch.
func TestAddingABackendServerPutsItFirstAndRefusesADuplicate(t *testing.T) {
	ts, backendID := backendBench(t)
	path := lbURL + "/backends/" + backendID + "/servers"

	status, out := do(t, ts, "POST", path, `{"server_ip":["10.0.0.2"]}`)
	if status != http.StatusOK {
		t.Fatalf("add: status %d (%v)", status, out)
	}
	if got := fmt.Sprint(poolOf(t, out)); got != "[10.0.0.2 10.0.0.1]" {
		t.Fatalf("pool after the add is %s, want the new address first, as the cloud answers", got)
	}

	status, refused := do(t, ts, "POST", path, `{"server_ip":["10.0.0.3","10.0.0.1"]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a batch holding an address the pool already has: status %d (%v)", status, refused)
	}
	details, _ := refused["details"].([]any)
	if len(details) != 1 {
		t.Fatalf("the refusal carries %d details, want the one naming server_ip: %v", len(details), refused)
	}
	detail, _ := details[0].(map[string]any)
	if detail["argument_name"] != "server_ip" || detail["reason"] != "constraint" ||
		detail["help_message"] != "server_ip 10.0.0.1 already exists" {
		t.Fatalf("the refusal is not the cloud's: %v", detail)
	}
	_, read := do(t, ts, "GET", lbURL+"/backends/"+backendID, "")
	if got := fmt.Sprint(poolOf(t, read)); got != "[10.0.0.2 10.0.0.1]" {
		t.Fatalf("a refused batch changed the pool to %s: the cloud refuses it whole", got)
	}
}

// TestRemovingABackendServerLeavesTheOthersAndIgnoresAnAbsentOne is the
// measured DELETE: the named address goes, the rest stay, and an address the
// pool does not hold is a success that changes nothing.
func TestRemovingABackendServerLeavesTheOthersAndIgnoresAnAbsentOne(t *testing.T) {
	ts, backendID := backendBench(t)
	path := lbURL + "/backends/" + backendID + "/servers"
	do(t, ts, "POST", path, `{"server_ip":["10.0.0.2"]}`)

	status, out := do(t, ts, "DELETE", path, `{"server_ip":["10.0.0.1"]}`)
	if status != http.StatusOK || fmt.Sprint(poolOf(t, out)) != "[10.0.0.2]" {
		t.Fatalf("remove: status %d, pool %v", status, poolOf(t, out))
	}
	status, out = do(t, ts, "DELETE", path, `{"server_ip":["10.0.0.9"]}`)
	if status != http.StatusOK || fmt.Sprint(poolOf(t, out)) != "[10.0.0.2]" {
		t.Fatalf("remove an absent address: status %d, pool %v; the cloud answers 200 and the same pool", status, poolOf(t, out))
	}
}

// TestSetAddAndRemoveAgreeOnThePool holds the three routes to one register:
// what one of them wrote, the others read, and a read of the backend agrees.
func TestSetAddAndRemoveAgreeOnThePool(t *testing.T) {
	ts, backendID := backendBench(t)
	path := lbURL + "/backends/" + backendID + "/servers"
	do(t, ts, "PUT", path, `{"server_ip":["10.0.0.7","10.0.0.8"]}`)
	do(t, ts, "POST", path, `{"server_ip":["10.0.0.9"]}`)
	do(t, ts, "DELETE", path, `{"server_ip":["10.0.0.8"]}`)
	_, read := do(t, ts, "GET", lbURL+"/backends/"+backendID, "")
	if got := fmt.Sprint(poolOf(t, read)); got != "[10.0.0.9 10.0.0.7]" {
		t.Fatalf("set, add, remove: the backend reads %s, want [10.0.0.9 10.0.0.7]", got)
	}
	// And a set after an add replaces what the add put there.
	_, out := do(t, ts, "PUT", path, `{"server_ip":["10.0.0.1"]}`)
	if got := fmt.Sprint(poolOf(t, out)); got != "[10.0.0.1]" {
		t.Fatalf("a set after an add reads %s", got)
	}
}
