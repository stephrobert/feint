package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

const regionURL = "/vpc/v2/regions/fr-par"

// nicAddress resolves the address of a NIC the way a client does: the NIC names
// its addresses through ipam_ip_ids, and ipam/v1 holds them. instance/v1.
// PrivateNIC carries no address of its own, so any test reading one off the NIC
// would be reading something the emulator invented.
func nicAddress(t *testing.T, ts *httptest.Server, nic map[string]any) string {
	t.Helper()

	ids, _ := nic["ipam_ip_ids"].([]any)
	if len(ids) == 0 {
		t.Fatalf("the NIC names no IPAM address: %v", nic)
	}
	id, _ := ids[0].(string)

	status, ip := do(t, ts, "GET", "/ipam/v1/regions/fr-par/ips/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("resolve %s through IPAM: expected 200, got %d (%v)", id, status, ip)
	}
	// The address is an scw.IPNet upstream, so it carries its mask.
	address, _ := ip["address"].(string)
	host, _, found := strings.Cut(address, "/")
	if !found {
		t.Errorf("IPAM served %q, which carries no mask; the SDK decodes an IPNet", address)
	}
	return host
}

// privateNetwork creates one and returns its id and body.
//
// 200, not 201: vpc/v2's creates were measured on the wire against a real
// account (see vpcCreateStatus). This helper is the only place the pack's
// private-network creates are asserted, so it is where the change bites.
func privateNetwork(t *testing.T, ts *httptest.Server, body string) (string, map[string]any) {
	t.Helper()
	status, created := do(t, ts, "POST", regionURL+"/private-networks", body)
	if status != http.StatusOK {
		t.Fatalf("create private network: expected 200, got %d (%v)", status, created)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create private network: no id in %v", created)
	}
	return id, created
}

// The block a client asks for must come back unchanged, and as a list of subnet
// objects: the SDK decodes them into vpc.Subnet, not into bare strings.
func TestPrivateNetworkRoundTripsItsBlock(t *testing.T) {
	ts := newTestServer(t)

	_, created := privateNetwork(t, ts, `{"name":"app","subnets":["10.20.0.0/24"]}`)
	subnets, _ := created["subnets"].([]any)
	// Two records, one per family: the IPv4 block the client chose, and the
	// IPv6 /64 upstream allocates unasked (#270).
	if len(subnets) != 2 {
		t.Fatalf("expected two subnets, got %v", created["subnets"])
	}
	subnet := ipv4SubnetOf(t, subnets)
	if subnet["subnet"] != "10.20.0.0/24" {
		t.Errorf("block came back as %v, want 10.20.0.0/24", subnet["subnet"])
	}
	if subnet["private_network_id"] != created["id"] {
		t.Errorf("the subnet does not name its network: %v", subnet)
	}
	// The VPC product spells the owner project_id, where the instance product
	// spells it project. Mixing them up decodes into a zero value in silence.
	if created["project_id"] == nil || created["organization_id"] == nil {
		t.Errorf("missing project_id or organization_id: %v", created)
	}
	if created["vpc_id"] == nil || created["vpc_id"] == "" {
		t.Errorf("the network belongs to no VPC: %v", created)
	}
}

// A block nobody asked for is still a legal block, and two generated ones never
// overlap.
func TestGeneratedBlocksDoNotOverlap(t *testing.T) {
	ts := newTestServer(t)

	seen := map[string]bool{}
	for i := 0; i < 5; i++ {
		_, created := privateNetwork(t, ts, `{"name":"generated"}`)
		subnets, _ := created["subnets"].([]any)
		subnet, _ := subnets[0].(map[string]any)
		block, _ := subnet["subnet"].(string)
		if block == "" {
			t.Fatalf("no block was generated: %v", created)
		}
		if seen[block] {
			t.Errorf("block %s handed out twice", block)
		}
		seen[block] = true
	}
}

// Validation the emulators this one is measured against do not perform: floci
// stores any CIDR, never checks the mask, and never checks overlap.
func TestPrivateNetworkRejectsBadBlocks(t *testing.T) {
	ts := newTestServer(t)
	privateNetwork(t, ts, `{"name":"first","subnets":["10.30.0.0/24"]}`)

	for name, body := range map[string]string{
		"host bits set":  `{"name":"x","subnets":["10.40.0.5/24"]}`,
		"not a CIDR":     `{"name":"x","subnets":["not-a-block"]}`,
		"mask too wide":  `{"name":"x","subnets":["10.0.0.0/8"]}`,
		"mask too small": `{"name":"x","subnets":["10.50.0.0/30"]}`,
		"overlaps":       `{"name":"x","subnets":["10.30.0.128/25"]}`,
	} {
		t.Run(name, func(t *testing.T) {
			status, denied := do(t, ts, "POST", regionURL+"/private-networks", body)
			if status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%v)", status, denied)
			}
			if denied["type"] != "invalid_arguments" {
				t.Errorf("got error type %v, want invalid_arguments", denied["type"])
			}
		})
	}
}

