package scaleway_test

import (
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/scaleway"
)

// The tests of this file ask with values that are NOT the defaults, and that is
// the point, not a detail: #277's whole class survived every suite because a
// list sorted by nothing and a list sorted by created_at_asc are
// indistinguishable when the suite only ever asks for the default, exactly as
// a constant and a datum are indistinguishable when only defaults are created.
// Asking for `_desc` against a suite-visible creation order is what tells an
// honoured order_by from a dropped one.

// newTickingTestServer is newTestServer with a clock that advances on every
// read: ordering by created_at needs two resources whose timestamps differ,
// and the pinned clock of newTestServer makes every sort a no-op.
func newTickingTestServer(t testing.TB) *httptest.Server {
	t.Helper()

	var seq int
	var ticks int64
	env := &emulator.Env{
		Store: store.New(),
		Now: func() time.Time {
			ticks++
			return time.Unix(1700000000+ticks, 0).UTC()
		},
		NewID: func() string {
			seq++
			return fmt.Sprintf("00000000-0000-4000-8000-%012d", seq)
		},
	}
	srv, err := emulator.NewServer(env, scaleway.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// names reads the `name` field of every record under key, in answer order.
func names(t *testing.T, body map[string]any, key string) []string {
	t.Helper()
	records, _ := body[key].([]any)
	out := make([]string, 0, len(records))
	for _, raw := range records {
		record, _ := raw.(map[string]any)
		name, _ := record["name"].(string)
		out = append(out, name)
	}
	return out
}

func wantOrder(t *testing.T, got, want []string, query string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("%s answered %d records (%v), want %d", query, len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("%s answered %v, want %v", query, got, want)
		}
	}
}

// TestServersHonourTheDeclaredOrder covers instance/v1's spelling — `order`,
// not `order_by` — and the SDK's default, creation_date_desc: a bare list must
// answer newest first, because that is what the real API does and what the
// non-pointer enum's empty value on the wire (`order=`) means.
func TestServersHonourTheDeclaredOrder(t *testing.T) {
	ts := newTickingTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	for _, name := range []string{"banana", "apple", "cherry"} {
		body := fmt.Sprintf(`{"name":%q,"commercial_type":"DEV1-S"}`, name)
		if status, out := do(t, ts, "POST", zone+"/servers", body); status != http.StatusCreated {
			t.Fatalf("create %s: status %d (%v)", name, status, out)
		}
	}

	cases := []struct {
		query string
		want  []string
	}{
		// The default: newest first, without being asked.
		{"", []string{"cherry", "apple", "banana"}},
		{"?order=creation_date_asc", []string{"banana", "apple", "cherry"}},
		{"?order=creation_date_desc", []string{"cherry", "apple", "banana"}},
		// The empty value every instance/v1 client sends is the default too.
		{"?order=", []string{"cherry", "apple", "banana"}},
	}
	for _, tc := range cases {
		status, body := do(t, ts, "GET", zone+"/servers"+tc.query, "")
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", tc.query, status)
		}
		wantOrder(t, names(t, body, "servers"), tc.want, tc.query)
	}

	if status, _ := do(t, ts, "GET", zone+"/servers?order=alphabetical", ""); status != http.StatusBadRequest {
		t.Fatalf("an order outside the enum answered %d, want 400", status)
	}
}

