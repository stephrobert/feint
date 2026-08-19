package scaleway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
)

const gwURL = "/vpc-gw/v2/zones/fr-par-1"

// gatewayChain builds the exact path terraform-talos and the official VPC
// module walk: an IP, a gateway on it, a private network, the connection.
func gatewayChain(t *testing.T, ts *httptest.Server) (ipID, gatewayID, pnID, gnID string) {
	t.Helper()
	status, ip := do(t, ts, "POST", gwURL+"/ips", `{"tags":["infra"]}`)
	if status != http.StatusOK {
		t.Fatalf("create gateway ip: expected 200, got %d (%v)", status, ip)
	}
	ipID, _ = ip["id"].(string)

	status, gw := do(t, ts, "POST", gwURL+"/gateways",
		fmt.Sprintf(`{"name":"main","type":"VPC-GW-S","ip_id":%q,"tags":["infra"]}`, ipID))
	if status != http.StatusOK {
		t.Fatalf("create gateway: expected 200, got %d (%v)", status, gw)
	}
	gatewayID, _ = gw["id"].(string)

	pnID, _ = privateNetwork(t, ts, `{"name":"main","subnets":["172.16.0.0/22"]}`)

	status, gn := do(t, ts, "POST", gwURL+"/gateway-networks",
		fmt.Sprintf(`{"gateway_id":%q,"private_network_id":%q,"enable_masquerade":true,"push_default_route":true}`, gatewayID, pnID))
	if status != http.StatusOK {
		t.Fatalf("create gateway network: expected 200, got %d (%v)", status, gn)
	}
	gnID, _ = gn["id"].(string)
	return ipID, gatewayID, pnID, gnID
}

// The talos shape: IP → gateway → connection, then read everything back the
// way the provider does after an apply.
func TestGatewayChainRoundTrips(t *testing.T) {
	ts := newTestServer(t)
	ipID, gatewayID, _, gnID := gatewayChain(t, ts)

	status, gw := do(t, ts, "GET", gwURL+"/gateways/"+gatewayID, "")
	if status != http.StatusOK {
		t.Fatalf("get gateway: expected 200, got %d", status)
	}
	if gw["status"] != "running" {
		t.Errorf("gateway status = %v; the provider's waiter polls GetGateway until a terminal status", gw["status"])
	}
	ipv4, _ := gw["ipv4"].(map[string]any)
	if ipv4 == nil || ipv4["id"] != ipID {
		t.Errorf("the gateway does not carry its IP back: %v", gw["ipv4"])
	}
	if gw["type"] != "VPC-GW-S" {
		t.Errorf("type = %v, want the offer as sent", gw["type"])
	}

	status, gn := do(t, ts, "GET", gwURL+"/gateway-networks/"+gnID, "")
	if status != http.StatusOK {
		t.Fatalf("get gateway network: expected 200, got %d", status)
	}
	if gn["status"] != "ready" {
		t.Errorf("gateway network status = %v, want ready", gn["status"])
	}
	if gn["mac_address"] == "" || gn["mac_address"] == nil {
		t.Errorf("the connection publishes no MAC; the provider reads it back")
	}
	if gn["ipam_ip_id"] == "" || gn["ipam_ip_id"] == nil {
		t.Errorf("the connection names no IPAM address: %v", gn)
	}

	// The gateway lists its connections inline, the shape the SDK's Gateway
	// struct declares.
	status, gw = do(t, ts, "GET", gwURL+"/gateways/"+gatewayID, "")
	if status != http.StatusOK {
		t.Fatalf("re-get gateway: expected 200, got %d", status)
	}
	networks, _ := gw["gateway_networks"].([]any)
	if len(networks) != 1 {
		t.Errorf("gateway_networks = %v, want the one connection", gw["gateway_networks"])
	}
}

// The provider reads the connection's private address through /ipam/v1 ListIPs
// filtered by resource_id + resource_type (its services/vpcgw/helpers.go,
// setPrivateIPs). An emulator that books the address but cannot answer that
// filter serves half the product.
func TestAGatewayConnectionIsResolvableThroughIPAM(t *testing.T) {
	ts := newTestServer(t)
	_, _, pnID, gnID := gatewayChain(t, ts)

	status, list := do(t, ts, "GET",
		"/ipam/v1/regions/fr-par/ips?resource_id="+gnID+"&resource_type=vpc_gateway_network&private_network_id="+pnID, "")
	if status != http.StatusOK {
		t.Fatalf("list ipam ips: expected 200, got %d", status)
	}
	ips, _ := list["ips"].([]any)
	if len(ips) != 1 {
		t.Fatalf("ipam answered %d addresses for the connection, want 1 (%v)", len(ips), list)
	}
	ip, _ := ips[0].(map[string]any)
	held, _ := ip["resource"].(map[string]any)
	if held == nil || held["type"] != "vpc_gateway_network" || held["id"] != gnID {
		t.Errorf("the address does not name its holder: %v", ip["resource"])
	}
}

