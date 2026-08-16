package exoscale_test

import (
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"
)

// Private networks, batch 3 (#9). Request bodies here mirror what
// EXOSCALE_TRACE=1 shows the official CLI sending — options is present on
// every create, {} when nothing was asked — and the assertions on responses
// hold the emulator to the published private-network schema, the only source
// of the response shape until an account holding one is recorded.

// createTestInstance posts the minimal instance the pack accepts and returns
// its id from the list, the way a client finds it.
func createTestInstance(t *testing.T, h http.Handler, name string) string {
	t.Helper()
	rec, out := call(t, h, "POST", "/v2/instance", `{
		"name": "`+name+`",
		"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template": {"id": "11111111-1111-4111-8111-111111111111"},
		"disk-size": 10
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("instance create answered %d: %v", rec.Code, out)
	}
	_, listed := call(t, h, "GET", "/v2/instance", "")
	for _, entry := range listed["instances"].([]any) {
		inst, _ := entry.(map[string]any)
		if inst["name"] == name {
			return inst["id"].(string)
		}
	}
	t.Fatalf("instance %s is not in the list after create", name)
	return ""
}

// createTestNetwork posts a network and resolves its id through the list, the
// way the CLI does for every command that takes a name.
func createTestNetwork(t *testing.T, h http.Handler, body, name string) string {
	t.Helper()
	rec, out := call(t, h, "POST", "/v2/private-network", body)
	if rec.Code != http.StatusOK {
		t.Fatalf("network create answered %d: %v", rec.Code, out)
	}
	_, listed := call(t, h, "GET", "/v2/private-network", "")
	for _, entry := range listed["private-networks"].([]any) {
		pn, _ := entry.(map[string]any)
		if pn["name"] == name {
			return pn["id"].(string)
		}
	}
	t.Fatalf("network %s is not in the list after create", name)
	return ""
}

const managedNetworkBody = `{
	"name": "pn-managed",
	"description": "range under test",
	"start-ip": "10.90.0.20",
	"end-ip": "10.90.0.200",
	"netmask": "255.255.255.0",
	"options": {}
}`

// A managed network round-trips: what the create declared is what every read
// answers, the schema's own vni included, and nothing their schema does not
// declare rides along.
func TestAPrivateNetworkRoundTrips(t *testing.T) {
	h := serve(t)
	id := createTestNetwork(t, h, managedNetworkBody, "pn-managed")

	rec, pn := call(t, h, "GET", "/v2/private-network/"+id, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("get answered %d: %v", rec.Code, pn)
	}
	for key, want := range map[string]string{
		"name": "pn-managed", "description": "range under test",
		"start-ip": "10.90.0.20", "end-ip": "10.90.0.200", "netmask": "255.255.255.0",
	} {
		if pn[key] != want {
			t.Errorf("%s is %v, want %s", key, pn[key], want)
		}
	}
	// vni is the schema's VXLAN id, an integer above 0.
	if vni, ok := pn["vni"].(float64); !ok || vni < 1 {
		t.Errorf("vni is %v, want an integer above 0", pn["vni"])
	}
	// Absent keys, not empty ones, for what nothing holds yet: the omission
	// habit measured on this API's security groups.
	for _, key := range []string{"leases", "labels", "options"} {
		if _, present := pn[key]; present {
			t.Errorf("%s is present on a network that has none: %v", key, pn[key])
		}
	}

	// A plain network keeps no range at all rather than an empty one.
	plainID := createTestNetwork(t, h, `{"name":"pn-plain","options":{}}`, "pn-plain")
	_, plain := call(t, h, "GET", "/v2/private-network/"+plainID, "")
	for _, key := range []string{"start-ip", "end-ip", "netmask"} {
		if _, present := plain[key]; present {
			t.Errorf("%s is present on a plain network: %v", key, plain[key])
		}
	}
}

// The range triple travels together: a start with no netmask is addressing
// nobody can compute, and their CLI always sends the three as one.
func TestAHalfDeclaredRangeIsRefused(t *testing.T) {
	h := serve(t)
	for _, body := range []string{
		`{"name":"n","start-ip":"10.0.0.10"}`,
		`{"name":"n","start-ip":"10.0.0.10","end-ip":"10.0.0.50"}`,
		`{"name":"n","start-ip":"10.0.0.10","end-ip":"10.0.0.50","netmask":"255.0.255.0"}`,
		`{"name":"n","start-ip":"10.0.0.50","end-ip":"10.0.0.10","netmask":"255.255.255.0"}`,
		`{"name":"n","start-ip":"10.0.0.10","end-ip":"10.0.1.50","netmask":"255.255.255.0"}`,
		`{"name":"n","start-ip":"not-an-ip","end-ip":"10.0.0.50","netmask":"255.255.255.0"}`,
	} {
		if rec, out := call(t, h, "POST", "/v2/private-network", body); rec.Code != http.StatusBadRequest {
			t.Errorf("%s was accepted: %d %v", body, rec.Code, out)
		}
	}
}

// An attach leases an address of the declared range, publishes it on the
// network, and the instance answers the membership their instance schema
// declares: {id, mac-address}, the MAC stable across reads.
func TestAttachLeasesAnAddressAndTheInstancePublishesIt(t *testing.T) {
	h := serve(t)
	pnID := createTestNetwork(t, h, managedNetworkBody, "pn-managed")
	instID := createTestInstance(t, h, "worker")

	rec, out := call(t, h, "PUT", "/v2/private-network/"+pnID+":attach",
		`{"instance":{"id":"`+instID+`"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("attach answered %d: %v", rec.Code, out)
	}

	_, pn := call(t, h, "GET", "/v2/private-network/"+pnID, "")
	leases, _ := pn["leases"].([]any)
	if len(leases) != 1 {
		t.Fatalf("want one lease, got %v", pn["leases"])
	}
	lease, _ := leases[0].(map[string]any)
	if lease["instance-id"] != instID {
		t.Errorf("the lease names %v, want %s", lease["instance-id"], instID)
	}
	ip, _ := lease["ip"].(string)
	if !strings.HasPrefix(ip, "10.90.0.") {
		t.Errorf("the lease %q is outside the declared range", ip)
	}

	_, inst := call(t, h, "GET", "/v2/instance/"+instID, "")
	refs, _ := inst["private-networks"].([]any)
	if len(refs) != 1 {
		t.Fatalf("the instance publishes %v, want one membership", inst["private-networks"])
	}
	ref, _ := refs[0].(map[string]any)
	if ref["id"] != pnID {
		t.Errorf("the membership names %v, want %s", ref["id"], pnID)
	}
	mac, _ := ref["mac-address"].(string)
	if len(mac) != 17 || !strings.HasPrefix(mac, "0a:") {
		t.Errorf("mac-address %q is not a derived locally-administered MAC", mac)
	}
	_, again := call(t, h, "GET", "/v2/instance/"+instID, "")
	againRef, _ := again["private-networks"].([]any)[0].(map[string]any)
	if againRef["mac-address"] != mac {
		t.Errorf("the MAC changed between two reads: %v then %v", mac, againRef["mac-address"])
	}

	// A second attach of the same pair would hand one interface two leases.
	if rec, _ := call(t, h, "PUT", "/v2/private-network/"+pnID+":attach",
		`{"instance":{"id":"`+instID+`"}}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a second attach of the same pair was accepted: %d", rec.Code)
	}

	// Detach takes the lease and the membership with it.
	if rec, _ := call(t, h, "PUT", "/v2/private-network/"+pnID+":detach",
		`{"instance":{"id":"`+instID+`"}}`); rec.Code != http.StatusOK {
		t.Fatalf("detach answered %d", rec.Code)
	}
	_, pnAfter := call(t, h, "GET", "/v2/private-network/"+pnID, "")
	if _, still := pnAfter["leases"]; still {
		t.Errorf("the lease survived its detach: %v", pnAfter["leases"])
	}
	_, instAfter := call(t, h, "GET", "/v2/instance/"+instID, "")
	if refs, _ := instAfter["private-networks"].([]any); len(refs) != 0 {
		t.Errorf("the membership survived its detach: %v", refs)
	}
}

// The static lease path: an address the client chose is honoured inside the
// range, refused outside it, refused when taken, and refused entirely on a
// network that declares no range — their CLI documents --ip as managed only.
func TestAStaticLeaseIsValidatedAgainstTheRange(t *testing.T) {
	h := serve(t)
	pnID := createTestNetwork(t, h, managedNetworkBody, "pn-managed")
	plainID := createTestNetwork(t, h, `{"name":"pn-plain","options":{}}`, "pn-plain")
	first := createTestInstance(t, h, "first")
	second := createTestInstance(t, h, "second")

	rec, _ := call(t, h, "PUT", "/v2/private-network/"+pnID+":attach",
		`{"instance":{"id":"`+first+`"},"ip":"10.90.0.42"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("a static lease inside the range was refused: %d", rec.Code)
	}
	_, pn := call(t, h, "GET", "/v2/private-network/"+pnID, "")
	lease, _ := pn["leases"].([]any)[0].(map[string]any)
	if lease["ip"] != "10.90.0.42" {
		t.Fatalf("the lease is %v, want the requested 10.90.0.42", lease["ip"])
	}

	for body, why := range map[string]string{
		`{"instance":{"id":"` + second + `"},"ip":"10.90.1.42"}`: "outside the range",
		`{"instance":{"id":"` + second + `"},"ip":"10.90.0.42"}`: "already leased",
		`{"instance":{"id":"` + second + `"},"ip":"nonsense"}`:   "not an address",
	} {
		if rec, _ := call(t, h, "PUT", "/v2/private-network/"+pnID+":attach", body); rec.Code != http.StatusBadRequest {
			t.Errorf("a static lease %s was accepted: %d", why, rec.Code)
		}
	}
	if rec, _ := call(t, h, "PUT", "/v2/private-network/"+plainID+":attach",
		`{"instance":{"id":"`+second+`"},"ip":"10.90.0.50"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("a static lease on a plain network was accepted: %d", rec.Code)
	}

	// update-ip moves the lease, under the same validation.
	if rec, _ := call(t, h, "PUT", "/v2/private-network/"+pnID+":update-ip",
		`{"instance":{"id":"`+first+`"},"ip":"10.90.0.77"}`); rec.Code != http.StatusOK {
		t.Fatalf("update-ip inside the range was refused: %d", rec.Code)
	}
	_, moved := call(t, h, "GET", "/v2/private-network/"+pnID, "")
	movedLease, _ := moved["leases"].([]any)[0].(map[string]any)
	if movedLease["ip"] != "10.90.0.77" {
		t.Errorf("the lease is %v after update-ip, want 10.90.0.77", movedLease["ip"])
	}
	if rec, _ := call(t, h, "PUT", "/v2/private-network/"+pnID+":update-ip",
		`{"instance":{"id":"`+second+`"},"ip":"10.90.0.99"}`); rec.Code != http.StatusBadRequest {
		t.Errorf("update-ip for an unattached instance was accepted: %d", rec.Code)
	}
}

// A network an instance still sits on does not go, and the range cannot slide
// out from under a held lease. Both are this emulator's own refusals — no
// recording shows the real API's — and both exist for the same reason: an
// attached instance must never publish a reference to a ghost, or an address
// outside every declared range.
func TestANetworkStillAttachedRefusesItsDelete(t *testing.T) {
	h := serve(t)
	pnID := createTestNetwork(t, h, managedNetworkBody, "pn-managed")
	instID := createTestInstance(t, h, "worker")
	call(t, h, "PUT", "/v2/private-network/"+pnID+":attach", `{"instance":{"id":"`+instID+`"}}`)

	if rec, _ := call(t, h, "DELETE", "/v2/private-network/"+pnID, ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("the delete of an attached network answered %d, want a refusal", rec.Code)
	}
	if rec, _ := call(t, h, "PUT", "/v2/private-network/"+pnID,
		`{"start-ip":"10.91.0.20","end-ip":"10.91.0.200","netmask":"255.255.255.0"}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("the range moved under a held lease: %d", rec.Code)
	}

	call(t, h, "PUT", "/v2/private-network/"+pnID+":detach", `{"instance":{"id":"`+instID+`"}}`)
	if rec, _ := call(t, h, "DELETE", "/v2/private-network/"+pnID, ""); rec.Code != http.StatusOK {
		t.Fatalf("the delete of a detached network answered %d", rec.Code)
	}
	if rec, _ := call(t, h, "GET", "/v2/private-network/"+pnID, ""); rec.Code != http.StatusNotFound {
		t.Errorf("the network survived its delete: %d", rec.Code)
	}
}

// reset-private-network-field serves the one field their API description
// enumerates, and refuses the rest rather than guessing what clearing them
// would mean.
func TestResetFieldClearsLabelsAlone(t *testing.T) {
	h := serve(t)
	pnID := createTestNetwork(t, h,
		`{"name":"pn-labelled","labels":{"team":"conformance"},"options":{}}`, "pn-labelled")

	_, before := call(t, h, "GET", "/v2/private-network/"+pnID, "")
	if labels, _ := before["labels"].(map[string]any); labels["team"] != "conformance" {
		t.Fatalf("the label did not round-trip: %v", before["labels"])
	}
	if rec, _ := call(t, h, "DELETE", "/v2/private-network/"+pnID+"/labels", ""); rec.Code != http.StatusOK {
		t.Fatalf("reset labels answered %d", rec.Code)
	}
	_, after := call(t, h, "GET", "/v2/private-network/"+pnID, "")
	if _, still := after["labels"]; still {
		t.Errorf("labels survived their reset: %v", after["labels"])
	}
	if rec, _ := call(t, h, "DELETE", "/v2/private-network/"+pnID+"/name", ""); rec.Code != http.StatusBadRequest {
		t.Errorf("resetting a field their description does not enumerate was accepted: %d", rec.Code)
	}
}

// An external source is a block a firewall is meant to match: it must parse as
// one, it round-trips on the group, and the key is absent when the last one
// goes — the omission rule measured on this API's groups.
func TestAnExternalSourceMustBeACIDR(t *testing.T) {
	h := serve(t)
	call(t, h, "POST", "/v2/security-group", `{"name":"sg-sources"}`)
	_, listed := call(t, h, "GET", "/v2/security-group", "")
	id := ""
	for _, entry := range listed["security-groups"].([]any) {
		sg, _ := entry.(map[string]any)
		if sg["name"] == "sg-sources" {
			id = sg["id"].(string)
		}
	}
	if id == "" {
		t.Fatal("the group is not in the list after create")
	}

	for _, bad := range []string{`{"cidr":"not-a-block"}`, `{"cidr":"203.0.113.7"}`, `{}`} {
		if rec, _ := call(t, h, "PUT", "/v2/security-group/"+id+":add-source", bad); rec.Code != http.StatusBadRequest {
			t.Errorf("%s was accepted as an external source", bad)
		}
	}
	if rec, _ := call(t, h, "PUT", "/v2/security-group/"+id+":add-source",
		`{"cidr":"203.0.113.0/24"}`); rec.Code != http.StatusOK {
		t.Fatalf("a well-formed source was refused: %d", rec.Code)
	}
	_, sg := call(t, h, "GET", "/v2/security-group/"+id, "")
	sources, _ := sg["external-sources"].([]any)
	if len(sources) != 1 || sources[0] != "203.0.113.0/24" {
		t.Fatalf("the source did not round-trip: %v", sg["external-sources"])
	}
	if rec, _ := call(t, h, "PUT", "/v2/security-group/"+id+":remove-source",
		`{"cidr":"203.0.113.0/24"}`); rec.Code != http.StatusOK {
		t.Fatalf("removing the source was refused: %d", rec.Code)
	}
	_, after := call(t, h, "GET", "/v2/security-group/"+id, "")
	if _, still := after["external-sources"]; still {
		t.Errorf("external-sources is present after the last one went: %v", after["external-sources"])
	}
}

// Two concurrent creates must not share a VXLAN id, and two concurrent
// attaches must not share a lease: both allocations are read-modify-write over
// the store, the exact shape the barrage of #134 caught on this pack's elastic
// pool. Both halves fail without p.lockAddresses() held across the read and the
// write.
func TestConcurrentAllocationsShareNothing(t *testing.T) {
	h := serve(t)

	const workers = 8
	var wg sync.WaitGroup
	for i := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			callRaw(h, "POST", "/v2/private-network", fmt.Sprintf(`{
				"name": "pn-%d",
				"start-ip": "10.%d.0.20", "end-ip": "10.%d.0.200",
				"netmask": "255.255.255.0", "options": {}
			}`, i, 100+i, 100+i))
		}()
	}
	wg.Wait()

	_, listed := call(t, h, "GET", "/v2/private-network", "")
	networks, _ := listed["private-networks"].([]any)
	if len(networks) != workers {
		t.Fatalf("%d networks out of %d creates", len(networks), workers)
	}
	seen := map[float64]string{}
	target := ""
	for _, entry := range networks {
		pn, _ := entry.(map[string]any)
		vni, _ := pn["vni"].(float64)
		if holder, taken := seen[vni]; taken {
			t.Errorf("vni %v handed to both %v and %v", vni, holder, pn["name"])
		}
		seen[vni] = pn["name"].(string)
		if pn["name"] == "pn-0" {
			target = pn["id"].(string)
		}
	}

	ids := make([]string, workers)
	for i := range workers {
		ids[i] = createTestInstance(t, h, fmt.Sprintf("racer-%d", i))
	}
	for _, id := range ids {
		wg.Add(1)
		go func() {
			defer wg.Done()
			callRaw(h, "PUT", "/v2/private-network/"+target+":attach",
				`{"instance":{"id":"`+id+`"}}`)
		}()
	}
	wg.Wait()

	_, pn := call(t, h, "GET", "/v2/private-network/"+target, "")
	leases, _ := pn["leases"].([]any)
	if len(leases) != workers {
		t.Fatalf("%d leases out of %d attaches", len(leases), workers)
	}
	held := map[string]string{}
	for _, entry := range leases {
		lease, _ := entry.(map[string]any)
		ip, _ := lease["ip"].(string)
		holder, _ := lease["instance-id"].(string)
		if first, taken := held[ip]; taken {
			t.Errorf("address %s leased to both %s and %s", ip, first, holder)
		}
		held[ip] = holder
	}
}
