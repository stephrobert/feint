package scaleway_test

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
)

// aclURL is the one spelling of the path, so a route that moves breaks every
// test here at once rather than three of five.
func aclURL(vpcID string) string {
	return regionURL + "/vpcs/" + vpcID + "/acl-rules"
}

// newVPC creates one and returns its id.
func newVPC(t *testing.T, ts *httptest.Server, name string) string {
	t.Helper()
	status, created := do(t, ts, "POST", regionURL+"/vpcs", `{"name":"`+name+`"}`)
	if status != http.StatusOK {
		t.Fatalf("create vpc: expected 200, got %d (%v)", status, created)
	}
	id, _ := created["id"].(string)
	if id == "" {
		t.Fatalf("create vpc: no id in %v", created)
	}
	return id
}

// oneRule is the shape SetACL takes, spelled once.
const oneRule = `{"is_ipv6":false,"default_policy":"drop","rules":[` +
	`{"protocol":"TCP","source":"0.0.0.0/0","src_port_low":0,"src_port_high":0,` +
	`"destination":"0.0.0.0/0","dst_port_low":443,"dst_port_high":443,` +
	`"action":"accept","description":"https"}]}`

// A VPC nobody has set an ACL on answers what the real cloud answers.
//
// Measured on 2026-08-21 against a real fr-par account, by reading the ACL of
// that account's own default VPC and creating nothing:
//
//	{"rules":[],"default_policy":"accept"}
//
// for is_ipv6=false and for is_ipv6=true alike. The value matters because it is
// the one an emulator is most tempted to invent: the SDK's own default for
// Action is `unknown_action`, the protobuf zero, and it is not what the wire
// carries. `scw vpc rule get` is a read a client makes before it has ever set
// anything, so this is the answer most clients meet first.
func TestAnUnsetACLAnswersWhatTheCloudAnswers(t *testing.T) {
	ts := newTestServer(t)
	vpc := newVPC(t, ts, "unset-acl")

	for _, family := range []string{"false", "true"} {
		status, body := do(t, ts, "GET", aclURL(vpc)+"?is_ipv6="+family, "")
		if status != http.StatusOK {
			t.Fatalf("is_ipv6=%s: expected 200, got %d (%v)", family, status, body)
		}
		rules, ok := body["rules"].([]any)
		if !ok {
			t.Fatalf("is_ipv6=%s: rules is %T, want a list — the SDK decodes []*ACLRule", family, body["rules"])
		}
		if len(rules) != 0 {
			t.Errorf("is_ipv6=%s: an unset ACL carries %d rule(s), the cloud answers none", family, len(rules))
		}
		if got := body["default_policy"]; got != "accept" {
			t.Errorf("is_ipv6=%s: default_policy is %v, the cloud answered \"accept\"", family, got)
		}
		// The answer carries the two fields the SDK declares and no identifier:
		// an ACL is the pair (VPC, family) upstream, and publishing the key
		// this emulator derives would hand a client a value no cloud answers.
		for _, absent := range []string{"id", "vpc_id", "is_ipv6", "created_at", "updated_at"} {
			if _, present := body[absent]; present {
				t.Errorf("is_ipv6=%s: the answer carries %q, which GetACLResponse does not declare", family, absent)
			}
		}
	}
}

// What SetACL stores is what GetACL answers, per address family, and the two
// families do not overwrite each other.
//
// The second half is the one a single stored ACL would fail: a VPC carries one
// rule set for IPv4 and one for IPv6, and the SDK says so in the field it makes
// mandatory on both operations ("Each Network ACL can have rules for only one
// IP type").
func TestSetACLRoundTripsPerAddressFamily(t *testing.T) {
	ts := newTestServer(t)
	vpc := newVPC(t, ts, "round-trip")

	status, set := do(t, ts, "PUT", aclURL(vpc), oneRule)
	if status != http.StatusOK {
		t.Fatalf("set acl: expected 200, got %d (%v)", status, set)
	}
	if got := set["default_policy"]; got != "drop" {
		t.Errorf("the set answered default_policy %v, the request said drop", got)
	}
	rules, _ := set["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("the set answered %d rule(s), the request carried one: %v", len(rules), set["rules"])
	}
	rule, _ := rules[0].(map[string]any)
	for field, want := range map[string]any{
		"protocol": "TCP", "action": "accept", "description": "https",
		"source": "0.0.0.0/0", "destination": "0.0.0.0/0",
		"dst_port_low": float64(443), "dst_port_high": float64(443),
	} {
		if rule[field] != want {
			t.Errorf("the stored rule's %s is %v, the request sent %v", field, rule[field], want)
		}
	}

	status, read := do(t, ts, "GET", aclURL(vpc)+"?is_ipv6=false", "")
	if status != http.StatusOK {
		t.Fatalf("get acl: expected 200, got %d (%v)", status, read)
	}
	if fmt.Sprint(read["rules"]) != fmt.Sprint(set["rules"]) {
		t.Errorf("the read answers %v where the set answered %v", read["rules"], set["rules"])
	}
	// A stored ACL answers the two fields the SDK declares and no identifier,
	// exactly as an unset one does. The emulator derives a key to address it —
	// the VPC plus the family — and publishing that key would hand a client a
	// value no real cloud answers, which a state file would then carry and
	// compare on every plan.
	for _, body := range []map[string]any{set, read} {
		for _, absent := range []string{"id", "vpc_id", "is_ipv6", "created_at", "updated_at"} {
			if _, present := body[absent]; present {
				t.Errorf("a stored ACL answers %q, which SetACLResponse does not declare: %v", absent, body)
			}
		}
	}

	// The other family is untouched: an IPv4 rule set must not appear as the
	// VPC's IPv6 one.
	status, v6 := do(t, ts, "GET", aclURL(vpc)+"?is_ipv6=true", "")
	if status != http.StatusOK {
		t.Fatalf("get ipv6 acl: expected 200, got %d (%v)", status, v6)
	}
	if got, _ := v6["rules"].([]any); len(got) != 0 {
		t.Errorf("setting the IPv4 rules gave the IPv6 family %d rule(s): %v", len(got), v6["rules"])
	}
	if got := v6["default_policy"]; got != "accept" {
		t.Errorf("setting the IPv4 policy moved the IPv6 one to %v", got)
	}
}

