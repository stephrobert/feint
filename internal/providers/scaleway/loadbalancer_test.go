package scaleway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const lbURL = "/lb/v1/zones/fr-par-1"

func lbIP(t *testing.T, ts *httptest.Server, body string) (string, map[string]any) {
	t.Helper()
	status, ip := do(t, ts, "POST", lbURL+"/ips", body)
	if status != http.StatusOK {
		t.Fatalf("create lb ip: expected 200, got %d (%v)", status, ip)
	}
	id, _ := ip["id"].(string)
	return id, ip
}

// The talos chain, condensed: two IPs, the balancer, a backend with an HTTPS
// health check, a frontend with an ACL.
func TestLoadBalancerChainRoundTrips(t *testing.T) {
	ts := newTestServer(t)

	// The IP response carries no is_ipv6 field (lb_sdk.go's IP struct): the
	// provider tells the families apart by parsing ip_address, so the shape
	// to guarantee is one address per family.
	v4ID, v4 := lbIP(t, ts, `{}`)
	if addr, _ := v4["ip_address"].(string); !strings.Contains(addr, ".") {
		t.Errorf("default lb ip is not IPv4: %v", v4["ip_address"])
	}
	v6ID, v6 := lbIP(t, ts, `{"is_ipv6":true}`)
	if addr, _ := v6["ip_address"].(string); !strings.Contains(addr, ":") {
		t.Errorf("is_ipv6 minted no IPv6 address: %v", v6["ip_address"])
	}

	status, lb := do(t, ts, "POST", lbURL+"/lbs",
		fmt.Sprintf(`{"name":"controlplane","type":"LB-S","ip_ids":[%q,%q],"tags":["infra"]}`, v4ID, v6ID))
	if status != http.StatusOK {
		t.Fatalf("create lb: expected 200, got %d (%v)", status, lb)
	}
	lbID, _ := lb["id"].(string)
	// "For now API return lowercase lb type" — the provider's own comment,
	// and it upper-cases on read; answering the request's case would be the
	// invented format.
	if lb["type"] != "lb-s" {
		t.Errorf("type = %v, want the lowercase the real API answers", lb["type"])
	}
	if lb["status"] != "ready" {
		t.Errorf("status = %v; the provider's waiter polls GetLB until ready", lb["status"])
	}
	ips, _ := lb["ip"].([]any)
	if len(ips) != 2 {
		t.Fatalf("the balancer carries %d addresses, want both: %v", len(ips), lb["ip"])
	}

	status, backend := do(t, ts, "POST", lbURL+"/lbs/"+lbID+"/backends",
		`{"name":"api","forward_protocol":"tcp","forward_port":6443,
		  "server_ips":null,"server_ip":["172.16.0.11","172.16.0.12"],
		  "health_check":{"port":6443,"check_delay":30000,"check_timeout":5000,"check_max_retries":2,
		                  "https_config":{"uri":"/readyz","code":401}}}`)
	if status != http.StatusOK {
		t.Fatalf("create backend: expected 200, got %d (%v)", status, backend)
	}
	backendID, _ := backend["id"].(string)
	pool, _ := backend["pool"].([]any)
	if len(pool) != 2 {
		t.Errorf("pool = %v, want the two servers", backend["pool"])
	}
	hc, _ := backend["health_check"].(map[string]any)
	if hc == nil {
		t.Fatalf("no health check came back: %v", backend)
	}
	if hc["check_delay"] != float64(30000) || hc["check_timeout"] != float64(5000) {
		t.Errorf("the millisecond timeouts did not round-trip: %v", hc)
	}
	https, _ := hc["https_config"].(map[string]any)
	if https == nil || https["uri"] != "/readyz" || https["code"] != float64(401) {
		t.Errorf("https_config did not round-trip: %v", hc["https_config"])
	}
	inner, _ := backend["lb"].(map[string]any)
	if inner == nil || inner["id"] != lbID {
		t.Errorf("the backend does not embed its balancer; the provider reads backend.LB.ID")
	}

	status, frontend := do(t, ts, "POST", lbURL+"/lbs/"+lbID+"/frontends",
		fmt.Sprintf(`{"name":"api","inbound_port":6443,"backend_id":%q}`, backendID))
	if status != http.StatusOK {
		t.Fatalf("create frontend: expected 200, got %d (%v)", status, frontend)
	}
	frontendID, _ := frontend["id"].(string)

	status, acl := do(t, ts, "POST", lbURL+"/frontends/"+frontendID+"/acls",
		`{"name":"Deny all","action":{"type":"deny"},"match":{"ip_subnet":["0.0.0.0/0","::/0"]},"index":1}`)
	if status != http.StatusOK {
		t.Fatalf("create acl: expected 200, got %d (%v)", status, acl)
	}
	match, _ := acl["match"].(map[string]any)
	if match == nil || match["http_filter"] != "acl_http_filter_none" {
		t.Errorf("the match does not carry the SDK's complete shape: %v", acl["match"])
	}

	// The counters a read publishes follow the children.
	status, lb = do(t, ts, "GET", lbURL+"/lbs/"+lbID, "")
	if status != http.StatusOK {
		t.Fatalf("get lb: %d", status)
	}
	if lb["backend_count"] != float64(1) || lb["frontend_count"] != float64(1) {
		t.Errorf("counts = %v/%v, want 1/1", lb["backend_count"], lb["frontend_count"])
	}
}

