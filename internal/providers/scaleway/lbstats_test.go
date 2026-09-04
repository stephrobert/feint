package scaleway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

// Four Load Balancer reads a Day-2 client asks for (#666), held at the level
// a handler test can hold them: what the stats say about a pool address with
// a server behind it and one without, the one field they never fill, the
// window, the filter, and the two lists that are empty because that is what
// a balancer here holds.

// statsBench builds a private network, a server on it through a NIC, a
// running server, and a balancer whose backend names that server's address
// beside one nobody holds.
func statsBench(t *testing.T) (ts *httptest.Server, lbID, backendID, serverID, held string) {
	t.Helper()
	ts = newTestServer(t)
	pnID, _ := createPN(t, ts, "10.63.0.0/24")

	status, server := do(t, ts, "POST", instanceZone+"/servers", `{"name":"web","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d (%v)", status, server)
	}
	serverID, _ = server["server"].(map[string]any)["id"].(string)
	status, nic := do(t, ts, "POST", instanceZone+"/servers/"+serverID+"/private_nics",
		fmt.Sprintf(`{"private_network_id":%q}`, pnID))
	if status != http.StatusCreated {
		t.Fatalf("create NIC: status %d (%v)", status, nic)
	}
	ids, _ := nic["private_nic"].(map[string]any)["ipam_ip_ids"].([]any)
	if len(ids) != 1 {
		t.Fatalf("the NIC names %d IPAM ids, want 1", len(ids))
	}
	_, ip := do(t, ts, "GET", ipamRegion+"/ips/"+ids[0].(string), "")
	held, _ = ip["address"].(string)
	held = held[:len(held)-len("/24")]
	if status, _ := do(t, ts, "POST", instanceZone+"/servers/"+serverID+"/action", `{"action":"poweron"}`); status != http.StatusAccepted {
		t.Fatalf("poweron: status %d", status)
	}

	v4ID, _ := lbIP(t, ts, `{}`)
	status, lb := do(t, ts, "POST", lbURL+"/lbs", fmt.Sprintf(`{"name":"front","type":"LB-S","ip_ids":[%q]}`, v4ID))
	if status != http.StatusOK {
		t.Fatalf("create lb: status %d (%v)", status, lb)
	}
	lbID, _ = lb["id"].(string)
	status, backend := do(t, ts, "POST", lbURL+"/lbs/"+lbID+"/backends",
		fmt.Sprintf(`{"name":"web","forward_protocol":"tcp","forward_port":443,"server_ip":[%q,"10.63.0.250"],
		  "health_check":{"port":443,"check_delay":30000,"check_timeout":5000,"check_max_retries":2,
		                  "https_config":{"uri":"/healthz","code":200}}}`, held))
	if status != http.StatusOK {
		t.Fatalf("create backend: status %d (%v)", status, backend)
	}
	backendID, _ = backend["id"].(string)
	return ts, lbID, backendID, serverID, held
}

// TestBackendStatsNameTheServerBehindEachPoolAddress: the address a NIC holds
// names its server and that server's state; an address nobody holds names no
// instance and reads stopped; the balancer's own stats route says the same.
func TestBackendStatsNameTheServerBehindEachPoolAddress(t *testing.T) {
	ts, lbID, backendID, serverID, held := statsBench(t)

	status, out := do(t, ts, "GET", lbURL+"/lbs/"+lbID+"/backend-stats", "")
	if status != http.StatusOK {
		t.Fatalf("backend-stats: status %d (%v)", status, out)
	}
	stats, _ := out["backend_servers_stats"].([]any)
	if len(stats) != 2 || out["total_count"] != float64(2) {
		t.Fatalf("stats = %v (total %v), want the two pool addresses", stats, out["total_count"])
	}
	first, _ := stats[0].(map[string]any)
	for field, want := range map[string]any{
		"backend_id": backendID, "instance_id": serverID, "ip": held,
		"server_state": "running", "last_health_check_status": "unknown",
	} {
		if first[field] != want {
			t.Errorf("held address: %s = %v, want %v", field, first[field], want)
		}
	}
	if changed, _ := first["server_state_changed_at"].(string); changed == "" {
		t.Errorf("held address: server_state_changed_at is %v, want the server's own timestamp", first["server_state_changed_at"])
	}
	second, _ := stats[1].(map[string]any)
	for field, want := range map[string]any{
		"instance_id": "", "ip": "10.63.0.250", "server_state": "stopped", "server_state_changed_at": nil,
	} {
		if second[field] != want {
			t.Errorf("unheld address: %s = %v, want %v", field, second[field], want)
		}
	}

	status, whole := do(t, ts, "GET", lbURL+"/lbs/"+lbID+"/stats", "")
	if status != http.StatusOK {
		t.Fatalf("stats: status %d (%v)", status, whole)
	}
	if got, _ := whole["backend_servers_stats"].([]any); len(got) != 2 {
		t.Errorf("GetLBStats answers %d entries, want the same two", len(got))
	}

	// The server stops, and the stats say so on the next read: the state is
	// the control plane's, read at the moment of the question.
	if status, _ := do(t, ts, "POST", instanceZone+"/servers/"+serverID+"/action", `{"action":"poweroff"}`); status != http.StatusAccepted {
		t.Fatalf("poweroff: status %d", status)
	}
	_, out = do(t, ts, "GET", lbURL+"/lbs/"+lbID+"/backend-stats", "")
	stats, _ = out["backend_servers_stats"].([]any)
	first, _ = stats[0].(map[string]any)
	if first["server_state"] != "stopped" {
		t.Errorf("after a poweroff the held address reads %v, want stopped", first["server_state"])
	}
}

// TestAHealthNobodyMeasuredIsNeverPassed is the line: whatever the server's
// state, the health is `unknown`, because nothing here checks one. A backend
// published UP that nothing checked is the lie this emulator exists to refuse.
func TestAHealthNobodyMeasuredIsNeverPassed(t *testing.T) {
	ts, lbID, _, serverID, _ := statsBench(t)
	for _, action := range []string{"poweroff", "poweron"} {
		if status, _ := do(t, ts, "POST", instanceZone+"/servers/"+serverID+"/action", `{"action":"`+action+`"}`); status != http.StatusAccepted {
			t.Fatalf("%s: status %d", action, status)
		}
		_, out := do(t, ts, "GET", lbURL+"/lbs/"+lbID+"/backend-stats", "")
		stats, _ := out["backend_servers_stats"].([]any)
		for _, entry := range stats {
			e, _ := entry.(map[string]any)
			if e["last_health_check_status"] != "unknown" {
				t.Fatalf("after %s the health reads %v: a health nobody measured is never anything but unknown", action, e["last_health_check_status"])
			}
		}
	}
}

// The window and the filter, so a client paging or narrowing the read gets
// the same entries the whole read holds.
func TestBackendStatsAreAWindowAndTakeTheBackendFilter(t *testing.T) {
	ts, lbID, backendID, _, _ := statsBench(t)
	_, page := do(t, ts, "GET", lbURL+"/lbs/"+lbID+"/backend-stats?page=2&page_size=1", "")
	if got, _ := page["backend_servers_stats"].([]any); len(got) != 1 || page["total_count"] != float64(2) {
		t.Errorf("page 2 of size 1: %v (total %v)", got, page["total_count"])
	}
	_, mine := do(t, ts, "GET", lbURL+"/lbs/"+lbID+"/backend-stats?backend_id="+backendID, "")
	if got, _ := mine["backend_servers_stats"].([]any); len(got) != 2 {
		t.Errorf("the backend's own filter answers %d entries, want 2", len(got))
	}
	_, other := do(t, ts, "GET", lbURL+"/lbs/"+lbID+"/stats?backend_id=11111111-2222-4333-8444-555555555555", "")
	if got, _ := other["backend_servers_stats"].([]any); len(got) != 0 {
		t.Errorf("a backend the balancer does not hold answers %d entries", len(got))
	}
	if status, _ := do(t, ts, "GET", lbURL+"/lbs/11111111-2222-4333-8444-555555555555/backend-stats", ""); status != http.StatusNotFound {
		t.Errorf("a balancer that does not exist answers %d, want 404", status)
	}
}

// The two lists are empty because that is what a balancer here holds — and a
// balancer that does not exist is a 404, not an empty list: "holds nothing"
// and "is nothing" are different answers.
func TestCertificatesAndSubscribersAreTheEmptyListsABalancerHereHolds(t *testing.T) {
	ts, lbID, _, _, _ := statsBench(t)
	status, certs := do(t, ts, "GET", lbURL+"/lbs/"+lbID+"/certificates", "")
	if status != http.StatusOK {
		t.Fatalf("certificates: status %d (%v)", status, certs)
	}
	if got, _ := certs["certificates"].([]any); got == nil || len(got) != 0 || certs["total_count"] != float64(0) {
		t.Errorf("certificates = %v", certs)
	}
	if status, _ := do(t, ts, "GET", lbURL+"/lbs/11111111-2222-4333-8444-555555555555/certificates", ""); status != http.StatusNotFound {
		t.Errorf("the certificates of a balancer that does not exist answer %d, want 404", status)
	}
	status, subs := do(t, ts, "GET", lbURL+"/subscribers", "")
	if status != http.StatusOK {
		t.Fatalf("subscribers: status %d (%v)", status, subs)
	}
	if got, _ := subs["subscribers"].([]any); got == nil || len(got) != 0 || subs["total_count"] != float64(0) {
		t.Errorf("subscribers = %v", subs)
	}
}
