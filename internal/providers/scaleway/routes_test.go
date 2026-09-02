package scaleway_test

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"github.com/stephrobert/feint/internal/core/machine"
)

// The rest of the VPC surface SW-4 serves: subnets flat, the enable family,
// and the routes. The reference client for the routes is the Terraform
// provider's services/vpc/route.go: CreateRoute, GetRoute, UpdateRoute,
// DeleteRoute, with ListRoutesWithNexthop behind the data sources.

func createVPC(t *testing.T, ts *httptest.Server, body string) map[string]any {
	t.Helper()
	status, out := do(t, ts, "POST", vpcRegion+"/vpcs", body)
	if status != http.StatusOK {
		t.Fatalf("create vpc: status %d (%v)", status, out)
	}
	return out
}

func TestListSubnetsServesTheSameSubnetAsTheNetwork(t *testing.T) {
	ts := newTestServer(t)
	pnID, subnetID := createPN(t, ts, "10.80.0.0/24")

	status, body := do(t, ts, "GET", vpcRegion+"/subnets", "")
	if status != http.StatusOK {
		t.Fatalf("list subnets: status %d", status)
	}
	subnets, _ := body["subnets"].([]any)
	// One record per family: the flat door serves the same dual-stack pair the
	// network embeds (#270).
	if len(subnets) != 2 {
		t.Fatalf("%d subnets, want 2", len(subnets))
	}
	subnet := ipv4SubnetOf(t, subnets)
	// Two doors, one record: the flat listing and the network's own embed must
	// agree on the id, because the Terraform provider joins on it.
	if subnet["id"] != subnetID || subnet["private_network_id"] != pnID {
		t.Fatalf("the flat subnet disagrees with the network's: %v", subnet)
	}
	if subnet["subnet"] != "10.80.0.0/24" {
		t.Fatalf("subnet block is %v", subnet["subnet"])
	}

	// The filters the SDK sends.
	status, body = do(t, ts, "GET", vpcRegion+"/subnets?subnet_ids="+subnetID, "")
	if status != http.StatusOK {
		t.Fatalf("filtered list: status %d", status)
	}
	if subnets, _ := body["subnets"].([]any); len(subnets) != 1 {
		t.Fatalf("subnet_ids filter returned %d", len(subnets))
	}
	status, body = do(t, ts, "GET", vpcRegion+"/subnets?vpc_id=00000000-dead-4000-8000-000000000000", "")
	if status != http.StatusOK {
		t.Fatalf("vpc filter: status %d", status)
	}
	if subnets, _ := body["subnets"].([]any); len(subnets) != 0 {
		t.Fatalf("a foreign vpc_id matched %d subnets", len(subnets))
	}
}

// peeringRuntime is a fakeRuntime that also answers Peerer, recording what each
// network was asked to reach — the way the OVN driver is driven.
type peeringRuntime struct {
	*fakeRuntime
	mu    sync.Mutex
	peers map[string][]string
}

func newPeeringRuntime() *peeringRuntime {
	rt := &peeringRuntime{fakeRuntime: newFakeRuntime(), peers: map[string][]string{}}
	close(rt.release) // nothing here needs to block
	return rt
}

func (r *peeringRuntime) NativeIsolation() bool { return true }

func (r *peeringRuntime) PeerNetworks(_ context.Context, network string, peers []string) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.peers[network] = append([]string(nil), peers...)
	return nil
}

func (r *peeringRuntime) peersOf(network string) []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.peers[network]...)
}

