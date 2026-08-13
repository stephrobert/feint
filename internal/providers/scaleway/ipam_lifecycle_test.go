package scaleway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
)

// The IPAM lifecycle, the write half SW-4 serves. The reference client is the
// Terraform provider's services/ipam/ip.go, which walks BookIP, GetIP,
// UpdateIP, MoveIP, DetachIP, ReleaseIP, and then carries the booked id into
// CreatePrivateNIC as ipam_ip_ids. These tests walk the same paths.

const ipamRegion = "/ipam/v1/regions/fr-par"
const vpcRegion = "/vpc/v2/regions/fr-par"
const instanceZone = "/instance/v1/zones/fr-par-1"

func createPN(t *testing.T, ts *httptest.Server, subnet string) (id string, subnetID string) {
	t.Helper()
	status, body := do(t, ts, "POST", vpcRegion+"/private-networks",
		fmt.Sprintf(`{"name":"pn-test","subnets":[%q]}`, subnet))
	if status != http.StatusCreated {
		t.Fatalf("create private network: status %d (%v)", status, body)
	}
	id, _ = body["id"].(string)
	subnets, _ := body["subnets"].([]any)
	if len(subnets) != 1 {
		t.Fatalf("private network carries %d subnets, want 1", len(subnets))
	}
	first, _ := subnets[0].(map[string]any)
	subnetID, _ = first["id"].(string)
	return id, subnetID
}

func bookIP(t *testing.T, ts *httptest.Server, body string) map[string]any {
	t.Helper()
	status, out := do(t, ts, "POST", ipamRegion+"/ips", body)
	if status != http.StatusCreated {
		t.Fatalf("book: status %d (%v)", status, out)
	}
	return out
}

func TestABookedIPRoundTripsThroughItsLifecycle(t *testing.T) {
	ts := newTestServer(t)
	pnID, subnetID := createPN(t, ts, "10.61.0.0/24")

	booked := bookIP(t, ts, fmt.Sprintf(`{"source":{"private_network_id":%q},"is_ipv6":false,"tags":["a"]}`, pnID))
	id, _ := booked["id"].(string)
	if id == "" {
		t.Fatalf("no id in the book response: %v", booked)
	}

	// The address carries its mask and belongs to the network's block: the SDK
	// decodes an scw.IPNet, and the Terraform provider matches source.subnet_id
	// against the network's own subnets to recognise its network.
	address, _ := booked["address"].(string)
	prefix, err := netip.ParsePrefix(address)
	if err != nil {
		t.Fatalf("the booked address %q does not parse as CIDR: %v", address, err)
	}
	if block := netip.MustParsePrefix("10.61.0.0/24"); !block.Contains(prefix.Addr()) {
		t.Fatalf("booked %s, outside the network block %s", address, block)
	}
	source, _ := booked["source"].(map[string]any)
	if source["subnet_id"] != subnetID {
		t.Fatalf("source.subnet_id is %v, the network publishes %s", source["subnet_id"], subnetID)
	}
	// Unattached means no resource at all: an object of zero values would tell
	// the client the IP is held by a nameless something.
	if booked["resource"] != nil {
		t.Fatalf("a booked, unattached IP carries a resource: %v", booked["resource"])
	}

	// Create-then-read round-trips field for field.
	status, got := do(t, ts, "GET", ipamRegion+"/ips/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get: status %d", status)
	}
	for _, key := range []string{"address", "project_id", "is_ipv6"} {
		if fmt.Sprint(got[key]) != fmt.Sprint(booked[key]) {
			t.Fatalf("%s changed between book and get: %v then %v", key, booked[key], got[key])
		}
	}

	// UpdateIP, the tags half the provider drives.
	status, updated := do(t, ts, "PATCH", ipamRegion+"/ips/"+id, `{"tags":["b","c"]}`)
	if status != http.StatusOK {
		t.Fatalf("update: status %d (%v)", status, updated)
	}
	if tags, _ := updated["tags"].([]any); len(tags) != 2 {
		t.Fatalf("tags did not update: %v", updated["tags"])
	}

	if status, _ := do(t, ts, "DELETE", ipamRegion+"/ips/"+id, ""); status != http.StatusNoContent {
		t.Fatalf("release: status %d", status)
	}
	if status, _ := do(t, ts, "GET", ipamRegion+"/ips/"+id, ""); status != http.StatusNotFound {
		t.Fatalf("a released IP still answers: status %d", status)
	}
}