// The official VPC module books the address first (scaleway_ipam_ip) and
// passes it as ipam_ip_id; the connection must use exactly that address, and
// deleting the connection must give the booked address back unheld rather
// than deleting it.
func TestABookedAddressRidesTheConnectionAndSurvivesIt(t *testing.T) {
	ts := newTestServer(t)

	status, ip := do(t, ts, "POST", gwURL+"/ips", `{}`)
	if status != http.StatusOK {
		t.Fatalf("create gateway ip: %d", status)
	}
	ipID, _ := ip["id"].(string)
	status, gw := do(t, ts, "POST", gwURL+"/gateways",
		fmt.Sprintf(`{"name":"m","type":"VPC-GW-S","ip_id":%q}`, ipID))
	if status != http.StatusOK {
		t.Fatalf("create gateway: %d", status)
	}
	gatewayID, _ := gw["id"].(string)
	pnID, _ := privateNetwork(t, ts, `{"name":"m","subnets":["172.16.0.0/22"]}`)

	status, booked := do(t, ts, "POST", "/ipam/v1/regions/fr-par/ips",
		fmt.Sprintf(`{"source":{"private_network_id":%q}}`, pnID))
	if status != http.StatusCreated {
		t.Fatalf("book ip: expected 201, got %d (%v)", status, booked)
	}
	bookedID, _ := booked["id"].(string)

	status, gn := do(t, ts, "POST", gwURL+"/gateway-networks",
		fmt.Sprintf(`{"gateway_id":%q,"private_network_id":%q,"enable_masquerade":true,"push_default_route":true,"ipam_ip_id":%q}`, gatewayID, pnID, bookedID))
	if status != http.StatusOK {
		t.Fatalf("create gateway network: expected 200, got %d (%v)", status, gn)
	}
	if gn["ipam_ip_id"] != bookedID {
		t.Errorf("the connection answers ipam_ip_id %v, want the booked %s", gn["ipam_ip_id"], bookedID)
	}
	gnID, _ := gn["id"].(string)

	status, _ = do(t, ts, "DELETE", gwURL+"/gateway-networks/"+gnID, "")
	if status != http.StatusOK {
		t.Fatalf("delete gateway network: expected 200 with the detaching view, got %d", status)
	}
	status, after := do(t, ts, "GET", "/ipam/v1/regions/fr-par/ips/"+bookedID, "")
	if status != http.StatusOK {
		t.Fatalf("the booked address died with the connection; BookIP owns its lifetime")
	}
	if after["resource"] != nil {
		t.Errorf("the booked address still names a holder after the detach: %v", after["resource"])
	}
}

// Deleting a gateway that still carries a connection must be refused: the
// Terraform provider always deletes the GatewayNetworks first, and a client
// that did not gets a retryable refusal instead of dangling connections.
func TestDeletingAConnectedGatewayIsRefused(t *testing.T) {
	ts := newTestServer(t)
	_, gatewayID, _, gnID := gatewayChain(t, ts)

	status, body := do(t, ts, "DELETE", gwURL+"/gateways/"+gatewayID, "")
	if status != http.StatusBadRequest || body["type"] != "precondition_failed" {
		t.Fatalf("deleting a connected gateway answered %d/%v, want 400 precondition_failed", status, body["type"])
	}

	status, _ = do(t, ts, "DELETE", gwURL+"/gateway-networks/"+gnID, "")
	if status != http.StatusOK {
		t.Fatalf("delete gateway network: %d", status)
	}
	status, _ = do(t, ts, "DELETE", gwURL+"/gateways/"+gatewayID+"?delete_ip=true", "")
	if status != http.StatusOK {
		t.Fatalf("delete gateway after detach: expected 200 with the deleting view, got %d", status)
	}
	status, _ = do(t, ts, "GET", gwURL+"/gateways/"+gatewayID, "")
	if status != http.StatusNotFound {
		t.Errorf("a deleted gateway still answers %d; the provider polls GetGateway until 404", status)
	}
}

// An offer outside the catalogue is refused, the #279 lesson applied to this
// product: the real API refuses an unknown type, and an emulator that accepts
// one lets a plan pass that production would refuse.
func TestAnUnknownGatewayTypeIsRefused(t *testing.T) {
	ts := newTestServer(t)
	status, body := do(t, ts, "POST", gwURL+"/gateways", `{"name":"x","type":"VPC-GW-QUANTUM"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("an unknown offer answered %d, want 400 (%v)", status, body)
	}
}