// EnableRouting is behaviour, not a stored flag: the reachability between the
// VPC's networks, which the machine driver enforces, reconciles when it flips.
func TestEnableRoutingReconcilesThePeering(t *testing.T) {
	rt := newPeeringRuntime()
	ts, _ := newAddressTestServer(t, machine.Use(rt))

	vpc := createVPC(t, ts, `{"name":"routed","enable_routing":false}`)
	vpcID, _ := vpc["id"].(string)
	if enabled, _ := vpc["routing_enabled"].(bool); enabled {
		t.Fatalf("a VPC created with enable_routing=false says routing_enabled=true")
	}

	networks := make([]string, 0, 2)
	for i, block := range []string{"10.81.0.0/24", "10.81.1.0/24"} {
		status, pn := do(t, ts, "POST", vpcRegion+"/private-networks",
			fmt.Sprintf(`{"name":"member-%d","vpc_id":%q,"subnets":[%q]}`, i, vpcID, block))
		if status != http.StatusOK {
			t.Fatalf("create pn %d: status %d (%v)", i, status, pn)
		}
		id, _ := pn["id"].(string)
		networks = append(networks, id)
	}

	// Routing off: the driver was told each network reaches nothing.
	for _, id := range networks {
		name := machine.NetworkName(machine.NetworkPrefix, id)
		if peers := rt.peersOf(name); len(peers) != 0 {
			t.Fatalf("network %s of a non-routing VPC was peered with %v", name, peers)
		}
	}

	status, enabled := do(t, ts, "POST", vpcRegion+"/vpcs/"+vpcID+"/enable-routing", "{}")
	if status != http.StatusOK {
		t.Fatalf("enable-routing: status %d (%v)", status, enabled)
	}
	if flag, _ := enabled["routing_enabled"].(bool); !flag {
		t.Fatalf("enable-routing did not flip the flag: %v", enabled)
	}

	// Routing on: each network now reaches its sibling, because the flag is
	// read by the reconciliation, not merely stored.
	first := machine.NetworkName(machine.NetworkPrefix, networks[0])
	second := machine.NetworkName(machine.NetworkPrefix, networks[1])
	if peers := rt.peersOf(first); !contains(peers, second) {
		t.Fatalf("after enable-routing, %s does not reach %s (peers: %v)", first, second, peers)
	}
	if peers := rt.peersOf(second); !contains(peers, first) {
		t.Fatalf("after enable-routing, %s does not reach %s (peers: %v)", second, first, peers)
	}
}

func TestARouteRoundTripsThroughItsLifecycle(t *testing.T) {
	ts := newTestServer(t)

	vpc := createVPC(t, ts, `{"name":"with-routes","enable_routing":true}`)
	vpcID, _ := vpc["id"].(string)
	status, pn := do(t, ts, "POST", vpcRegion+"/private-networks",
		fmt.Sprintf(`{"name":"nexthop-net","vpc_id":%q,"subnets":["10.82.0.0/24"]}`, vpcID))
	if status != http.StatusOK {
		t.Fatalf("create pn: status %d", status)
	}
	pnID, _ := pn["id"].(string)

	status, created := do(t, ts, "POST", vpcRegion+"/routes",
		fmt.Sprintf(`{"vpc_id":%q,"description":"to the lab","destination":"192.168.42.0/24","nexthop_private_network_id":%q}`, vpcID, pnID))
	// 200, like every vpc/v2 create: measured on the wire against a real fr-par
	// account on 2026-08-24, see vpcCreateStatus.
	if status != http.StatusOK {
		t.Fatalf("create route: status %d (%v)", status, created)
	}
	routeID, _ := created["id"].(string)
	if created["is_read_only"] != false || created["type"] != "custom" {
		t.Fatalf("a client route must be writable and custom: %v", created)
	}

	status, got := do(t, ts, "GET", vpcRegion+"/routes/"+routeID, "")
	if status != http.StatusOK {
		t.Fatalf("get route: status %d", status)
	}
	for _, key := range []string{"destination", "description", "nexthop_private_network_id", "vpc_id"} {
		if fmt.Sprint(got[key]) != fmt.Sprint(created[key]) {
			t.Fatalf("%s changed between create and get: %v then %v", key, created[key], got[key])
		}
	}

	status, updated := do(t, ts, "PATCH", vpcRegion+"/routes/"+routeID, `{"description":"renamed","tags":["a"]}`)
	if status != http.StatusOK {
		t.Fatalf("update route: status %d", status)
	}
	if updated["description"] != "renamed" {
		t.Fatalf("description did not update: %v", updated)
	}

	// The nexthop network cannot vanish under the route.
	status, _ = do(t, ts, "DELETE", vpcRegion+"/private-networks/"+pnID, "")
	if status != http.StatusBadRequest {
		t.Fatalf("deleting the nexthop network answered %d, want a refusal", status)
	}
	// Nor the VPC under its routes.
	status, _ = do(t, ts, "DELETE", vpcRegion+"/vpcs/"+vpcID, "")
	if status != http.StatusBadRequest {
		t.Fatalf("deleting the VPC with a live route answered %d, want a refusal", status)
	}

	if status, _ := do(t, ts, "DELETE", vpcRegion+"/routes/"+routeID, ""); status != http.StatusNoContent {
		t.Fatalf("delete route: status %d", status)
	}
	if status, _ := do(t, ts, "GET", vpcRegion+"/routes/"+routeID, ""); status != http.StatusNotFound {
		t.Fatalf("a deleted route still answers")
	}
}

