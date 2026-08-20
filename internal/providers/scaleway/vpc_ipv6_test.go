package scaleway_test

import (
	"bytes"
	"io"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"testing"
)

// The IPv6 half of a Private Network (#270). Upstream every Private Network is
// dual-stack: creation allocates an IPv4 block and an IPv6 /64 without being
// asked, and both ride the one subnets list (vpc/v2 SDK, PrivateNetwork.Subnets).
// An emulator serving only the IPv4 half made the ordinary consumer expression,
// one(pn.ipv6_subnets).subnet, die on null — on apply and on destroy both.
//
// The fd00::/8 range and the /64 size are measured, not derived: a Private
// Network created on a real fr-par account on 2026-08-20, with nothing asked
// for, answered 172.16.4.0/22 and fdb2:1bb5:120a:9b::/64. The field tree of
// that read is committed in shapes/scaleway.json under
// `GET /vpc/v2/regions/fr-par/private-networks/{id}`.

// ipv6SubnetOf picks the IPv6 record out of a subnets list, the counterpart of
// ipv4SubnetOf.
func ipv6SubnetOf(t *testing.T, subnets []any) map[string]any {
	t.Helper()
	for _, raw := range subnets {
		s, _ := raw.(map[string]any)
		block, _ := s["subnet"].(string)
		if p, err := netip.ParsePrefix(block); err == nil && p.Addr().Is6() && !p.Addr().Is4In6() {
			return s
		}
	}
	t.Fatalf("no IPv6 subnet in %v", subnets)
	return nil
}

func TestPrivateNetworkPublishesAnIPv6Subnet(t *testing.T) {
	ts := newTestServer(t)
	ula := netip.MustParsePrefix("fd00::/8")

	id, created := privateNetwork(t, ts, `{"name":"dual"}`)
	subnets, _ := created["subnets"].([]any)
	if len(subnets) != 2 {
		t.Fatalf("expected an IPv4 and an IPv6 subnet, got %v", created["subnets"])
	}
	v6 := ipv6SubnetOf(t, subnets)

	block, _ := v6["subnet"].(string)
	prefix, err := netip.ParsePrefix(block)
	if err != nil {
		t.Fatalf("the IPv6 subnet %q does not parse: %v", block, err)
	}
	if prefix.Bits() != 64 {
		t.Errorf("expected a /64, got %s", prefix)
	}
	if !ula.Contains(prefix.Addr()) {
		t.Errorf("expected a unique-local fd… block, got %s", prefix)
	}

	// Two records, two identities: a client stores both ids.
	v4 := ipv4SubnetOf(t, subnets)
	if v6["id"] == v4["id"] || v6["id"] == id || v6["id"] == "" {
		t.Errorf("the IPv6 subnet has no identity of its own: v6 %v vs v4 %v", v6["id"], v4["id"])
	}
	if v6["private_network_id"] != id {
		t.Errorf("the IPv6 subnet does not name its network: %v", v6)
	}
	if v6["vpc_id"] == nil || v6["project_id"] == nil || v6["region"] != "fr-par" {
		t.Errorf("the IPv6 subnet is missing its scope: %v", v6)
	}

	// Identical between two reads: anything Terraform stores must be
	// deterministic, or the computed ipv6_subnets attribute is a permanent diff.
	for range 2 {
		status, got := do(t, ts, "GET", regionURL+"/private-networks/"+id, "")
		if status != http.StatusOK {
			t.Fatalf("read back: status %d", status)
		}
		reread := ipv6SubnetOf(t, got["subnets"].([]any))
		if reread["subnet"] != block || reread["id"] != v6["id"] {
			t.Fatalf("the IPv6 subnet changed between reads: %v vs %v", reread, v6)
		}
	}

	// The flat door serves the same record: the two doors cannot disagree.
	status, listed := do(t, ts, "GET", regionURL+"/subnets", "")
	if status != http.StatusOK {
		t.Fatalf("list subnets: status %d", status)
	}
	flat := ipv6SubnetOf(t, listed["subnets"].([]any))
	if flat["id"] != v6["id"] || flat["subnet"] != block {
		t.Errorf("the flat IPv6 subnet disagrees with the network's: %v vs %v", flat, v6)
	}
}

