package outscale_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

// Secondary private addresses on a NIC (#172), the last two operations that had
// no decision.
//
// They were deliberately not declined: a NIC carrying several private addresses
// is ordinary and implementable, so "out of scope" would have been false where
// the truth was "not yet". Shapes read from the SDK's api.yaml — NicId required,
// PrivateIps or SecondaryPrivateIpCount, AllowRelink for a move.
func TestSecondaryAddressesAreAllocatedAndPublished(t *testing.T) {
	ts := newServer(t)
	net, sub := netWithSubnet(t, ts, "10.31.0.0/16", "10.31.1.0/24")
	_ = net
	nic := createNic(t, ts, sub)

	// Named: the address lands, and the primary keeps its flag.
	status, _ := doAction(t, ts, "LinkPrivateIps",
		`{"NicId":"`+nic+`","PrivateIps":["10.31.1.40"]}`)
	if status != http.StatusOK {
		t.Fatalf("LinkPrivateIps answered %d", status)
	}
	entries := privateIPsOf(t, ts, nic)
	if !hasAddress(entries, "10.31.1.40", false) {
		t.Errorf("10.31.1.40 is not published as a secondary address: %v", entries)
	}
	if countPrimary(entries) != 1 {
		t.Errorf("the primary flag moved or multiplied: %v", entries)
	}

	// Counted: the allocator hands out what the Subnet has left, and never an
	// address already taken.
	status, _ = doAction(t, ts, "LinkPrivateIps",
		`{"NicId":"`+nic+`","SecondaryPrivateIpCount":2}`)
	if status != http.StatusOK {
		t.Fatalf("the counted form answered %d", status)
	}
	entries = privateIPsOf(t, ts, nic)
	if got := len(entries); got != 4 {
		t.Errorf("the NIC publishes %d addresses, want 4 (one primary, three secondary): %v", got, entries)
	}
	seen := map[string]bool{}
	for _, entry := range entries {
		address, _ := entry["PrivateIp"].(string)
		if seen[address] {
			t.Errorf("%s was handed out twice: %v", address, entries)
		}
		seen[address] = true
	}

	// Both forms at once is a client error, not a guess.
	status, _ = doAction(t, ts, "LinkPrivateIps",
		`{"NicId":"`+nic+`","PrivateIps":["10.31.1.50"],"SecondaryPrivateIpCount":1}`)
	if status != http.StatusBadRequest {
		t.Errorf("naming and counting together answered %d, want 400", status)
	}

	// An address outside the Subnet is refused rather than stored.
	status, _ = doAction(t, ts, "LinkPrivateIps",
		`{"NicId":"`+nic+`","PrivateIps":["10.99.9.9"]}`)
	if status != http.StatusBadRequest {
		t.Errorf("an address outside the Subnet answered %d, want 400", status)
	}
}

// The primary address belongs to the interface and goes with it.
//
// Unlinking it would take the machine off its own network while the API kept
// publishing it, which is the exact disagreement between what a machine carries
// and what its API says that #202 removed.
func TestThePrimaryAddressIsNeverUnlinked(t *testing.T) {
	ts := newServer(t)
	_, sub := netWithSubnet(t, ts, "10.32.0.0/16", "10.32.1.0/24")
	nic := createNic(t, ts, sub)

	entries := privateIPsOf(t, ts, nic)
	primary := ""
	for _, entry := range entries {
		if flag, _ := entry["IsPrimary"].(bool); flag {
			primary, _ = entry["PrivateIp"].(string)
		}
	}
	if primary == "" {
		t.Fatalf("the NIC publishes no primary address: %v", entries)
	}

	status, _ := doAction(t, ts, "UnlinkPrivateIps",
		`{"NicId":"`+nic+`","PrivateIps":["`+primary+`"]}`)
	if status != http.StatusConflict {
		t.Errorf("unlinking the primary answered %d, want 409", status)
	}
	if got := privateIPsOf(t, ts, nic); len(got) != len(entries) {
		t.Errorf("the refused unlink changed the list anyway: %v", got)
	}

	// And an address the NIC does not hold is a client error, never a silent
	// success: a caller that mistyped one would otherwise believe it removed
	// something.
	status, _ = doAction(t, ts, "UnlinkPrivateIps",
		`{"NicId":"`+nic+`","PrivateIps":["10.32.1.77"]}`)
	if status != http.StatusBadRequest {
		t.Errorf("unlinking an address it does not hold answered %d, want 400", status)
	}

	// The accepting half: a secondary address links and unlinks.
	if status, _ := doAction(t, ts, "LinkPrivateIps",
		`{"NicId":"`+nic+`","PrivateIps":["10.32.1.60"]}`); status != http.StatusOK {
		t.Fatalf("link answered %d", status)
	}
	if status, _ := doAction(t, ts, "UnlinkPrivateIps",
		`{"NicId":"`+nic+`","PrivateIps":["10.32.1.60"]}`); status != http.StatusOK {
		t.Fatalf("unlink answered %d", status)
	}
	if got := privateIPsOf(t, ts, nic); hasAddress(got, "10.32.1.60", false) {
		t.Errorf("the unlinked address is still published: %v", got)
	}
}