// The attachment without ipam_ids books an address from the network's own
// pool, and the provider reads it back through /ipam/v1 filtered by
// resource_type=lb_server (its services/lb/helpers_lb.go, getLBPrivateIPs).
func TestAnAttachedBalancerIsResolvableThroughIPAM(t *testing.T) {
	ts := newTestServer(t)
	v4ID, _ := lbIP(t, ts, `{}`)
	status, lb := do(t, ts, "POST", lbURL+"/lbs",
		fmt.Sprintf(`{"name":"lb","type":"LB-S","ip_ids":[%q]}`, v4ID))
	if status != http.StatusOK {
		t.Fatalf("create lb: %d", status)
	}
	lbID, _ := lb["id"].(string)
	pnID, _ := privateNetwork(t, ts, `{"name":"main","subnets":["172.16.0.0/22"]}`)

	status, attachment := do(t, ts, "POST", lbURL+"/lbs/"+lbID+"/attach-private-network",
		fmt.Sprintf(`{"private_network_id":%q,"ipam_ids":[]}`, pnID))
	if status != http.StatusOK {
		t.Fatalf("attach: expected 200, got %d (%v)", status, attachment)
	}
	if attachment["status"] != "ready" {
		t.Errorf("attachment status = %v; the provider's waiter polls until ready", attachment["status"])
	}
	ipamIDs, _ := attachment["ipam_ids"].([]any)
	if len(ipamIDs) != 1 {
		t.Fatalf("the attachment names %d addresses, want the booked one: %v", len(ipamIDs), attachment)
	}

	status, list := do(t, ts, "GET",
		"/ipam/v1/regions/fr-par/ips?resource_id="+lbID+"&resource_type=lb_server&private_network_id="+pnID, "")
	if status != http.StatusOK {
		t.Fatalf("list ipam ips: %d", status)
	}
	ips, _ := list["ips"].([]any)
	if len(ips) != 1 {
		t.Fatalf("ipam answered %d addresses for the balancer, want 1 (%v)", len(ips), list)
	}

	// Detach releases the address the attach created: the pool must not leak.
	status, _ = do(t, ts, "POST", lbURL+"/lbs/"+lbID+"/detach-private-network",
		fmt.Sprintf(`{"private_network_id":%q}`, pnID))
	if status != http.StatusNoContent {
		t.Fatalf("detach: expected 204, got %d", status)
	}
	status, list = do(t, ts, "GET",
		"/ipam/v1/regions/fr-par/ips?resource_id="+lbID+"&resource_type=lb_server", "")
	if status != http.StatusOK {
		t.Fatalf("list after detach: %d", status)
	}
	if ips, _ := list["ips"].([]any); len(ips) != 0 {
		t.Errorf("the balancer still holds %d addresses after the detach", len(ips))
	}
}

// Deleting the balancer without release_ip detaches its addresses; kubic's
// whole demand is that the lb ip survives whatever consumed it.
func TestDeletingABalancerKeepsItsAddressUnlessReleased(t *testing.T) {
	ts := newTestServer(t)
	v4ID, _ := lbIP(t, ts, `{}`)
	status, lb := do(t, ts, "POST", lbURL+"/lbs",
		fmt.Sprintf(`{"name":"lb","type":"LB-S","ip_ids":[%q]}`, v4ID))
	if status != http.StatusOK {
		t.Fatalf("create lb: %d", status)
	}
	lbID, _ := lb["id"].(string)

	status, _ = do(t, ts, "DELETE", lbURL+"/lbs/"+lbID+"?release_ip=false", "")
	if status != http.StatusNoContent {
		t.Fatalf("delete lb: expected 204, got %d", status)
	}
	status, ip := do(t, ts, "GET", lbURL+"/ips/"+v4ID, "")
	if status != http.StatusOK {
		t.Fatalf("the address died with the balancer despite release_ip=false")
	}
	if ip["lb_id"] != nil {
		t.Errorf("the surviving address still names the dead balancer: %v", ip["lb_id"])
	}
	// And a released address really is free to take again.
	status, _ = do(t, ts, "DELETE", lbURL+"/ips/"+v4ID, "")
	if status != http.StatusNoContent {
		t.Errorf("release of a detached address: expected 204, got %d", status)
	}
}