// A rule the API's own enums and types refuse is refused here, before anything
// is stored — and the answer names which rule of the list.
//
// Echoing an unparseable CIDR back with a 200 is the "a 200 that lies" family
// this repository exists to measure: the client would read its filter back
// unchanged and believe a source it wrote wrongly was accepted.
func TestSetACLRefusesWhatTheEnumsRefuse(t *testing.T) {
	ts := newTestServer(t)
	vpc := newVPC(t, ts, "refusals")

	cases := map[string]struct{ body, names string }{
		"an unknown protocol": {
			body:  `{"is_ipv6":false,"default_policy":"accept","rules":[{"protocol":"SCTP","source":"0.0.0.0/0","destination":"0.0.0.0/0","action":"accept"}]}`,
			names: "rules.0.protocol",
		},
		"an unknown action": {
			body:  `{"is_ipv6":false,"default_policy":"accept","rules":[{"protocol":"TCP","source":"0.0.0.0/0","destination":"0.0.0.0/0","action":"maybe"}]}`,
			names: "rules.0.action",
		},
		"a source that is not a CIDR": {
			body:  `{"is_ipv6":false,"default_policy":"accept","rules":[{"protocol":"TCP","source":"not-a-cidr","destination":"0.0.0.0/0","action":"accept"}]}`,
			names: "rules.0.source",
		},
		"a port range the wrong way round": {
			body:  `{"is_ipv6":false,"default_policy":"accept","rules":[{"protocol":"TCP","source":"0.0.0.0/0","destination":"0.0.0.0/0","action":"accept","dst_port_low":443,"dst_port_high":80}]}`,
			names: "rules.0.dst_port_high",
		},
		"a default policy outside the enum": {
			body:  `{"is_ipv6":false,"default_policy":"unknown_action","rules":[]}`,
			names: "default_policy",
		},
	}
	for name, c := range cases {
		t.Run(name, func(t *testing.T) {
			status, body := do(t, ts, "PUT", aclURL(vpc), c.body)
			if status != http.StatusBadRequest {
				t.Fatalf("expected 400, got %d (%v)", status, body)
			}
			if !bodyNames(body, c.names) {
				t.Errorf("the refusal does not name %s: %v", c.names, body)
			}
		})
	}

	// And nothing was stored on the way through: a refusal that half-applied
	// would leave the VPC filtered by a rule set no client ever accepted.
	status, read := do(t, ts, "GET", aclURL(vpc)+"?is_ipv6=false", "")
	if status != http.StatusOK {
		t.Fatalf("get acl: expected 200, got %d (%v)", status, read)
	}
	if got, _ := read["rules"].([]any); len(got) != 0 {
		t.Errorf("a refused set left %d rule(s) behind: %v", len(got), read["rules"])
	}
}

// bodyNames reports whether an invalid-arguments body names this argument. The
// shape is scw's own: {"details":[{"argument_name":…}]}.
func bodyNames(body map[string]any, argument string) bool {
	details, _ := body["details"].([]any)
	for _, d := range details {
		entry, _ := d.(map[string]any)
		if entry["argument_name"] == argument {
			return true
		}
	}
	return false
}