// TestServersFilterByLinks covers the ListServers filters that reach beyond
// the record: servers, with_ip, without_ip, private_network(s) and
// private_nic_mac_address. Every one of them was declared and dropped (#277).
func TestServersFilterByLinks(t *testing.T) {
	ts := newTickingTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"
	const region = "/vpc/v2/regions/fr-par"

	// A flexible IP, attached at create to the first server.
	status, out := do(t, ts, "POST", zone+"/ips", `{"type":"routed_ipv4"}`)
	if status != http.StatusCreated {
		t.Fatalf("create ip: status %d (%v)", status, out)
	}
	ip, _ := out["ip"].(map[string]any)
	address, _ := ip["address"].(string)
	ipID, _ := ip["id"].(string)

	status, out = do(t, ts, "POST", zone+"/servers",
		fmt.Sprintf(`{"name":"with-ip","commercial_type":"DEV1-S","public_ip":%q}`, ipID))
	if status != http.StatusCreated {
		t.Fatalf("create with-ip: status %d (%v)", status, out)
	}
	server, _ := out["server"].(map[string]any)
	withIPID, _ := server["id"].(string)

	status, out = do(t, ts, "POST", zone+"/servers", `{"name":"bare","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create bare: status %d", status)
	}
	server, _ = out["server"].(map[string]any)
	bareID, _ := server["id"].(string)

	// A Private Network and a NIC on the bare server.
	status, out = do(t, ts, "POST", region+"/private-networks", `{"name":"pn"}`)
	if status != http.StatusOK {
		t.Fatalf("create pn: status %d (%v)", status, out)
	}
	pnID, _ := out["id"].(string)

	status, out = do(t, ts, "POST", zone+"/servers/"+bareID+"/private_nics",
		fmt.Sprintf(`{"private_network_id":%q}`, pnID))
	if status != http.StatusCreated {
		t.Fatalf("create nic: status %d (%v)", status, out)
	}
	nic, _ := out["private_nic"].(map[string]any)
	mac, _ := nic["mac_address"].(string)
	if mac == "" {
		t.Fatalf("the NIC came back without a MAC: %v", out)
	}

	cases := []struct {
		query string
		want  []string
	}{
		{"?servers=" + withIPID, []string{"with-ip"}},
		{"?servers=" + withIPID + "," + bareID, []string{"bare", "with-ip"}},
		{"?with_ip=" + address, []string{"with-ip"}},
		{"?with_ip=198.51.100.77", []string{}},
		{"?without_ip=true", []string{"bare"}},
		// The undocumented direction filters nothing, by decision (the SDK
		// documents only true).
		{"?without_ip=false", []string{"bare", "with-ip"}},
		{"?private_network=" + pnID, []string{"bare"}},
		{"?private_networks=" + pnID, []string{"bare"}},
		{"?private_nic_mac_address=" + mac, []string{"bare"}},
		{"?private_nic_mac_address=aa:bb:cc:dd:ee:ff", []string{}},
	}
	for _, tc := range cases {
		status, body := do(t, ts, "GET", zone+"/servers"+tc.query, "")
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", tc.query, status)
		}
		wantOrder(t, names(t, body, "servers"), tc.want, tc.query)
	}
}

// TestBlockListsHonourTheDeclaredFilters covers block/v1's ListVolumes and
// ListSnapshots: order_by with both fields, and the volume_ids, volume_id,
// project_id, tags, product_resource_id, volume_type and include_deleted
// filters — 24 declared parameters, every one dropped before #277.
func TestBlockListsHonourTheDeclaredFilters(t *testing.T) {
	ts := newTickingTestServer(t)
	const zone = "/block/v1/zones/fr-par-1"

	ids := map[string]string{}
	for _, name := range []string{"banana", "apple"} {
		body := fmt.Sprintf(`{"name":%q,"from_empty":{"size":10000000000},"tags":["team-%s"]}`, name, name)
		status, out := do(t, ts, "POST", zone+"/volumes", body)
		// 200, not 201: measured on the wire against a real fr-par account on
		// 2026-08-24, see blockCreateStatus.
		if status != http.StatusOK {
			t.Fatalf("create %s: status %d (%v)", name, status, out)
		}
		id, _ := out["id"].(string)
		ids[name] = id
	}
	status, out := do(t, ts, "POST", zone+"/snapshots",
		fmt.Sprintf(`{"name":"snap-banana","volume_id":%q}`, ids["banana"]))
	if status != http.StatusOK {
		t.Fatalf("create snapshot: status %d (%v)", status, out)
	}

	volumeCases := []struct {
		query string
		want  []string
	}{
		{"", []string{"banana", "apple"}}, // created_at_asc is the SDK default
		{"?order_by=created_at_desc", []string{"apple", "banana"}},
		{"?order_by=name_asc", []string{"apple", "banana"}},
		{"?order_by=name_desc", []string{"banana", "apple"}},
		{"?volume_ids=" + ids["apple"], []string{"apple"}},
		{"?tags=team-banana", []string{"banana"}},
		{"?tags=team-banana&tags=team-apple", []string{"banana", "apple"}}, // any-match
		{"?project_id=00000000-0000-4000-9000-000000000001", []string{}},
		{"?product_resource_id=00000000-dead-4000-9000-000000000001", []string{}},
		{"?volume_type=scratch", []string{}},
		{"?volume_type=sbs", []string{"banana", "apple"}},
		// Deletion is immediate here, so both values answer the same list —
		// served, with the difference recorded in docs/limits.md.
		{"?include_deleted=true", []string{"banana", "apple"}},
	}
	for _, tc := range volumeCases {
		status, body := do(t, ts, "GET", zone+"/volumes"+tc.query, "")
		if status != http.StatusOK {
			t.Fatalf("volumes%s: status %d", tc.query, status)
		}
		wantOrder(t, names(t, body, "volumes"), tc.want, "volumes"+tc.query)
	}
	if status, _ := do(t, ts, "GET", zone+"/volumes?order_by=size_desc", ""); status != http.StatusBadRequest {
		t.Fatalf("an order_by outside the enum answered %d, want 400", status)
	}

	snapshotCases := []struct {
		query string
		want  []string
	}{
		{"?volume_id=" + ids["banana"], []string{"snap-banana"}},
		{"?volume_id=" + ids["apple"], []string{}},
		{"?order_by=name_desc", []string{"snap-banana"}},
	}
	for _, tc := range snapshotCases {
		status, body := do(t, ts, "GET", zone+"/snapshots"+tc.query, "")
		if status != http.StatusOK {
			t.Fatalf("snapshots%s: status %d", tc.query, status)
		}
		wantOrder(t, names(t, body, "snapshots"), tc.want, "snapshots"+tc.query)
	}
}

// TestVPCListsHonourTheDeclaredFilters covers ListVPCs and
// ListPrivateNetworks: order_by, name, is_default, routing_enabled,
// dhcp_enabled, private_network_ids and the s3_integration_enabled decision.
func TestVPCListsHonourTheDeclaredFilters(t *testing.T) {
	ts := newTickingTestServer(t)
	const region = "/vpc/v2/regions/fr-par"

	// enable_routing is spelled out here, and false on purpose: since #497 a
	// VPC created without the field routes like the real cloud's, so an
	// omitted field would put both VPCs on the same side of the filter below
	// and the routing_enabled cases would stop separating anything.
	status, out := do(t, ts, "POST", region+"/vpcs", `{"name":"team","enable_routing":false}`)
	if status != http.StatusOK {
		t.Fatalf("create vpc: status %d (%v)", status, out)
	}
	status, out = do(t, ts, "POST", region+"/private-networks", `{"name":"pn-a","tags":["blue"]}`)
	if status != http.StatusOK {
		t.Fatalf("create pn-a: status %d (%v)", status, out)
	}
	pnA, _ := out["id"].(string)
	if status, _ = do(t, ts, "POST", region+"/private-networks", `{"name":"pn-b"}`); status != http.StatusOK {
		t.Fatalf("create pn-b: status %d", status)
	}

	vpcCases := []struct {
		query string
		want  []string
	}{
		// "team" is created above; "default" is lazily provisioned by the
		// first list, so it is the newer of the two.
		{"", []string{"team", "default"}},
		{"?order_by=created_at_desc", []string{"default", "team"}},
		{"?order_by=name_desc", []string{"team", "default"}},
		{"?is_default=true", []string{"default"}},
		{"?is_default=false", []string{"team"}},
		{"?name=tea", []string{"team"}},
		// "team" asked for enable_routing:false; the lazily provisioned
		// default VPC routes, like upstream's — and so does every VPC created
		// without the field (#497).
		{"?routing_enabled=false", []string{"team"}},
		{"?routing_enabled=true", []string{"default"}},
		{"?s3_integration_enabled=true", []string{}},
		{"?s3_integration_enabled=false", []string{"team", "default"}},
	}
	for _, tc := range vpcCases {
		status, body := do(t, ts, "GET", region+"/vpcs"+tc.query, "")
		if status != http.StatusOK {
			t.Fatalf("vpcs%s: status %d", tc.query, status)
		}
		wantOrder(t, names(t, body, "vpcs"), tc.want, "vpcs"+tc.query)
	}

	pnCases := []struct {
		query string
		want  []string
	}{
		{"?order_by=created_at_desc", []string{"pn-b", "pn-a"}},
		{"?order_by=name_asc", []string{"pn-a", "pn-b"}},
		{"?private_network_ids=" + pnA, []string{"pn-a"}},
		{"?tags=blue", []string{"pn-a"}},
		{"?name=pn-b", []string{"pn-b"}},
		{"?dhcp_enabled=false", []string{}},
		{"?s3_integration_enabled=true", []string{}},
	}
	for _, tc := range pnCases {
		status, body := do(t, ts, "GET", region+"/private-networks"+tc.query, "")
		if status != http.StatusOK {
			t.Fatalf("private-networks%s: status %d", tc.query, status)
		}
		wantOrder(t, names(t, body, "private_networks"), tc.want, "private-networks"+tc.query)
	}

	// Subnets order by created_at only, and a subnet's created_at is its
	// network's: descending answers pn-b's records before pn-a's.
	status, body := do(t, ts, "GET", region+"/subnets?order_by=created_at_desc", "")
	if status != http.StatusOK {
		t.Fatalf("subnets: status %d", status)
	}
	subnets, _ := body["subnets"].([]any)
	if len(subnets) < 2 {
		t.Fatalf("subnets answered %d records, want at least 2", len(subnets))
	}
	first, _ := subnets[0].(map[string]any)
	if first["private_network_id"] != pnA {
		// pn-b is newer, so descending puts its subnets first; pn-a's last.
		last, _ := subnets[len(subnets)-1].(map[string]any)
		if last["private_network_id"] != pnA {
			t.Fatalf("subnets?order_by=created_at_desc did not group pn-a last: %v", subnets)
		}
	} else {
		t.Fatalf("subnets?order_by=created_at_desc answered oldest first")
	}
	if status, _ := do(t, ts, "GET", region+"/subnets?order_by=updated_at_desc", ""); status != http.StatusBadRequest {
		t.Fatalf("subnets with an order outside the enum answered %d, want 400", status)
	}
}

// The Object Storage filter has had two names, and both must narrow.
//
// Scaleway renamed the family on 2026-08-25 — the five S3-endpoint operations
// became *ObjectStoragePrivateAccess and the query parameter went with them.
// The SDK emits object_storage_private_access_enabled today and the portal's
// document declares only that spelling; every client built before the rename
// still sends the old one. A handler reading one spelling answers a filtered
// list with everything, which is the #277 class seen from a rename rather than
// from an omission.
func TestBothSpellingsOfTheObjectStorageFilterNarrow(t *testing.T) {
	ts := newTickingTestServer(t)
	const region = "/vpc/v2/regions/fr-par"

	if status, out := do(t, ts, "POST", region+"/vpcs", `{"name":"team"}`); status != http.StatusOK {
		t.Fatalf("create vpc: status %d (%v)", status, out)
	}
	if status, out := do(t, ts, "POST", region+"/private-networks", `{"name":"pn-a"}`); status != http.StatusOK {
		t.Fatalf("create pn-a: status %d (%v)", status, out)
	}

	for _, spelling := range []string{"object_storage_private_access_enabled", "s3_integration_enabled"} {
		status, body := do(t, ts, "GET", region+"/vpcs?"+spelling+"=true", "")
		if status != http.StatusOK {
			t.Fatalf("vpcs?%s=true: status %d", spelling, status)
		}
		if vpcs, _ := body["vpcs"].([]any); len(vpcs) != 0 {
			t.Errorf("vpcs?%s=true answered %d VPC(s); nothing here has Object Storage private access", spelling, len(vpcs))
		}

		status, body = do(t, ts, "GET", region+"/private-networks?"+spelling+"=true", "")
		if status != http.StatusOK {
			t.Fatalf("private-networks?%s=true: status %d", spelling, status)
		}
		if pns, _ := body["private_networks"].([]any); len(pns) != 0 {
			t.Errorf("private-networks?%s=true answered %d network(s)", spelling, len(pns))
		}

		// The narrowing must be the filter's doing and not an empty store.
		status, body = do(t, ts, "GET", region+"/vpcs?"+spelling+"=false", "")
		if status != http.StatusOK {
			t.Fatalf("vpcs?%s=false: status %d", spelling, status)
		}
		if vpcs, _ := body["vpcs"].([]any); len(vpcs) == 0 {
			t.Errorf("vpcs?%s=false answered nothing, so the =true case proves nothing", spelling)
		}
	}
}

// TestIPAMListHonoursOrderAndResourceFilters covers ipam/v1's order_by —
// including the numeric ip_address order and the attached_at refusal — and
// the resource_ids, resource_types and resource_name filters.
func TestIPAMListHonoursOrderAndResourceFilters(t *testing.T) {
	ts := newTickingTestServer(t)
	const region = "/ipam/v1/regions/fr-par"
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", "/vpc/v2/regions/fr-par/private-networks", `{"name":"pn"}`)
	if status != http.StatusOK {
		t.Fatalf("create pn: status %d (%v)", status, out)
	}
	pnID, _ := out["id"].(string)

	// One NIC-held address...
	status, out = do(t, ts, "POST", zone+"/servers", `{"name":"srv","commercial_type":"DEV1-S"}`)
	if status != http.StatusCreated {
		t.Fatalf("create server: status %d", status)
	}
	server, _ := out["server"].(map[string]any)
	serverID, _ := server["id"].(string)
	status, out = do(t, ts, "POST", zone+"/servers/"+serverID+"/private_nics",
		fmt.Sprintf(`{"private_network_id":%q}`, pnID))
	if status != http.StatusCreated {
		t.Fatalf("create nic: status %d (%v)", status, out)
	}
	nic, _ := out["private_nic"].(map[string]any)
	nicID, _ := nic["id"].(string)

	// ...and one booked for a custom resource, whose holder carries a name.
	book := fmt.Sprintf(`{"source":{"private_network_id":%q},"resource":{"mac_address":"02:00:00:aa:bb:01","name":"appliance-7"}}`, pnID)
	if status, out = do(t, ts, "POST", region+"/ips", book); status != http.StatusOK {
		t.Fatalf("book ip: status %d (%v)", status, out)
	}

	count := func(query string) int {
		t.Helper()
		status, body := do(t, ts, "GET", region+"/ips"+query, "")
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", query, status)
		}
		ips, _ := body["ips"].([]any)
		return len(ips)
	}

	if got := count("?resource_ids=" + nicID); got != 1 {
		t.Fatalf("resource_ids matched %d addresses, want 1", got)
	}
	if got := count("?resource_ids=00000000-dead-4000-9000-000000000001"); got != 0 {
		t.Fatalf("resource_ids of nothing matched %d addresses, want 0", got)
	}
	if got := count("?resource_types=custom"); got != 1 {
		t.Fatalf("resource_types=custom matched %d addresses, want 1", got)
	}
	if got := count("?resource_types=instance_private_nic&resource_types=custom"); got != 2 {
		t.Fatalf("two resource_types matched %d addresses, want 2", got)
	}
	if got := count("?resource_name=appliance"); got != 1 {
		t.Fatalf("resource_name matched %d addresses, want 1 (the named custom holder)", got)
	}
	if got := count("?resource_name=nothing-called-this"); got != 0 {
		t.Fatalf("resource_name of nothing matched %d addresses, want 0", got)
	}

	// The addresses are handed out in ascending order, so ip_address_desc must
	// answer the booked (second, higher) address first.
	status, body := do(t, ts, "GET", region+"/ips?order_by=ip_address_desc", "")
	if status != http.StatusOK {
		t.Fatalf("order_by=ip_address_desc: status %d", status)
	}
	ips, _ := body["ips"].([]any)
	if len(ips) != 2 {
		t.Fatalf("listed %d addresses, want 2", len(ips))
	}
	firstIP, _ := ips[0].(map[string]any)
	secondIP, _ := ips[1].(map[string]any)
	if firstIP["address"].(string) <= secondIP["address"].(string) {
		t.Fatalf("ip_address_desc answered %v before %v", firstIP["address"], secondIP["address"])
	}

	// attached_at is refused, not served under a stand-in: this emulator
	// records no attachment time (docs/limits.md).
	if status, _ := do(t, ts, "GET", region+"/ips?order_by=attached_at_desc", ""); status != http.StatusBadRequest {
		t.Fatalf("order_by=attached_at_desc answered %d, want 400", status)
	}
}

// TestInstanceListsHonourTheirRemainingFilters sweeps the smaller instance/v1
// drops: IPs (name, type, tags), volumes (volume_type, tags), security groups
// (organization, project_default, tags), NICs (tags), images (arch, public,
// tags) and snapshots (base_volume_id, paging).
func TestInstanceListsHonourTheirRemainingFilters(t *testing.T) {
	ts := newTickingTestServer(t)
	const zone = "/instance/v1/zones/fr-par-1"

	status, out := do(t, ts, "POST", zone+"/ips", `{"tags":["edge"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create ip: status %d", status)
	}
	ip, _ := out["ip"].(map[string]any)
	address, _ := ip["address"].(string)

	count := func(path, key, query string) int {
		t.Helper()
		status, body := do(t, ts, "GET", zone+path+query, "")
		if status != http.StatusOK {
			t.Fatalf("%s%s: status %d", path, query, status)
		}
		records, _ := body[key].([]any)
		return len(records)
	}

	// IPs: name is a LIKE on the address, type an equality, tags exact.
	if got := count("/ips", "ips", "?name="+address[:7]); got != 1 {
		t.Fatalf("ips?name= matched %d, want 1", got)
	}
	if got := count("/ips", "ips", "?type=routed_ipv6"); got != 0 {
		t.Fatalf("ips?type=routed_ipv6 matched %d, want 0", got)
	}
	if got := count("/ips", "ips", "?type=routed_ipv4"); got != 1 {
		t.Fatalf("ips?type=routed_ipv4 matched %d, want 1", got)
	}
	if got := count("/ips", "ips", "?tags=edge"); got != 1 {
		t.Fatalf("ips?tags=edge matched %d, want 1", got)
	}
	if got := count("/ips", "ips", "?tags=edge,absent"); got != 0 {
		t.Fatalf("ips?tags=edge,absent matched %d, want 0 (exact tags conjoin)", got)
	}

	// Volumes: volume_type and tags.
	status, _ = do(t, ts, "POST", zone+"/volumes", `{"name":"data","volume_type":"l_ssd","size":10000000000,"tags":["gold"]}`)
	if status != http.StatusCreated {
		t.Fatalf("create volume: status %d", status)
	}
	// scratch, because the type that answers nothing here has to be one the
	// filter would happily match: l_ssd played that part until #393 made it the
	// type this create asks for.
	if got := count("/volumes", "volumes", "?volume_type=scratch"); got != 0 {
		t.Fatalf("volumes?volume_type=scratch matched %d, want 0", got)
	}
	if got := count("/volumes", "volumes", "?volume_type=l_ssd"); got != 1 {
		t.Fatalf("volumes?volume_type=l_ssd matched %d, want 1", got)
	}
	if got := count("/volumes", "volumes", "?tags=gold"); got != 1 {
		t.Fatalf("volumes?tags=gold matched %d, want 1", got)
	}

	// Security groups: the lazily provisioned default answers
	// project_default=true. An organization filter scopes to the whole
	// account — whatever identifier the client's configuration carries, it
	// names the one account living here, never an empty answer
	// (`scw iam ssh-key list` proved the equality reading wrong: the CLI
	// names its configured organization on every list).
	if got := count("/security_groups", "security_groups", "?project_default=true"); got != 1 {
		t.Fatalf("security_groups?project_default=true matched %d, want 1", got)
	}
	if got := count("/security_groups", "security_groups", "?project_default=false"); got != 0 {
		t.Fatalf("security_groups?project_default=false matched %d, want 0", got)
	}
	if got := count("/security_groups", "security_groups", "?organization=00000000-dead-4000-9000-000000000001"); got != 1 {
		t.Fatalf("security_groups under a client-named organization matched %d, want 1 (the account's)", got)
	}
	if got := count("/security_groups", "security_groups", "?tags=absent"); got != 0 {
		t.Fatalf("security_groups?tags=absent matched %d, want 0", got)
	}

	// Images: the catalogue is public and x86_64.
	if got := count("/images", "images", "?arch=arm64"); got != 0 {
		t.Fatalf("images?arch=arm64 matched %d, want 0", got)
	}
	if got := count("/images", "images", "?public=false"); got != 0 {
		t.Fatalf("images?public=false matched %d, want 0 (no client image was cut)", got)
	}
	if all := count("/images", "images", "?public=true"); all == 0 {
		t.Fatalf("images?public=true matched nothing, want the catalogue")
	}
	if got := count("/images", "images", "?tags=absent"); got != 0 {
		t.Fatalf("images?tags=absent matched %d, want 0", got)
	}

	// Snapshots: base_volume_id narrows to the volume's own snapshots, and the
	// list pages — it used to answer everything, whatever per_page said.
	status, out = do(t, ts, "GET", zone+"/volumes", "")
	if status != http.StatusOK {
		t.Fatalf("list volumes: status %d", status)
	}
	volumes, _ := out["volumes"].([]any)
	volume, _ := volumes[0].(map[string]any)
	volumeID, _ := volume["id"].(string)
	for _, name := range []string{"s1", "s2"} {
		body := fmt.Sprintf(`{"name":%q,"volume_id":%q}`, name, volumeID)
		if status, _ := do(t, ts, "POST", zone+"/snapshots", body); status != http.StatusCreated {
			t.Fatalf("create snapshot %s: status %d", name, status)
		}
	}
	if got := count("/snapshots", "snapshots", "?base_volume_id="+volumeID); got != 2 {
		t.Fatalf("snapshots?base_volume_id matched %d, want 2", got)
	}
	if got := count("/snapshots", "snapshots", "?base_volume_id=00000000-dead-4000-9000-000000000001"); got != 0 {
		t.Fatalf("snapshots of another volume matched %d, want 0", got)
	}
	if got := count("/snapshots", "snapshots", "?per_page=1"); got != 1 {
		t.Fatalf("snapshots?per_page=1 answered %d records, want 1", got)
	}
	status, out = do(t, ts, "GET", zone+"/snapshots", "")
	if status != http.StatusOK {
		t.Fatalf("list snapshots: status %d", status)
	}
	if total, _ := out["total_count"].(float64); int(total) != 2 {
		t.Fatalf("snapshots total_count is %v, want 2", out["total_count"])
	}
}

// TestSSHKeysHonourTheDeclaredParameters covers iam/v1alpha1: order_by with a
// non-default value, and the disabled, name and organization_id filters.
func TestSSHKeysHonourTheDeclaredParameters(t *testing.T) {
	ts := newTickingTestServer(t)
	const path = "/iam/v1alpha1/ssh-keys"

	for _, name := range []string{"banana", "apple"} {
		body := fmt.Sprintf(`{"name":%q,"public_key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIBRuRQZ0eGdzCsIYIbHHDJdZTMEcCEbUEyKJlEUDb25B %s@feint"}`, name, name)
		status, out := do(t, ts, "POST", path, body)
		// 200, measured on the wire: see sshKeyCreateStatus.
		if status != http.StatusOK {
			t.Fatalf("create %s: status %d (%v)", name, status, out)
		}
	}

	cases := []struct {
		query string
		want  []string
	}{
		{"", []string{"banana", "apple"}}, // created_at_asc is the SDK default
		{"?order_by=created_at_desc", []string{"apple", "banana"}},
		{"?order_by=name_asc", []string{"apple", "banana"}},
		{"?name=ban", []string{"banana"}},
		{"?disabled=true", []string{}},
		{"?disabled=false", []string{"banana", "apple"}},
		// An organization names the one account living here, whatever
		// identifier the client's configuration carries: the CLI sends its
		// configured organization_id on every list, and an equality against
		// the pack's constant answered it an empty list about its own keys.
		{"?organization_id=00000000-dead-4000-9000-000000000001", []string{"banana", "apple"}},
	}
	for _, tc := range cases {
		status, body := do(t, ts, "GET", path+tc.query, "")
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", tc.query, status)
		}
		wantOrder(t, names(t, body, "ssh_keys"), tc.want, tc.query)
	}
	if status, _ := do(t, ts, "GET", path+"?order_by=fingerprint_asc", ""); status != http.StatusBadRequest {
		t.Fatalf("an order_by outside the enum answered %d, want 400", status)
	}
}

// TestLocalImagesHonourTheirDeclaredParameters covers the marketplace lookup:
// arch and type as truthful equalities, the page window, and the enum check on
// order_by — a list of one is every order, but an unknown value is still an
// unknown value.
func TestLocalImagesHonourTheirDeclaredParameters(t *testing.T) {
	ts := newTickingTestServer(t)
	const path = "/marketplace/v2/local-images"

	count := func(query string) int {
		t.Helper()
		status, body := do(t, ts, "GET", path+query, "")
		if status != http.StatusOK {
			t.Fatalf("%s: status %d", query, status)
		}
		images, _ := body["local_images"].([]any)
		return len(images)
	}

	if got := count("?image_label=ubuntu_noble"); got != 1 {
		t.Fatalf("the bare lookup answered %d images, want 1", got)
	}
	if got := count("?image_label=ubuntu_noble&arch=arm64"); got != 0 {
		t.Fatalf("arch=arm64 answered %d images, want 0 — the catalogue is x86_64", got)
	}
	if got := count("?image_label=ubuntu_noble&type=instance_local"); got != 0 {
		t.Fatalf("type=instance_local answered %d images, want 0 — the catalogue is instance_sbs", got)
	}
	if got := count("?image_label=ubuntu_noble&type=unknown_type"); got != 1 {
		t.Fatalf("type=unknown_type is the enum's zero value, not a filter; answered %d, want 1", got)
	}
	if got := count("?image_label=ubuntu_noble&page=2"); got != 0 {
		t.Fatalf("page=2 answered %d images, want 0 — the same image again never ends the SDK's loop", got)
	}
	if status, _ := do(t, ts, "GET", path+"?order_by=label_asc", ""); status != http.StatusBadRequest {
		t.Fatalf("an order_by outside the enum answered %d, want 400", status)
	}
}