func TestBookIPHonoursARequestedAddress(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := createPN(t, ts, "10.62.0.0/24")

	booked := bookIP(t, ts, fmt.Sprintf(`{"source":{"private_network_id":%q},"address":"10.62.0.9"}`, pnID))
	if address, _ := booked["address"].(string); address != "10.62.0.9/24" {
		t.Fatalf("asked for 10.62.0.9, got %q", address)
	}

	// The SDK documents it: "if the requested IP is already reserved, then the
	// call will fail". So it must, or two clients hold one address.
	status, body := do(t, ts, "POST", ipamRegion+"/ips",
		fmt.Sprintf(`{"source":{"private_network_id":%q},"address":"10.62.0.9"}`, pnID))
	if status != http.StatusBadRequest {
		t.Fatalf("booking a reserved address answered %d (%v)", status, body)
	}

	// Outside the block is a different refusal with the same shape.
	status, _ = do(t, ts, "POST", ipamRegion+"/ips",
		fmt.Sprintf(`{"source":{"private_network_id":%q},"address":"192.168.1.1"}`, pnID))
	if status != http.StatusBadRequest {
		t.Fatalf("booking outside the block answered %d", status)
	}
}

// The allocator is rebuilt from every IPAM address of the network, booked
// included. Rebuilt from the NICs alone — the shape of the code before SW-4 —
// a booked address has no NIC yet, the rebuild cannot see it, and the next NIC
// receives the exact address a client had just reserved.
func TestABookedAddressIsNotHandedToTheNextNIC(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := createPN(t, ts, "10.63.0.0/28")

	// Book every address the /28 holds but one: 16 minus network, gateway and
	// broadcast leaves 13, so 12 books corner the pool.
	booked := make(map[string]bool)
	for i := 0; i < 12; i++ {
		out := bookIP(t, ts, fmt.Sprintf(`{"source":{"private_network_id":%q}}`, pnID))
		address, _ := out["address"].(string)
		booked[strings.Split(address, "/")[0]] = true
	}

	status, server := do(t, ts, "POST", instanceZone+"/servers", `{"name":"nic-host","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d", status)
	}
	serverID, _ := server["server"].(map[string]any)["id"].(string)

	status, nic := do(t, ts, "POST", instanceZone+"/servers/"+serverID+"/private_nics",
		fmt.Sprintf(`{"private_network_id":%q}`, pnID))
	if status != http.StatusCreated {
		t.Fatalf("create NIC: status %d (%v)", status, nic)
	}
	ids, _ := nic["private_nic"].(map[string]any)["ipam_ip_ids"].([]any)
	if len(ids) != 1 {
		t.Fatalf("the NIC names %d IPAM ids, want 1", len(ids))
	}
	status, ip := do(t, ts, "GET", ipamRegion+"/ips/"+ids[0].(string), "")
	if status != http.StatusOK {
		t.Fatalf("read the NIC's address: status %d", status)
	}
	address, _ := ip["address"].(string)
	if booked[strings.Split(address, "/")[0]] {
		t.Fatalf("the NIC received %s, an address a client had booked", address)
	}
}

// The booked id rides CreatePrivateNIC as ipam_ip_ids — the exact sequence the
// Terraform provider performs with a scaleway_ipam_ip carried by a private NIC.
func TestANICCarriesTheBookedAddress(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := createPN(t, ts, "10.64.0.0/24")

	booked := bookIP(t, ts, fmt.Sprintf(`{"source":{"private_network_id":%q},"address":"10.64.0.42"}`, pnID))
	bookedID, _ := booked["id"].(string)

	status, server := do(t, ts, "POST", instanceZone+"/servers", `{"name":"carrier","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d", status)
	}
	serverID, _ := server["server"].(map[string]any)["id"].(string)

	status, nic := do(t, ts, "POST", instanceZone+"/servers/"+serverID+"/private_nics",
		fmt.Sprintf(`{"private_network_id":%q,"ipam_ip_ids":[%q]}`, pnID, bookedID))
	if status != http.StatusCreated {
		t.Fatalf("create NIC with a booked address: status %d (%v)", status, nic)
	}
	nicBody, _ := nic["private_nic"].(map[string]any)
	nicID, _ := nicBody["id"].(string)
	ids, _ := nicBody["ipam_ip_ids"].([]any)
	if len(ids) != 1 || ids[0] != bookedID {
		t.Fatalf("the NIC names %v, want exactly the booked id %s", ids, bookedID)
	}

	// The IP now says who holds it, with the NIC's own MAC.
	status, ip := do(t, ts, "GET", ipamRegion+"/ips/"+bookedID, "")
	if status != http.StatusOK {
		t.Fatalf("get booked ip: status %d", status)
	}
	held, _ := ip["resource"].(map[string]any)
	if held == nil || held["type"] != "instance_private_nic" || held["id"] != nicID {
		t.Fatalf("the booked IP does not name its NIC: %v", ip["resource"])
	}
	if held["mac_address"] != nicBody["mac_address"] {
		t.Fatalf("IP and NIC disagree on the MAC: %v vs %v", held["mac_address"], nicBody["mac_address"])
	}

	// A second NIC cannot take the same booked address.
	status, other := do(t, ts, "POST", instanceZone+"/servers", `{"name":"second","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create second server: status %d", status)
	}
	otherID, _ := other["server"].(map[string]any)["id"].(string)
	status, body := do(t, ts, "POST", instanceZone+"/servers/"+otherID+"/private_nics",
		fmt.Sprintf(`{"private_network_id":%q,"ipam_ip_ids":[%q]}`, pnID, bookedID))
	if status != http.StatusBadRequest {
		t.Fatalf("an attached booked address was taken twice: status %d (%v)", status, body)
	}
}

// A booked address is the client's: the NIC delete detaches it, never deletes
// it, because the Terraform destroy order removes the NIC first and releases
// the scaleway_ipam_ip after — through ReleaseIP, not as a side effect.
func TestABookedAddressSurvivesItsNIC(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := createPN(t, ts, "10.65.0.0/24")

	booked := bookIP(t, ts, fmt.Sprintf(`{"source":{"private_network_id":%q}}`, pnID))
	bookedID, _ := booked["id"].(string)

	status, server := do(t, ts, "POST", instanceZone+"/servers", `{"name":"ephemeral","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d", status)
	}
	serverID, _ := server["server"].(map[string]any)["id"].(string)
	status, nic := do(t, ts, "POST", instanceZone+"/servers/"+serverID+"/private_nics",
		fmt.Sprintf(`{"private_network_id":%q,"ipam_ip_ids":[%q]}`, pnID, bookedID))
	if status != http.StatusCreated {
		t.Fatalf("create NIC: status %d", status)
	}
	nicID, _ := nic["private_nic"].(map[string]any)["id"].(string)

	if status, _ := do(t, ts, "DELETE", instanceZone+"/servers/"+serverID+"/private_nics/"+nicID, ""); status != http.StatusNoContent {
		t.Fatalf("delete NIC: status %d", status)
	}

	status, ip := do(t, ts, "GET", ipamRegion+"/ips/"+bookedID, "")
	if status != http.StatusOK {
		t.Fatalf("the booked address died with its NIC: status %d", status)
	}
	if ip["resource"] != nil {
		t.Fatalf("the booked address still claims a holder: %v", ip["resource"])
	}
	// And it is releasable now, which is exactly what terraform destroy does next.
	if status, _ := do(t, ts, "DELETE", ipamRegion+"/ips/"+bookedID, ""); status != http.StatusNoContent {
		t.Fatalf("release after NIC delete: status %d", status)
	}
}

