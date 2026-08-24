package scaleway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/scaleway"
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
	// 200, not the 201 a create writes by habit: measured on the wire against a
	// real fr-par account on 2026-08-24, see ipamBookStatus.
	if status != http.StatusOK {
		t.Fatalf("book ip: expected 200, got %d (%v)", status, booked)
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

// TestAGatewayAddressAnswersAReverseOfTheRecordedType holds the type a real
// fr-par account answers on `reverse`, and holds it on the create as well as on
// the read.
//
// The value is not asserted and cannot be: the recording carries a reverse the
// sanitiser replaced, so what was measured is that the cloud answers a *string*
// there and never null. An invented hostname would be the fabricated format
// this repository refuses; the empty string is what the sibling lb IP has
// answered all along.
func TestAGatewayAddressAnswersAReverseOfTheRecordedType(t *testing.T) {
	ts := newTestServer(t)
	status, ip := do(t, ts, "POST", gwURL+"/ips", `{}`)
	if status != http.StatusOK {
		t.Fatalf("create gateway ip: expected 200, got %d (%v)", status, ip)
	}
	id, _ := ip["id"].(string)
	if _, isString := ip["reverse"].(string); !isString {
		t.Errorf("a created address answers reverse = %v (%T), want a string as the recorded cloud answers",
			ip["reverse"], ip["reverse"])
	}

	status, read := do(t, ts, "GET", gwURL+"/ips/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get gateway ip: expected 200, got %d (%v)", status, read)
	}
	if _, isString := read["reverse"].(string); !isString {
		t.Errorf("a re-read address answers reverse = %v (%T), want a string", read["reverse"], read["reverse"])
	}

	// A client that sets one gets it back, and one that clears it still gets a
	// string: the field never becomes null again.
	status, updated := do(t, ts, "PATCH", gwURL+"/ips/"+id, `{"reverse":"gw.example.invalid"}`)
	if status != http.StatusOK {
		t.Fatalf("update gateway ip: expected 200, got %d (%v)", status, updated)
	}
	if updated["reverse"] != "gw.example.invalid" {
		t.Errorf("reverse = %v, want the name the client set", updated["reverse"])
	}
	status, cleared := do(t, ts, "PATCH", gwURL+"/ips/"+id, `{"reverse":""}`)
	if status != http.StatusOK {
		t.Fatalf("clear reverse: expected 200, got %d (%v)", status, cleared)
	}
	if got, isString := cleared["reverse"].(string); !isString || got != "" {
		t.Errorf("a cleared reverse = %v (%T), want the empty string and not null", cleared["reverse"], cleared["reverse"])
	}

	// The list answers the same shape: it is the door the recording caught the
	// null on, because a create was compared against a body the list built.
	status, page := do(t, ts, "GET", gwURL+"/ips", "")
	if status != http.StatusOK {
		t.Fatalf("list gateway ips: expected 200, got %d (%v)", status, page)
	}
	ips, _ := page["ips"].([]any)
	if len(ips) != 1 {
		t.Fatalf("the list answers %d address(es), want the one created: %v", len(ips), page)
	}
	if _, isString := ips[0].(map[string]any)["reverse"].(string); !isString {
		t.Errorf("a listed address answers a reverse that is not a string: %v", ips[0])
	}
}

// TestTheGatewaySoftwareVersionIsDeclinedRatherThanInvented holds the decision
// at both ends, because either end alone is a comment.
//
// A key present with a null value cannot be excused: a field decline answers
// for a field the response does not carry, so `"version": nil` produced a type
// divergence no decline could reach, and the emulator was stating "no version"
// in a shape the cloud never answers. So the key is absent AND the decline says
// why — assert one without the other and a future edit satisfies the test while
// undoing the decision.
func TestTheGatewaySoftwareVersionIsDeclinedRatherThanInvented(t *testing.T) {
	ts := newTestServer(t)
	_, gatewayID, _, _ := gatewayChain(t, ts)
	status, gw := do(t, ts, "GET", gwURL+"/gateways/"+gatewayID, "")
	if status != http.StatusOK {
		t.Fatalf("get gateway: expected 200, got %d (%v)", status, gw)
	}
	if _, present := gw["version"]; present {
		t.Errorf("the gateway answers version = %v; nothing runs here, so the key is absent and DeclinedFields says why",
			gw["version"])
	}
	if _, present := gw["bastion_allowed_ips"]; !present {
		t.Error("bastion_allowed_ips is absent; the array is served empty, only its elements are declined")
	}

	declines := emulator.FieldDeclinesOf(scaleway.New(emulator.DefaultEnv()))
	for _, want := range []struct{ operation, path string }{
		{"vpcgw/v2/API.GetGateway", "version"},
		{"vpcgw/v2/API.CreateGateway", "version"},
		{"vpcgw/v2/API.GetGateway", "bastion_allowed_ips[]"},
		{"vpcgw/v2/API.CreateGateway", "bastion_allowed_ips[]"},
	} {
		found := false
		for _, d := range declines {
			if d.Matches(want.operation, want.path) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no field decline covers %s on %s: the replay reads the omission as a divergence",
				want.path, want.operation)
		}
	}
}