// Two networks of one project get two /64s out of one /48.
//
// Both halves are measured. On 2026-08-20 two Private Networks created in one
// real fr-par project answered fdb2:1bb5:120a:9b::/64 and
// fdb2:1bb5:120a:6cad::/64: distinct blocks, one global ID, which is RFC 4193's
// own layout. Seeded from the resource alone, as this pack first did, sibling
// networks landed in unrelated /48s — nothing wrong on the wire, and nothing in
// one tenancy that looked related to anything else in it.
func TestTwoNetworksGetTwoIPv6BlocksUnderOnePrefix(t *testing.T) {
	ts := newTestServer(t)

	_, first := privateNetwork(t, ts, `{"name":"one"}`)
	_, second := privateNetwork(t, ts, `{"name":"two"}`)
	a, _ := ipv6SubnetOf(t, first["subnets"].([]any))["subnet"].(string)
	b, _ := ipv6SubnetOf(t, second["subnets"].([]any))["subnet"].(string)
	if a == b {
		t.Fatalf("two networks share the IPv6 block %s", a)
	}

	within48 := func(block string) netip.Prefix {
		p, err := netip.ParsePrefix(block)
		if err != nil {
			t.Fatalf("the IPv6 subnet %q does not parse: %v", block, err)
		}
		return netip.PrefixFrom(p.Addr(), 48).Masked()
	}
	if within48(a) != within48(b) {
		t.Errorf("two networks of one project landed in %s and %s, which share no /48; "+
			"the real cloud gives a project one global ID and varies the subnet ID", a, b)
	}
}

func TestARequestedIPv6SubnetComesBackVerbatim(t *testing.T) {
	ts := newTestServer(t)

	_, created := privateNetwork(t, ts,
		`{"name":"chosen","subnets":["10.44.0.0/24","fd0c:1111:2222:3333::/64"]}`)
	v6 := ipv6SubnetOf(t, created["subnets"].([]any))
	if v6["subnet"] != "fd0c:1111:2222:3333::/64" {
		t.Fatalf("the requested IPv6 block came back as %v", v6["subnet"])
	}

	// The same block again overlaps a sibling's, exactly like the IPv4 path.
	status, body := do(t, ts, "POST", regionURL+"/private-networks",
		`{"name":"clash","subnets":["10.45.0.0/24","fd0c:1111:2222:3333::/64"]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("an overlapping IPv6 block was accepted: status %d (%v)", status, body)
	}

	// A mask that is not /64 is refused: the blocks upstream hands out are /64,
	// and a stored block the allocator cannot reason about is worse than a 400.
	status, body = do(t, ts, "POST", regionURL+"/private-networks",
		`{"name":"wide","subnets":["10.46.0.0/24","fd0d::/48"]}`)
	if status != http.StatusBadRequest {
		t.Fatalf("a /48 IPv6 block was accepted: status %d (%v)", status, body)
	}
}

// A snapshot is designed to be loaded into another instance, and the computed
// ipv6_subnets attribute Terraform stored must survive the trip: a block that
// changed across a restore would be a permanent diff in a configuration that
// did nothing.
func TestTheIPv6SubnetSurvivesASnapshotRoundTrip(t *testing.T) {
	first := newTestServer(t)

	id, created := privateNetwork(t, first, `{"name":"persistent"}`)
	before := ipv6SubnetOf(t, created["subnets"].([]any))

	snapshot := rawGet(t, first, "/_feint/state")

	// A fresh instance, the way an operator restores: through the served route.
	second := newTestServer(t)
	req, err := http.NewRequest(http.MethodPut, second.URL+"/_feint/state", bytes.NewReader(snapshot))
	if err != nil {
		t.Fatalf("build the restore: %v", err)
	}
	res, err := second.Client().Do(req)
	if err != nil {
		t.Fatalf("restore: %v", err)
	}
	_ = res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("restore answered %d, so this test measured nothing", res.StatusCode)
	}

	status, got := do(t, second, "GET", regionURL+"/private-networks/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("read after restore: status %d", status)
	}
	after := ipv6SubnetOf(t, got["subnets"].([]any))
	if after["subnet"] != before["subnet"] || after["id"] != before["id"] {
		t.Fatalf("the IPv6 subnet did not survive the snapshot: %v vs %v", after, before)
	}
}

func rawGet(t *testing.T, ts *httptest.Server, path string) []byte {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + path)
	if err != nil {
		t.Fatalf("GET %s: %v", path, err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("GET %s: status %d", path, res.StatusCode)
	}
	body, err := io.ReadAll(res.Body)
	if err != nil {
		t.Fatalf("GET %s: read: %v", path, err)
	}
	return body
}