func TestARouteRefusesWhatCannotExistHere(t *testing.T) {
	ts := newTestServer(t)
	vpc := createVPC(t, ts, `{"name":"strict"}`)
	vpcID, _ := vpc["id"].(string)

	// A connector nexthop names a product this pack declines.
	status, _ := do(t, ts, "POST", vpcRegion+"/routes",
		fmt.Sprintf(`{"vpc_id":%q,"destination":"10.9.0.0/24","nexthop_vpc_connector_id":"11111111-1111-4111-8111-111111111111"}`, vpcID))
	if status != http.StatusBadRequest {
		t.Fatalf("a connector nexthop was accepted: status %d", status)
	}

	// A destination with host bits is not a block.
	status, _ = do(t, ts, "POST", vpcRegion+"/routes",
		fmt.Sprintf(`{"vpc_id":%q,"destination":"10.9.0.1/24"}`, vpcID))
	if status != http.StatusBadRequest {
		t.Fatalf("host bits in the destination were accepted: status %d", status)
	}

	// A nexthop network of another VPC is not this VPC's route.
	otherPN, _ := createPN(t, ts, "10.83.0.0/24")
	status, _ = do(t, ts, "POST", vpcRegion+"/routes",
		fmt.Sprintf(`{"vpc_id":%q,"destination":"10.9.0.0/24","nexthop_private_network_id":%q}`, vpcID, otherPN))
	if status != http.StatusNotFound {
		t.Fatalf("a foreign nexthop network was accepted: status %d", status)
	}

	// An unknown VPC is not found, in the SDK's shape — and the body has to be
	// valid for the request to reach that wall at all. This case used to send no
	// next hop and expect 404, which is the very thing #394 measured as wrong:
	// fr-par answers 400 there, about the body, and 404 only once the body
	// satisfies the constraints its document declares.
	status, _ = do(t, ts, "POST", vpcRegion+"/routes",
		fmt.Sprintf(`{"vpc_id":"00000000-dead-4000-8000-000000000000","destination":"10.9.0.0/24","nexthop_private_network_id":%q}`, otherPN))
	if status != http.StatusNotFound {
		t.Fatalf("an unknown VPC answered %d", status)
	}
}