// Two concurrent SetACLs on one VPC leave one ACL, not two.
//
// The reason the stored key is derived from the VPC and the family rather than
// minted. With a minted identifier the write is a find-or-create, two PUTs that
// both miss the read both create, and a later GET answers whichever the store
// happens to hold — an emulator that forgets one of two writes it acknowledged.
// Racy by construction and run under -race, so a lost update shows as a rule
// set neither request sent, or as a second stored object.
func TestTwoConcurrentSetACLsLeaveOneACL(t *testing.T) {
	ts := newTestServer(t)
	vpc := newVPC(t, ts, "concurrent")

	bodies := []string{
		`{"is_ipv6":false,"default_policy":"drop","rules":[{"protocol":"TCP","source":"10.0.0.0/8","destination":"0.0.0.0/0","action":"accept","dst_port_low":22,"dst_port_high":22}]}`,
		`{"is_ipv6":false,"default_policy":"accept","rules":[{"protocol":"UDP","source":"10.0.0.0/8","destination":"0.0.0.0/0","action":"drop","dst_port_low":53,"dst_port_high":53}]}`,
	}
	var wg sync.WaitGroup
	for _, body := range bodies {
		wg.Add(1)
		go func() {
			defer wg.Done()
			status, out := do(t, ts, "PUT", aclURL(vpc), body)
			if status != http.StatusOK {
				t.Errorf("concurrent set: expected 200, got %d (%v)", status, out)
			}
		}()
	}
	wg.Wait()

	status, read := do(t, ts, "GET", aclURL(vpc)+"?is_ipv6=false", "")
	if status != http.StatusOK {
		t.Fatalf("get acl: expected 200, got %d (%v)", status, read)
	}
	rules, _ := read["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("two concurrent sets left %d rule(s); each sent exactly one, so the VPC holds "+
			"neither answer whole: %v", len(rules), read["rules"])
	}
	// The winner is either request, and it is one of them entire: a policy from
	// one and a rule from the other is the lost update this guard is about.
	rule, _ := rules[0].(map[string]any)
	switch read["default_policy"] {
	case "drop":
		if rule["protocol"] != "TCP" {
			t.Errorf("the stored policy is the first request's and the rule is not: %v", read)
		}
	case "accept":
		if rule["protocol"] != "UDP" {
			t.Errorf("the stored policy is the second request's and the rule is not: %v", read)
		}
	default:
		t.Errorf("the stored policy is %v, which neither request sent", read["default_policy"])
	}
}

// Deleting a VPC takes its ACLs with it.
//
// The stored key is derived from the VPC's identifier, so an ACL left behind is
// addressed by a key nothing can reach — until something mints that identifier
// again, which a restored snapshot or a seeded run does, and the new VPC then
// answers a filter it never set. The store is the only thing that remembers, so
// the delete is where the sweep belongs.
func TestDeletingAVPCTakesItsACLsWithIt(t *testing.T) {
	ts := newTestServer(t)
	vpc := newVPC(t, ts, "swept")

	if status, out := do(t, ts, "PUT", aclURL(vpc), oneRule); status != http.StatusOK {
		t.Fatalf("set acl: expected 200, got %d (%v)", status, out)
	}
	// Asserted before the delete, so a test that swept nothing because nothing
	// was there cannot pass.
	status, before := do(t, ts, "GET", aclURL(vpc)+"?is_ipv6=false", "")
	if rules, _ := before["rules"].([]any); status != http.StatusOK || len(rules) != 1 {
		t.Fatalf("the ACL was not stored, so this proves nothing: %d %v", status, before)
	}

	if status, out := do(t, ts, "DELETE", regionURL+"/vpcs/"+vpc, ""); status != http.StatusNoContent {
		t.Fatalf("delete vpc: expected 204, got %d (%v)", status, out)
	}

	// The path answers 404 now — but that is the VPC being gone, not the ACL
	// being swept, and taking it as proof is the mistake this test exists to
	// avoid. The store is asked directly, through the snapshot the emulator
	// publishes, which is also the artefact a restore would read back.
	if status, after := do(t, ts, "GET", aclURL(vpc)+"?is_ipv6=false", ""); status != http.StatusNotFound {
		t.Fatalf("reading the ACL of a deleted VPC answered %d, want 404 (%v)", status, after)
	}
	if kinds := snapshotKinds(t, ts); kinds["vpc/acl"] != 0 {
		t.Errorf("deleting the VPC left %d vpc/acl resource(s) in the store, addressed by a key "+
			"nothing can reach: %v", kinds["vpc/acl"], kinds)
	}
}

// snapshotKinds counts what the store holds, per kind, through the snapshot the
// emulator publishes at /_feint/state.
func snapshotKinds(t *testing.T, ts *httptest.Server) map[string]int {
	t.Helper()
	res, err := ts.Client().Get(ts.URL + "/_feint/state")
	if err != nil {
		t.Fatalf("read the state: %v", err)
	}
	defer func() { _ = res.Body.Close() }()
	var snapshot struct {
		Resources []struct {
			Kind string `json:"kind"`
		} `json:"resources"`
	}
	if err := json.NewDecoder(res.Body).Decode(&snapshot); err != nil {
		t.Fatalf("decode the state: %v", err)
	}
	out := map[string]int{}
	for _, r := range snapshot.Resources {
		out[r.Kind]++
	}
	return out
}