// The addresses handed to NICs come from the network's own block, are distinct,
// and skip the range the runtime keeps for itself.
func TestPrivateNICsAllocateFromTheBlock(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := privateNetwork(t, ts, `{"name":"app","subnets":["10.60.0.0/24"]}`)

	seen := map[string]bool{}
	for _, name := range []string{"a", "b", "c"} {
		serverID, _ := serverWith(t, ts, `{"name":"`+name+`","commercial_type":"DEV1-S"}`)
		status, created := do(t, ts, "POST",
			zoneURL+"/servers/"+serverID+"/private_nics", `{"private_network_id":"`+pnID+`"}`)
		if status != http.StatusCreated {
			t.Fatalf("attach %s: expected 201, got %d (%v)", name, status, created)
		}
		nic, _ := created["private_nic"].(map[string]any)
		address := nicAddress(t, ts, nic)

		switch {
		case address == "":
			t.Fatalf("no address in %v", created)
		case seen[address]:
			t.Errorf("address %s handed out twice", address)
		case address == "10.60.0.0" || address == "10.60.0.1":
			t.Errorf("address %s falls in the reserved range", address)
		case address == "10.60.0.255":
			t.Errorf("address %s is the broadcast address", address)
		}
		seen[address] = true

		if nic["private_network_id"] != pnID {
			t.Errorf("the NIC does not name its network: %v", nic)
		}
		if nic["mac_address"] == nil || nic["mac_address"] == "" {
			t.Errorf("the NIC has no MAC address: %v", nic)
		}
	}
}

// A released address comes back: without this a create/destroy loop drains the
// subnet until nothing can be attached.
func TestDetachingReturnsTheAddress(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := privateNetwork(t, ts, `{"name":"app","subnets":["10.70.0.0/28"]}`)
	serverID, _ := serverWith(t, ts, `{"name":"a","commercial_type":"DEV1-S"}`)

	_, created := do(t, ts, "POST", zoneURL+"/servers/"+serverID+"/private_nics",
		`{"private_network_id":"`+pnID+`"}`)
	nic, _ := created["private_nic"].(map[string]any)
	first := nicAddress(t, ts, nic)
	nicID, _ := nic["id"].(string)

	if status, _ := do(t, ts, "DELETE", zoneURL+"/servers/"+serverID+"/private_nics/"+nicID, ""); status != http.StatusNoContent {
		t.Fatalf("detach: expected 204, got %d", status)
	}
	_, created = do(t, ts, "POST", zoneURL+"/servers/"+serverID+"/private_nics",
		`{"private_network_id":"`+pnID+`"}`)
	nic, _ = created["private_nic"].(map[string]any)
	if again := nicAddress(t, ts, nic); again != first {
		t.Errorf("the released address was not reused: got %s, want %s", again, first)
	}
}

// Preconditions a destroy depends on: Terraform tears down in reverse order and
// reads these errors to know it must retry.
func TestNetworkPreconditions(t *testing.T) {
	ts := newTestServer(t)
	pnID, created := privateNetwork(t, ts, `{"name":"app","subnets":["10.80.0.0/24"]}`)
	vpcID, _ := created["vpc_id"].(string)
	serverID, _ := serverWith(t, ts, `{"name":"a","commercial_type":"DEV1-S"}`)

	do(t, ts, "POST", zoneURL+"/servers/"+serverID+"/private_nics", `{"private_network_id":"`+pnID+`"}`)

	status, denied := do(t, ts, "DELETE", regionURL+"/private-networks/"+pnID, "")
	if status != http.StatusBadRequest || denied["type"] != "precondition_failed" {
		t.Errorf("delete a network in use: got %d %v, want 400 precondition_failed", status, denied["type"])
	}

	status, denied = do(t, ts, "DELETE", regionURL+"/vpcs/"+vpcID, "")
	if status != http.StatusBadRequest || denied["type"] != "precondition_failed" {
		t.Errorf("delete the default VPC: got %d %v, want 400 precondition_failed", status, denied["type"])
	}

	// Attaching the same server twice to the same network is refused: the
	// second address would be one the control plane cannot account for.
	status, denied = do(t, ts, "POST",
		zoneURL+"/servers/"+serverID+"/private_nics", `{"private_network_id":"`+pnID+`"}`)
	if status != http.StatusBadRequest {
		t.Errorf("attach twice: got %d (%v), want 400", status, denied)
	}
}

// An exhausted block is a real answer, not an internal error.
func TestExhaustedBlockIsReported(t *testing.T) {
	ts := newTestServer(t)
	// A /28 leaves thirteen usable addresses once the network address and the
	// gateway are reserved, and the broadcast excluded. /28 is also the
	// narrowest mask the product accepts.
	pnID, _ := privateNetwork(t, ts, `{"name":"tiny","subnets":["10.90.0.0/28"]}`)

	attached := 0
	for i := 0; i < 20; i++ {
		serverID, _ := serverWith(t, ts, `{"name":"s","commercial_type":"DEV1-S"}`)
		status, body := do(t, ts, "POST",
			zoneURL+"/servers/"+serverID+"/private_nics", `{"private_network_id":"`+pnID+`"}`)
		if status == http.StatusCreated {
			attached++
			continue
		}
		if body["type"] != "precondition_failed" {
			t.Fatalf("exhaustion reported as %v, want precondition_failed (%v)", body["type"], body)
		}
		break
	}
	if attached != 13 {
		t.Errorf("a /28 handed out %d addresses, want 13", attached)
	}
}