// The order the two refusals come in, measured against fr-par on 2026-09-02
// (#394). A client branches on the status: 404 says "create the parent first",
// 400 says "your body is wrong and the parent is beside the point", and this
// emulator answered 404 to both.
func TestCreateRouteChecksWhatTheDocumentDeclaresBeforeWhatTheStoreHolds(t *testing.T) {
	ts := newTestServer(t)
	const ghost = "00000000-dead-4000-8000-000000000000"

	// A ghost VPC and no next hop: the cloud answers about the next hop, not
	// about the VPC it was never asked to resolve.
	status, body := do(t, ts, "POST", vpcRegion+"/routes",
		`{"vpc_id":"`+ghost+`","destination":"10.9.0.0/24"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a body missing its next hop answered %d, want 400 (%v)", status, body)
	}
	details, _ := body["details"].([]any)
	if len(details) != 1 {
		t.Fatalf("details holds %d entries, want 1 (%v)", len(details), body)
	}
	d, _ := details[0].(map[string]any)
	if d["argument_name"] != "route" {
		t.Errorf("argument_name is %v, want route: the constraint is on the object, not on a field", d["argument_name"])
	}
	// The help_message is a JSON fragment inside a string, which is not a shape
	// anyone would invent — the cloud's generated layer renders it that way.
	if help, _ := d["help_message"].(string); !strings.Contains(help, "requires_one_of") ||
		!strings.Contains(help, "nexthop_private_network_id") {
		t.Errorf("help_message is %q, want the requires_one_of the cloud answers", help)
	}

	// No vpc_id at all: the field-level constraint comes before the object-level
	// one, so this answers about vpc_id rather than about the next hop.
	status, body = do(t, ts, "POST", vpcRegion+"/routes", `{"destination":"10.9.0.0/24"}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a body with no vpc_id answered %d, want 400 (%v)", status, body)
	}
	details, _ = body["details"].([]any)
	d, _ = details[0].(map[string]any)
	if d["argument_name"] != "vpc_id" {
		t.Errorf("argument_name is %v, want vpc_id", d["argument_name"])
	}
	if d["help_message"] != "uuid: {}" {
		t.Errorf("help_message is %v, want the cloud's own \"uuid: {}\"", d["help_message"])
	}

	// And the accepting half: a body the document is happy with reaches the
	// store, and THEN the ghost VPC is not found. Without this a guard that
	// refused every route would pass the two cases above.
	pnID, _ := createPN(t, ts, "10.86.0.0/24")
	status, body = do(t, ts, "POST", vpcRegion+"/routes",
		fmt.Sprintf(`{"vpc_id":%q,"destination":"10.9.0.0/24","nexthop_private_network_id":%q}`, ghost, pnID))
	if status != http.StatusNotFound {
		t.Fatalf("a valid body naming a ghost VPC answered %d, want 404 (%v)", status, body)
	}
}

func TestEnableDHCPReadsBackEnabled(t *testing.T) {
	ts := newTestServer(t)

	pnID, _ := createPN(t, ts, "10.84.0.0/24")
	status, pn := do(t, ts, "POST", vpcRegion+"/private-networks/"+pnID+"/enable-dhcp", "{}")
	if status != http.StatusOK {
		t.Fatalf("enable dhcp: status %d", status)
	}
	if flag, _ := pn["dhcp_enabled"].(bool); !flag {
		t.Fatalf("dhcp did not read back enabled: %v", pn)
	}
}