// A linked address must leave the Subnet's pool, or a Vm created afterwards is
// handed an address a NIC already holds — two interfaces fighting for one IP,
// which placeInSubnet's own comment warns about for the primary case.
func TestALinkedAddressIsNotHandedToTheNextVm(t *testing.T) {
	ts := newServer(t)
	_, sub := netWithSubnet(t, ts, "10.33.0.0/16", "10.33.1.0/24")
	nic := createNic(t, ts, sub)

	if status, _ := doAction(t, ts, "LinkPrivateIps",
		`{"NicId":"`+nic+`","SecondaryPrivateIpCount":3}`); status != http.StatusOK {
		t.Fatalf("link answered %d", status)
	}
	linked := map[string]bool{}
	for _, entry := range privateIPsOf(t, ts, nic) {
		if address, _ := entry["PrivateIp"].(string); address != "" {
			linked[address] = true
		}
	}

	// Every Vm the Subnet can still hold, and none of them may take one.
	for range 5 {
		status, doc := doAction(t, ts, "CreateVms",
			`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2","SubnetId":"`+sub+`"}`)
		if status != http.StatusOK {
			break
		}
		vms, _ := doc["Vms"].([]any)
		if len(vms) == 0 {
			break
		}
		vm, _ := vms[0].(map[string]any)
		address, _ := vm["PrivateIp"].(string)
		if linked[address] {
			t.Fatalf("a Vm was handed %s, which the NIC already holds", address)
		}
	}
}

// ---- helpers ----------------------------------------------------------------

func netWithSubnet(t *testing.T, ts *httptest.Server, netRange, subRange string) (string, string) {
	t.Helper()
	status, doc := doAction(t, ts, "CreateNet", `{"IpRange":"`+netRange+`"}`)
	if status != http.StatusOK {
		t.Fatalf("CreateNet answered %d: %v", status, doc)
	}
	netDoc, _ := doc["Net"].(map[string]any)
	netID, _ := netDoc["NetId"].(string)

	status, doc = doAction(t, ts, "CreateSubnet",
		`{"NetId":"`+netID+`","IpRange":"`+subRange+`"}`)
	if status != http.StatusOK {
		t.Fatalf("CreateSubnet answered %d: %v", status, doc)
	}
	subDoc, _ := doc["Subnet"].(map[string]any)
	subID, _ := subDoc["SubnetId"].(string)
	return netID, subID
}

func createNic(t *testing.T, ts *httptest.Server, subnetID string) string {
	t.Helper()
	status, doc := doAction(t, ts, "CreateNic", `{"SubnetId":"`+subnetID+`"}`)
	if status != http.StatusOK {
		t.Fatalf("CreateNic answered %d: %v", status, doc)
	}
	nic, _ := doc["Nic"].(map[string]any)
	id, _ := nic["NicId"].(string)
	if id == "" {
		t.Fatalf("CreateNic returned no NicId: %v", doc)
	}
	return id
}

func privateIPsOf(t *testing.T, ts *httptest.Server, nicID string) []map[string]any {
	t.Helper()
	_, doc := doAction(t, ts, "ReadNics",
		`{"Filters":{"NicIds":["`+nicID+`"]}}`)
	nics, _ := doc["Nics"].([]any)
	if len(nics) == 0 {
		t.Fatalf("ReadNics found no %s", nicID)
	}
	nic, _ := nics[0].(map[string]any)
	raw, _ := nic["PrivateIps"].([]any)
	out := make([]map[string]any, 0, len(raw))
	for _, entry := range raw {
		if m, ok := entry.(map[string]any); ok {
			out = append(out, m)
		}
	}
	return out
}