func TestReleasingAnAttachedIPIsRefused(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := createPN(t, ts, "10.66.0.0/24")

	status, server := do(t, ts, "POST", instanceZone+"/servers", `{"name":"holder","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d", status)
	}
	serverID, _ := server["server"].(map[string]any)["id"].(string)
	status, nic := do(t, ts, "POST", instanceZone+"/servers/"+serverID+"/private_nics",
		fmt.Sprintf(`{"private_network_id":%q}`, pnID))
	if status != http.StatusCreated {
		t.Fatalf("create NIC: status %d", status)
	}
	ids, _ := nic["private_nic"].(map[string]any)["ipam_ip_ids"].([]any)
	if len(ids) != 1 {
		t.Fatalf("the NIC names %d IPAM ids, want 1", len(ids))
	}
	ipID, _ := ids[0].(string)

	status, body := do(t, ts, "DELETE", ipamRegion+"/ips/"+ipID, "")
	if status != http.StatusBadRequest {
		t.Fatalf("releasing an attached address answered %d, want a refusal", status)
	}
	if body["type"] != "precondition_failed" {
		t.Fatalf("the refusal is not the SDK's precondition shape: %v", body)
	}
	// Still there, still attached: the refusal protected the NIC.
	if status, _ := do(t, ts, "GET", ipamRegion+"/ips/"+ipID, ""); status != http.StatusOK {
		t.Fatalf("the address vanished despite the refusal: status %d", status)
	}
}

// AttachIP, DetachIP, MoveIP: the custom-resource family. The SDK documents
// that standard resources must not go through here, and the provider only
// sends MACs of things IPAM does not manage.
func TestTheCustomResourceAttachmentFamily(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := createPN(t, ts, "10.67.0.0/24")

	booked := bookIP(t, ts, fmt.Sprintf(`{"source":{"private_network_id":%q}}`, pnID))
	id, _ := booked["id"].(string)

	status, attached := do(t, ts, "POST", ipamRegion+"/ips/"+id+"/attach",
		`{"resource":{"mac_address":"02:00:00:aa:bb:01","name":"vm-1"}}`)
	if status != http.StatusOK {
		t.Fatalf("attach: status %d (%v)", status, attached)
	}
	held, _ := attached["resource"].(map[string]any)
	if held["type"] != "custom" || held["mac_address"] != "02:00:00:aa:bb:01" || held["name"] != "vm-1" {
		t.Fatalf("the attachment did not round-trip: %v", attached["resource"])
	}

	// Attached is attached: a second attach must refuse, not overwrite.
	status, _ = do(t, ts, "POST", ipamRegion+"/ips/"+id+"/attach",
		`{"resource":{"mac_address":"02:00:00:aa:bb:02"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("attaching over an attachment answered %d", status)
	}

	// Detach names the wrong MAC: refused, because detaching by id alone would
	// let a stale caller detach somebody else's attachment.
	status, _ = do(t, ts, "POST", ipamRegion+"/ips/"+id+"/detach",
		`{"resource":{"mac_address":"02:00:00:aa:bb:99"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("detaching with the wrong MAC answered %d", status)
	}

	// Move, the provider's path when custom_resource changes in place.
	status, moved := do(t, ts, "POST", ipamRegion+"/ips/"+id+"/move",
		`{"from_resource":{"mac_address":"02:00:00:aa:bb:01"},"to_resource":{"mac_address":"02:00:00:aa:bb:03","name":"vm-2"}}`)
	if status != http.StatusOK {
		t.Fatalf("move: status %d (%v)", status, moved)
	}
	held, _ = moved["resource"].(map[string]any)
	if held["mac_address"] != "02:00:00:aa:bb:03" || held["name"] != "vm-2" {
		t.Fatalf("the move did not land: %v", moved["resource"])
	}

	status, detached := do(t, ts, "POST", ipamRegion+"/ips/"+id+"/detach",
		`{"resource":{"mac_address":"02:00:00:aa:bb:03"}}`)
	if status != http.StatusOK {
		t.Fatalf("detach: status %d", status)
	}
	if detached["resource"] != nil {
		t.Fatalf("still attached after detach: %v", detached["resource"])
	}

	// A MAC that does not parse is refused at intake, and a name carrying a
	// control character too: both are stored and rendered back verbatim.
	status, _ = do(t, ts, "POST", ipamRegion+"/ips/"+id+"/attach", `{"resource":{"mac_address":"not-a-mac"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a malformed MAC was accepted: status %d", status)
	}
	status, _ = do(t, ts, "POST", ipamRegion+"/ips/"+id+"/attach",
		`{"resource":{"mac_address":"02:00:00:aa:bb:04","name":"a\nb"}}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a control character in the name was accepted: status %d", status)
	}
}

func TestReleaseIPSetIsAllOrNothing(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := createPN(t, ts, "10.68.0.0/24")

	first := bookIP(t, ts, fmt.Sprintf(`{"source":{"private_network_id":%q}}`, pnID))
	second := bookIP(t, ts, fmt.Sprintf(`{"source":{"private_network_id":%q}}`, pnID))
	firstID, _ := first["id"].(string)
	secondID, _ := second["id"].(string)

	// One of the two is attached: the set must refuse and delete neither.
	status, _ := do(t, ts, "POST", ipamRegion+"/ips/"+secondID+"/attach",
		`{"resource":{"mac_address":"02:00:00:cc:dd:01"}}`)
	if status != http.StatusOK {
		t.Fatalf("attach: status %d", status)
	}
	status, _ = do(t, ts, "POST", ipamRegion+"/ip-sets/release",
		fmt.Sprintf(`{"ip_ids":[%q,%q]}`, firstID, secondID))
	if status != http.StatusBadRequest {
		t.Fatalf("a set holding an attached address answered %d", status)
	}
	if status, _ := do(t, ts, "GET", ipamRegion+"/ips/"+firstID, ""); status != http.StatusOK {
		t.Fatalf("the refused set deleted half of itself")
	}

	status, _ = do(t, ts, "POST", ipamRegion+"/ips/"+secondID+"/detach",
		`{"resource":{"mac_address":"02:00:00:cc:dd:01"}}`)
	if status != http.StatusOK {
		t.Fatalf("detach: status %d", status)
	}
	status, _ = do(t, ts, "POST", ipamRegion+"/ip-sets/release",
		fmt.Sprintf(`{"ip_ids":[%q,%q]}`, firstID, secondID))
	if status != http.StatusNoContent {
		t.Fatalf("release set: status %d", status)
	}
	for _, id := range []string{firstID, secondID} {
		if status, _ := do(t, ts, "GET", ipamRegion+"/ips/"+id, ""); status != http.StatusNotFound {
			t.Fatalf("released ip %s still answers", id)
		}
	}
}

// The list filters the provider actually sends when it resolves a NIC's
// addresses: resource_id, resource_type, private_network_id, attached.
func TestIPAMListFiltersAreApplied(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := createPN(t, ts, "10.69.0.0/24")
	otherPN, _ := createPN(t, ts, "10.70.0.0/24")

	booked := bookIP(t, ts, fmt.Sprintf(`{"source":{"private_network_id":%q}}`, pnID))
	bookedID, _ := booked["id"].(string)
	bookIP(t, ts, fmt.Sprintf(`{"source":{"private_network_id":%q}}`, otherPN))

	status, server := do(t, ts, "POST", instanceZone+"/servers", `{"name":"filters","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d", status)
	}
	serverID, _ := server["server"].(map[string]any)["id"].(string)
	status, nic := do(t, ts, "POST", instanceZone+"/servers/"+serverID+"/private_nics",
		fmt.Sprintf(`{"private_network_id":%q}`, pnID))
	if status != http.StatusCreated {
		t.Fatalf("create NIC: status %d", status)
	}
	nicID, _ := nic["private_nic"].(map[string]any)["id"].(string)

	count := func(query string) int {
		t.Helper()
		status, body := do(t, ts, "GET", ipamRegion+"/ips"+query, "")
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", query, status)
		}
		ips, _ := body["ips"].([]any)
		return len(ips)
	}

	if got := count(""); got != 3 {
		t.Fatalf("unfiltered: %d, want 3", got)
	}
	if got := count("?private_network_id=" + pnID); got != 2 {
		t.Fatalf("private_network_id: %d, want 2", got)
	}
	if got := count("?resource_id=" + nicID); got != 1 {
		t.Fatalf("resource_id: %d, want 1", got)
	}
	if got := count("?resource_type=instance_private_nic"); got != 1 {
		t.Fatalf("resource_type: %d, want 1", got)
	}
	if got := count("?attached=true"); got != 1 {
		t.Fatalf("attached=true: %d, want 1", got)
	}
	if got := count("?attached=false"); got != 2 {
		t.Fatalf("attached=false: %d, want 2", got)
	}
	if got := count("?ip_ids=" + bookedID); got != 1 {
		t.Fatalf("ip_ids: %d, want 1", got)
	}
	// Everything served here is IPv4 and regional.
	if got := count("?is_ipv6=true"); got != 0 {
		t.Fatalf("is_ipv6=true: %d, want 0", got)
	}
	if got := count("?zonal=fr-par-1"); got != 0 {
		t.Fatalf("zonal: %d, want 0", got)
	}
}

// Booking in a network of another project is refused: upstream documents that
// the Project must match the Private Network's.
func TestBookIPRefusesAForeignProject(t *testing.T) {
	ts := newTestServer(t)
	pnID, _ := createPN(t, ts, "10.71.0.0/24")

	status, body := do(t, ts, "POST", ipamRegion+"/ips",
		fmt.Sprintf(`{"project_id":"22222222-2222-4222-8222-222222222222","source":{"private_network_id":%q}}`, pnID))
	if status != http.StatusBadRequest {
		t.Fatalf("booking across projects answered %d (%v)", status, body)
	}
}