// A backend a frontend still forwards to must not vanish under it: the wrong
// destroy order gets a retryable refusal, exactly as the volume and IPAM
// paths answer.
func TestDeletingAUsedBackendIsRefused(t *testing.T) {
	ts := newTestServer(t)
	v4ID, _ := lbIP(t, ts, `{}`)
	status, lb := do(t, ts, "POST", lbURL+"/lbs",
		fmt.Sprintf(`{"name":"lb","type":"LB-S","ip_ids":[%q]}`, v4ID))
	if status != http.StatusOK {
		t.Fatalf("create lb: %d", status)
	}
	lbID, _ := lb["id"].(string)
	status, backend := do(t, ts, "POST", lbURL+"/lbs/"+lbID+"/backends",
		`{"name":"b","forward_protocol":"http","forward_port":80,
		  "health_check":{"port":80,"http_config":{"uri":"/healthz"}}}`)
	if status != http.StatusOK {
		t.Fatalf("create backend: %d", status)
	}
	backendID, _ := backend["id"].(string)
	status, frontend := do(t, ts, "POST", lbURL+"/lbs/"+lbID+"/frontends",
		fmt.Sprintf(`{"name":"f","inbound_port":80,"backend_id":%q}`, backendID))
	if status != http.StatusOK {
		t.Fatalf("create frontend: %d", status)
	}
	frontendID, _ := frontend["id"].(string)

	status, refused := do(t, ts, "DELETE", lbURL+"/backends/"+backendID, "")
	if status != http.StatusBadRequest || refused["type"] != "precondition_failed" {
		t.Fatalf("deleting a used backend answered %d/%v, want 400 precondition_failed", status, refused["type"])
	}
	status, _ = do(t, ts, "DELETE", lbURL+"/frontends/"+frontendID, "")
	if status != http.StatusNoContent {
		t.Fatalf("delete frontend: %d", status)
	}
	status, _ = do(t, ts, "DELETE", lbURL+"/backends/"+backendID, "")
	if status != http.StatusNoContent {
		t.Errorf("delete backend after its frontend: expected 204, got %d", status)
	}
}

// The route family, for the official LB module's scaleway_lb_route.
func TestARouteRoundTrips(t *testing.T) {
	ts := newTestServer(t)
	v4ID, _ := lbIP(t, ts, `{}`)
	status, lb := do(t, ts, "POST", lbURL+"/lbs",
		fmt.Sprintf(`{"name":"lb","type":"LB-S","ip_ids":[%q]}`, v4ID))
	if status != http.StatusOK {
		t.Fatalf("create lb: %d", status)
	}
	lbID, _ := lb["id"].(string)
	status, backend := do(t, ts, "POST", lbURL+"/lbs/"+lbID+"/backends",
		`{"name":"b","forward_protocol":"http","forward_port":80,
		  "health_check":{"port":80,"http_config":{"uri":"/"}}}`)
	if status != http.StatusOK {
		t.Fatalf("create backend: %d", status)
	}
	backendID, _ := backend["id"].(string)
	status, frontend := do(t, ts, "POST", lbURL+"/lbs/"+lbID+"/frontends",
		fmt.Sprintf(`{"name":"f","inbound_port":80,"backend_id":%q}`, backendID))
	if status != http.StatusOK {
		t.Fatalf("create frontend: %d", status)
	}
	frontendID, _ := frontend["id"].(string)

	status, route := do(t, ts, "POST", lbURL+"/routes",
		fmt.Sprintf(`{"frontend_id":%q,"backend_id":%q,"match":{"host_header":"app.example.org"}}`, frontendID, backendID))
	if status != http.StatusOK {
		t.Fatalf("create route: expected 200, got %d (%v)", status, route)
	}
	routeID, _ := route["id"].(string)
	match, _ := route["match"].(map[string]any)
	if match == nil || match["host_header"] != "app.example.org" || match["match_subdomains"] != false {
		t.Errorf("the route's match did not round-trip in the SDK's shape: %v", route["match"])
	}
	status, again := do(t, ts, "GET", lbURL+"/routes/"+routeID, "")
	if status != http.StatusOK {
		t.Fatalf("get route: %d", status)
	}
	if fmt.Sprint(again["match"]) != fmt.Sprint(route["match"]) {
		t.Errorf("create and read disagree: %v vs %v", route["match"], again["match"])
	}
}

// Listing the routes of a frontend that does not exist is a refusal, and the
// real cloud says so: `scw lb route list frontend-id=<absent>` answered 404
// "frontend not Found" on 2026-08-21, recorded in
// corpus/scaleway/scw-refusals.jsonl. This pack answered 200 with an empty
// list, which a client reads as "that frontend carries no route".
//
// The test is the one named by the comment in listLBRoutes, and it fails
// without it: the filter alone leaves an unknown identifier matching nothing
// and the page comes back empty with a 200.
func TestListingRoutesOfAnAbsentFrontendIsRefused(t *testing.T) {
	ts := newTestServer(t)

	status, body := do(t, ts, "GET", lbURL+"/routes?frontend_id=11111111-2222-4333-8444-555555555555", "")
	if status != http.StatusNotFound {
		t.Fatalf("listing the routes of an absent frontend answered %d (%v), want 404", status, body)
	}
	// And the unfiltered listing still answers, so the refusal is about the
	// identifier and not about the route family being unreadable.
	if status, body := do(t, ts, "GET", lbURL+"/routes", ""); status != http.StatusOK {
		t.Fatalf("listing every route answered %d (%v), want 200", status, body)
	}
}