// A VPC created without enable_routing routes, like the real cloud's (#497),
// and its Private Networks reach each other because of it.
//
// The premise was measured on a real account on 2026-08-26 before anything
// was changed here, the test VPC deleted afterwards:
//
//	$ scw vpc vpc create name=feint-premise-routing   # no enable_routing
//	RoutingEnabled                  true
//
// The emulator stored the request field as-is, so the Go zero became the
// default and both VPCs of examples/stacks/scaleway read back false — with the
// consequence measured on the host under `--vm incus-ovn`: the workload VPC's
// two networks were never peered and `app→web:443` answered `connect_ex=111`
// while the web group accepted 0.0.0.0/0.
//
// The assertion is on the peering and not only on the flag, because the flag
// is not a record here: reachableFrom reads it, and a default corrected in the
// view alone would leave the host exactly as it was.
//
// The explicit half is asserted too, in the other direction: a client that
// says false is answered false. The real-cloud measurement above covers the
// absent field and nothing else, so nothing here invents an answer for a field
// that was sent.
func TestAVPCCreatedWithoutEnableRoutingRoutes(t *testing.T) {
	for _, tc := range []struct {
		name    string
		body    string
		routing bool
	}{
		{"without the field", `{"name":"audit-497"}`, true},
		{"with an explicit false", `{"name":"audit-497-off","enable_routing":false}`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rt := newPeeringRuntime()
			ts, _ := newAddressTestServer(t, machine.Use(rt))

			vpc := createVPC(t, ts, tc.body)
			vpcID, _ := vpc["id"].(string)
			if flag, _ := vpc["routing_enabled"].(bool); flag != tc.routing {
				t.Fatalf("routing_enabled=%v for %s, want %v", flag, tc.body, tc.routing)
			}

			networks := make([]string, 0, 2)
			for i, block := range []string{"10.83.0.0/24", "10.83.1.0/24"} {
				status, pn := do(t, ts, "POST", vpcRegion+"/private-networks",
					fmt.Sprintf(`{"name":"member-%d","vpc_id":%q,"subnets":[%q]}`, i, vpcID, block))
				if status != http.StatusOK {
					t.Fatalf("create pn %d: status %d (%v)", i, status, pn)
				}
				id, _ := pn["id"].(string)
				networks = append(networks, id)
			}

			first := machine.NetworkName(machine.NetworkPrefix, networks[0])
			second := machine.NetworkName(machine.NetworkPrefix, networks[1])
			peered := contains(rt.peersOf(first), second) && contains(rt.peersOf(second), first)
			if peered != tc.routing {
				t.Fatalf("the two networks of a VPC created %s are peered=%v, want %v (peers: %v, %v)",
					tc.name, peered, tc.routing, rt.peersOf(first), rt.peersOf(second))
			}
		})
	}
}

// lb/v1 answers 403 for a frontend it does not hold, where its own reads answer
// 404 (#394).
//
// Measured on fr-par, 2026-09-02: POST /routes with a ghost frontend answers
// 403 {"message": "Permission denied"}, while GET /frontends/{id} and
// GET /lbs/{id} both answer 404. So this is not the product refusing everything
// it cannot see — it is this route's own answer, and the emulator answered 404.
//
// The body carries `message` alone, with no `type`: the older lb/v1 error shape,
// which is why it does not share a helper with the projectguard's 403.
func TestALBRouteRefusesAnAbsentFrontendTheWayTheCloudDoes(t *testing.T) {
	ts := newTestServer(t)
	const ghost = "00000000-dead-4000-8000-000000000000"

	status, body := do(t, ts, "POST", "/lb/v1/zones/fr-par-1/routes",
		`{"frontend_id":"`+ghost+`","backend_id":"`+ghost+`","match":{"sni":"probe.example.com"}}`)
	if status != http.StatusForbidden {
		t.Fatalf("a route naming a frontend nothing holds answered %d, want 403 (%v)", status, body)
	}
	if body["message"] != "Permission denied" {
		t.Errorf("message is %v, want the cloud's own \"Permission denied\"", body["message"])
	}
	// The `type` this pack adds and lb/v1 does not send: asserted rather than
	// left to drift, because it is a known divergence and the contract is why.
	// contracts/scaleway.json declares one errorSchema for the whole document,
	// scw.ResponseError with `required: ["type"]`, so a body without it fails
	// internal/probe. docs/limits.md carries the limit.
	if body["type"] != "permissions_denied" {
		t.Errorf("type is %v, want the envelope this pack serves for all of lb/v1", body["type"])
	}

	// The reads beside it still answer 404, which is what makes the 403 above a
	// property of the route rather than of the product.
	if status, _ := do(t, ts, "GET", "/lb/v1/zones/fr-par-1/frontends/"+ghost, ""); status != http.StatusNotFound {
		t.Errorf("GET a frontend that does not exist answered %d, want 404", status)
	}
	if status, _ := do(t, ts, "GET", "/lb/v1/zones/fr-par-1/lbs/"+ghost, ""); status != http.StatusNotFound {
		t.Errorf("GET a load balancer that does not exist answered %d, want 404", status)
	}
}