func hasAddress(entries []map[string]any, want string, primary bool) bool {
	for _, entry := range entries {
		address, _ := entry["PrivateIp"].(string)
		flag, _ := entry["IsPrimary"].(bool)
		if address == want && flag == primary {
			return true
		}
	}
	return false
}

func countPrimary(entries []map[string]any) int {
	n := 0
	for _, entry := range entries {
		if flag, _ := entry["IsPrimary"].(bool); flag {
			n++
		}
	}
	return n
}

// The count a client reads before it creates must be the count the create will
// honour (#217).
//
// AvailableIpsCount rebuilt its own reservation loop instead of asking
// subnetAllocator, and the two drifted exactly where a second computation of one
// truth always drifts: at the corner the first one learned about later. The
// allocator reserves machines, primary NIC addresses and secondary ones, and
// reads liveness through Gone; the view's loop reserved machines only, and
// compared a state.
//
// Measured before the fix, on the subnet below: 11 published, 8 creates
// accepted. A Terraform count sized on that figure plans against three addresses
// the emulator refuses to hand out — and a plan that cannot apply is worse than a
// smaller number, because nothing says which of the two was wrong.
//
// The assertion is the agreement itself rather than a figure: hard-coding 8 would
// pass just as well against a second wrong computation.
func TestAvailableIpsCountAgreesWithWhatACreateWillDo(t *testing.T) {
	ts := newServer(t)
	_, subnet := netWithSubnet(t, ts, "10.51.0.0/16", "10.51.1.0/28")

	available := func() int {
		t.Helper()
		_, doc := doAction(t, ts, "ReadSubnets", `{"Filters":{"SubnetIds":["`+subnet+`"]}}`)
		subnets, _ := doc["Subnets"].([]any)
		if len(subnets) == 0 {
			t.Fatalf("no subnet in %v", doc)
		}
		first, _ := subnets[0].(map[string]any)
		count, ok := first["AvailableIpsCount"].(float64)
		if !ok {
			t.Fatalf("the subnet publishes no AvailableIpsCount: %v", first)
		}
		return int(count)
	}

	// The three holders the view's own loop could not see: a NIC's primary
	// address and two secondary ones.
	nic := createNic(t, ts, subnet)
	if status, doc := doAction(t, ts, "LinkPrivateIps",
		`{"NicId":"`+nic+`","SecondaryPrivateIpCount":2}`); status != http.StatusOK {
		t.Fatalf("LinkPrivateIps answered %d: %v", status, doc)
	}

	published := available()
	if published < 1 {
		t.Fatalf("the subnet publishes %d available, so this test measures nothing", published)
	}

	made := 0
	for range published + 5 {
		status, _ := doAction(t, ts, "CreateVms",
			`{"ImageId":"ami-12345678","VmType":"tinav4.c1r1p2","SubnetId":"`+subnet+`"}`)
		if status != http.StatusOK {
			break
		}
		made++
	}
	if made != published {
		t.Errorf("the subnet published %d available addresses and accepted %d creates: "+
			"a client sizing a pool on that figure plans against addresses the emulator refuses",
			published, made)
	}
	if left := available(); left != 0 {
		t.Errorf("every address is taken and the subnet still publishes %d available", left)
	}

	// And the count comes back when a machine dies, which is the half a stale
	// second computation would also get wrong — the allocator reads Gone, so the
	// view now does too.
	_, doc := doAction(t, ts, "ReadVms", `{"Filters":{"SubnetIds":["`+subnet+`"]}}`)
	vms, _ := doc["Vms"].([]any)
	if len(vms) == 0 {
		t.Fatalf("no Vm to terminate: %v", doc)
	}
	first, _ := vms[0].(map[string]any)
	vmID, _ := first["VmId"].(string)
	doAction(t, ts, "StopVms", `{"VmIds":["`+vmID+`"]}`)
	if status, doc := doAction(t, ts, "DeleteVms", `{"VmIds":["`+vmID+`"]}`); status != http.StatusOK {
		t.Fatalf("DeleteVms answered %d: %v", status, doc)
	}
	if back := available(); back != 1 {
		t.Errorf("a terminated Vm gave back %d addresses, want 1", back)
	}
	if status, doc := doAction(t, ts, "CreateVms",
		`{"ImageId":"ami-12345678","VmType":"tinav4.c1r1p2","SubnetId":"`+subnet+`"}`); status != http.StatusOK {
		t.Errorf("the address a terminated Vm gave back cannot be used: %d %v", status, doc)
	}
}
