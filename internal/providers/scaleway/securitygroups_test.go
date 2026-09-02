package scaleway_test

import (
	"net/http"
	"net/http/httptest"
	"testing"
)

const zoneURL = "/instance/v1/zones/fr-par-1"

// groupID creates a security group and returns its ID, so each test states only
// what it is about.
func groupID(t *testing.T, ts *httptest.Server, body string) string {
	t.Helper()
	status, created := do(t, ts, "POST", zoneURL+"/security_groups", body)
	if status != http.StatusCreated {
		t.Fatalf("create group: expected 201, got %d (%v)", status, created)
	}
	group, _ := created["security_group"].(map[string]any)
	id, _ := group["id"].(string)
	if id == "" {
		t.Fatalf("create group: no id in %v", created)
	}
	return id
}

func TestSecurityGroupLifecycle(t *testing.T) {
	ts := newTestServer(t)

	id := groupID(t, ts, `{"name":"web","description":"http and https","inbound_default_policy":"drop"}`)

	status, got := do(t, ts, "GET", zoneURL+"/security_groups/"+id, "")
	if status != http.StatusOK {
		t.Fatalf("get: expected 200, got %d", status)
	}
	group, _ := got["security_group"].(map[string]any)

	// Create-then-read must round-trip, field for field: this is the single
	// most common cause of "inconsistent result after apply".
	for field, want := range map[string]any{
		"name":                    "web",
		"description":             "http and https",
		"inbound_default_policy":  "drop",
		"outbound_default_policy": "accept",
		"stateful":                true,
		"project_default":         false,
		"state":                   "available",
		"zone":                    "fr-par-1",
	} {
		if group[field] != want {
			t.Errorf("get %s: got %v, want %v", field, group[field], want)
		}
	}
	if group["creation_date"] == nil || group["modification_date"] == nil {
		t.Errorf("get: missing dates in %v", group)
	}

	status, updated := do(t, ts, "PATCH", zoneURL+"/security_groups/"+id, `{"name":"web-edge","stateful":false}`)
	if status != http.StatusOK {
		t.Fatalf("update: expected 200, got %d", status)
	}
	group, _ = updated["security_group"].(map[string]any)
	if group["name"] != "web-edge" || group["stateful"] != false {
		t.Errorf("update: got %v", group)
	}
	if group["description"] != "http and https" {
		t.Errorf("update dropped a field it was not given: %v", group)
	}

	if status, _ := do(t, ts, "DELETE", zoneURL+"/security_groups/"+id, ""); status != http.StatusNoContent {
		t.Fatalf("delete: expected 204, got %d", status)
	}
	if status, _ := do(t, ts, "GET", zoneURL+"/security_groups/"+id, ""); status != http.StatusNotFound {
		t.Fatalf("get after delete: expected 404, got %d", status)
	}
}

// A project that never created a group still has one: every client reads the
// existing groups before it creates anything.
func TestSecurityGroupDefaultIsProvisioned(t *testing.T) {
	ts := newTestServer(t)

	status, listed := do(t, ts, "GET", zoneURL+"/security_groups", "")
	if status != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", status)
	}
	groups, _ := listed["security_groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("list on a fresh project: expected the default group, got %v", listed)
	}
	first, _ := groups[0].(map[string]any)
	if first["project_default"] != true {
		t.Errorf("the provisioned group is not the project default: %v", first)
	}

	// Listing twice must not provision twice.
	if _, listed = do(t, ts, "GET", zoneURL+"/security_groups", ""); listed["total_count"] != float64(1) {
		t.Errorf("second list: expected 1 group, got %v", listed["total_count"])
	}

	defaultID, _ := first["id"].(string)
	status, denied := do(t, ts, "DELETE", zoneURL+"/security_groups/"+defaultID, "")
	if status != http.StatusBadRequest {
		t.Fatalf("delete the project default: expected 400, got %d (%v)", status, denied)
	}
	if denied["type"] != "precondition_failed" {
		t.Errorf("delete the project default: got type %v", denied["type"])
	}
}

// Asking for the default moves it: two defaults in a project is a state the API
// cannot return.
func TestSecurityGroupProjectDefaultIsExclusive(t *testing.T) {
	ts := newTestServer(t)

	do(t, ts, "GET", zoneURL+"/security_groups", "") // provisions the original default
	id := groupID(t, ts, `{"name":"new-default","project_default":true}`)

	_, listed := do(t, ts, "GET", zoneURL+"/security_groups", "")
	groups, _ := listed["security_groups"].([]any)
	defaults := 0
	for _, raw := range groups {
		group, _ := raw.(map[string]any)
		if group["project_default"] == true {
			defaults++
			if group["id"] != id {
				t.Errorf("the default stayed on %v", group["id"])
			}
		}
	}
	if defaults != 1 {
		t.Errorf("expected exactly one project default, got %d", defaults)
	}
}

func TestSecurityGroupRules(t *testing.T) {
	ts := newTestServer(t)
	id := groupID(t, ts, `{"name":"web"}`)
	rules := zoneURL + "/security_groups/" + id + "/rules"

	status, created := do(t, ts, "POST", rules,
		`{"protocol":"TCP","direction":"inbound","action":"accept","ip_range":"10.0.0.0/8","dest_port_from":22,"dest_port_to":22}`)
	if status != http.StatusCreated {
		t.Fatalf("create rule: expected 201, got %d (%v)", status, created)
	}
	rule, _ := created["rule"].(map[string]any)
	ruleID, _ := rule["id"].(string)
	if rule["dest_port_from"] != float64(22) || rule["position"] != float64(1) {
		t.Errorf("create rule: got %v", rule)
	}
	if _, leaked := rule["security_group"]; leaked {
		t.Errorf("the rule leaks its group into the wire shape: %v", rule)
	}

	status, got := do(t, ts, "GET", rules+"/"+ruleID, "")
	if status != http.StatusOK {
		t.Fatalf("get rule: expected 200, got %d", status)
	}
	if read, _ := got["rule"].(map[string]any); read["ip_range"] != "10.0.0.0/8" {
		t.Errorf("get rule: got %v", read)
	}

	// A zero port unsets the bound, which is what the SDK documents.
	status, patched := do(t, ts, "PATCH", rules+"/"+ruleID, `{"dest_port_to":0,"action":"drop"}`)
	if status != http.StatusOK {
		t.Fatalf("update rule: expected 200, got %d", status)
	}
	rule, _ = patched["rule"].(map[string]any)
	if rule["dest_port_to"] != nil || rule["action"] != "drop" {
		t.Errorf("update rule: got %v", rule)
	}

	if status, _ := do(t, ts, "DELETE", rules+"/"+ruleID, ""); status != http.StatusNoContent {
		t.Fatalf("delete rule: expected 204, got %d", status)
	}
	_, listed := do(t, ts, "GET", rules, "")
	if listed["total_count"] != float64(0) {
		t.Errorf("after delete: expected no rule, got %v", listed["total_count"])
	}
}

// set-rules replaces the whole list, and Terraform uses it for every change:
// a rule sent with its ID keeps it, a rule left out is gone.
func TestSecurityGroupSetRules(t *testing.T) {
	ts := newTestServer(t)
	id := groupID(t, ts, `{"name":"web"}`)
	rules := zoneURL + "/security_groups/" + id + "/rules"

	_, created := do(t, ts, "POST", rules, `{"protocol":"TCP","dest_port_from":22}`)
	rule, _ := created["rule"].(map[string]any)
	keptID, _ := rule["id"].(string)

	status, set := do(t, ts, "PUT", rules,
		`{"rules":[{"id":"`+keptID+`","protocol":"TCP","direction":"inbound","action":"accept","ip_range":"0.0.0.0/0","dest_port_from":22},`+
			`{"protocol":"UDP","direction":"outbound","action":"drop","ip_range":"0.0.0.0/0","dest_port_from":53}]}`)
	if status != http.StatusOK {
		t.Fatalf("set rules: expected 200, got %d (%v)", status, set)
	}
	out, _ := set["rules"].([]any)
	if len(out) != 2 {
		t.Fatalf("set rules: expected 2 rules, got %v", set)
	}
	first, _ := out[0].(map[string]any)
	if first["id"] != keptID {
		t.Errorf("set rules: the rule sent with its id was recreated: %v", first)
	}
	second, _ := out[1].(map[string]any)
	if second["protocol"] != "UDP" || second["position"] != float64(2) {
		t.Errorf("set rules: got %v", second)
	}

	// Replacing with a single rule drops the others.
	_, set = do(t, ts, "PUT", rules, `{"rules":[{"protocol":"ICMP","direction":"inbound","action":"accept","ip_range":"0.0.0.0/0"}]}`)
	if out, _ = set["rules"].([]any); len(out) != 1 {
		t.Fatalf("set rules again: expected 1 rule, got %v", set)
	}
	_, listed := do(t, ts, "GET", rules, "")
	if listed["total_count"] != float64(1) {
		t.Errorf("after replace: expected 1 rule, got %v", listed["total_count"])
	}
}

// The error shape matters more than the status: the SDK branches on the type
// field, and a wrong one turns a typed error into an opaque failure.
func TestSecurityGroupErrors(t *testing.T) {
	ts := newTestServer(t)

	cases := []struct {
		name       string
		method     string
		path       string
		body       string
		wantStatus int
		wantType   string
	}{
		{"missing name", "POST", zoneURL + "/security_groups", `{}`, http.StatusBadRequest, "invalid_arguments"},
		{"bad policy", "POST", zoneURL + "/security_groups", `{"name":"x","inbound_default_policy":"maybe"}`, http.StatusBadRequest, "invalid_arguments"},
		{"unknown group", "GET", zoneURL + "/security_groups/nope", "", http.StatusNotFound, "not_found"},
		{"unknown zone", "GET", "/instance/v1/zones/xx-yyy-9/security_groups", "", http.StatusBadRequest, "invalid_arguments"},
		{"malformed body", "POST", zoneURL + "/security_groups", `{`, http.StatusBadRequest, "invalid_arguments"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			status, body := do(t, ts, tc.method, tc.path, tc.body)
			if status != tc.wantStatus {
				t.Fatalf("expected %d, got %d (%v)", tc.wantStatus, status, body)
			}
			if body["type"] != tc.wantType {
				t.Errorf("expected type %q, got %v", tc.wantType, body["type"])
			}
		})
	}

	id := groupID(t, ts, `{"name":"web"}`)
	status, body := do(t, ts, "POST", zoneURL+"/security_groups/"+id+"/rules", `{"protocol":"SCTP"}`)
	if status != http.StatusBadRequest || body["type"] != "invalid_arguments" {
		t.Errorf("unsupported protocol: got %d (%v)", status, body)
	}

	// A rule read through another group is not found, not someone else's rule.
	_, created := do(t, ts, "POST", zoneURL+"/security_groups/"+id+"/rules", `{"protocol":"TCP"}`)
	rule, _ := created["rule"].(map[string]any)
	other := groupID(t, ts, `{"name":"db"}`)
	if status, _ = do(t, ts, "GET", zoneURL+"/security_groups/"+other+"/rules/"+rule["id"].(string), ""); status != http.StatusNotFound {
		t.Errorf("cross-group rule read: expected 404, got %d", status)
	}
}

// A server carries the project default unless it asks for a group, and it
// carries it as an object even though the request took a bare ID.
func TestServerCarriesSecurityGroup(t *testing.T) {
	ts := newTestServer(t)

	_, created := do(t, ts, "POST", zoneURL+"/servers", `{"name":"web-1","commercial_type":"DEV1-S"}`)
	server, _ := created["server"].(map[string]any)
	summary, _ := server["security_group"].(map[string]any)
	if summary == nil || summary["name"] != "Default security group" {
		t.Fatalf("server without a security group: %v", server)
	}

	id := groupID(t, ts, `{"name":"web"}`)
	_, created = do(t, ts, "POST", zoneURL+"/servers", `{"name":"web-2","commercial_type":"DEV1-S","security_group":"`+id+`"}`)
	server, _ = created["server"].(map[string]any)
	summary, _ = server["security_group"].(map[string]any)
	if summary["id"] != id {
		t.Errorf("server did not take the requested group: %v", summary)
	}

	// The group now names the server, and refuses to be deleted under it.
	_, got := do(t, ts, "GET", zoneURL+"/security_groups/"+id, "")
	group, _ := got["security_group"].(map[string]any)
	servers, _ := group["servers"].([]any)
	if len(servers) != 1 {
		t.Errorf("the group does not list its server: %v", group)
	}
	if status, _ := do(t, ts, "DELETE", zoneURL+"/security_groups/"+id, ""); status != http.StatusBadRequest {
		t.Errorf("delete an attached group: expected 400, got %d", status)
	}

	status, body := do(t, ts, "POST", zoneURL+"/servers", `{"name":"web-3","commercial_type":"DEV1-S","security_group":"nope"}`)
	if status != http.StatusBadRequest || body["type"] != "invalid_arguments" {
		t.Errorf("unknown security group: got %d (%v)", status, body)
	}
}

// Listing must be scoped to the project asked for. Without it, the lazy default
// is provisioned once per project seen and every one of them shows up in every
// list, so a client reads several groups all claiming to be the project default.
func TestSecurityGroupListIsScopedToTheProject(t *testing.T) {
	ts := newTestServer(t)

	// Made through the API since #391: a group filed under a project the
	// register does not hold is refused, the way fr-par refuses it. The list
	// below still asks about it before anything exists there, which stays a
	// truthful empty answer.
	other := projectMade(t, ts, "second")
	if status, _ := do(t, ts, "GET", zoneURL+"/security_groups?project="+other, ""); status != http.StatusOK {
		t.Fatalf("list for another project: expected 200, got %d", status)
	}

	status, listed := do(t, ts, "GET", zoneURL+"/security_groups", "")
	if status != http.StatusOK {
		t.Fatalf("list: expected 200, got %d", status)
	}
	groups, _ := listed["security_groups"].([]any)
	if len(groups) != 1 {
		t.Fatalf("list leaked another project's groups: got %d, want 1 (%v)", len(groups), listed)
	}

	// The group created in one project must not be visible from the other.
	id := groupID(t, ts, `{"name":"web","project":"`+other+`"}`)
	_, listed = do(t, ts, "GET", zoneURL+"/security_groups", "")
	groups, _ = listed["security_groups"].([]any)
	for _, g := range groups {
		if m, _ := g.(map[string]any); m["id"] == id {
			t.Errorf("group %s of project %s is visible from the default project", id, other)
		}
	}
}

// A set-rules call naming a rule of another group must not take it over: the ID
// An unparseable ip_range must be refused at the door. Stored, it poisons the
// runtime sync: the machine driver sends the whole rule set to the runtime,
// which rejects it wholesale, and the machines keep enforcing the previous
// rules while the API describes the new ones.
func TestRuleRefusesAnInvalidIPRange(t *testing.T) {
	ts := newTestServer(t)
	group := groupID(t, ts, `{"name":"g"}`)

	for _, ipRange := range []string{"10.291.0.0/24", "not-a-block", "10.0.0.0/33"} {
		status, body := do(t, ts, "POST", zoneURL+"/security_groups/"+group+"/rules",
			`{"protocol":"TCP","direction":"inbound","action":"accept","ip_range":"`+ipRange+`"}`)
		if status != http.StatusBadRequest {
			t.Errorf("ip_range %q: expected 400, got %d (%v)", ipRange, status, body)
		}
	}

	// A bare address is legal upstream and the SDK reads it with its host mask.
	status, body := do(t, ts, "POST", zoneURL+"/security_groups/"+group+"/rules",
		`{"protocol":"TCP","direction":"inbound","action":"accept","ip_range":"192.0.2.7"}`)
	if status != http.StatusCreated {
		t.Fatalf("bare address: expected 201, got %d (%v)", status, body)
	}
	rule, _ := body["rule"].(map[string]any)
	if rule["ip_range"] != "192.0.2.7/32" {
		t.Errorf("bare address served as %v, want 192.0.2.7/32", rule["ip_range"])
	}
}

// would be reused, and the rule would silently move out of the group that owns
// it.
func TestSetRulesCannotStealAnotherGroupRule(t *testing.T) {
	ts := newTestServer(t)

	victim := groupID(t, ts, `{"name":"victim"}`)
	status, created := do(t, ts, "POST", zoneURL+"/security_groups/"+victim+"/rules",
		`{"protocol":"TCP","direction":"inbound","action":"accept","dest_port_from":22}`)
	if status != http.StatusCreated {
		t.Fatalf("create rule: expected 201, got %d (%v)", status, created)
	}
	rule, _ := created["rule"].(map[string]any)
	stolenID, _ := rule["id"].(string)

	thief := groupID(t, ts, `{"name":"thief"}`)
	status, _ = do(t, ts, "PUT", zoneURL+"/security_groups/"+thief+"/rules",
		`{"rules":[{"id":"`+stolenID+`","protocol":"TCP","direction":"inbound","action":"drop"}]}`)
	if status != http.StatusOK {
		t.Fatalf("set rules: expected 200, got %d", status)
	}

	status, got := do(t, ts, "GET", zoneURL+"/security_groups/"+victim+"/rules/"+stolenID, "")
	if status != http.StatusOK {
		t.Fatalf("the victim's rule was taken over by another group: get returned %d (%v)", status, got)
	}
	kept, _ := got["rule"].(map[string]any)
	if kept["action"] != "accept" {
		t.Errorf("the victim's rule was rewritten: action is %v, want accept", kept["action"])
	}
}

// TestARuleAnswersTheDestinationRangeKeyTheCloudAnswers pins a key their own Go
// SDK has no field for and their published document does not declare.
//
// Only a recording of the wire could find it, and only a recording can hold it:
// corpus/scaleway/scw-free-shapes.jsonl seq 21-26 answers `dest_ip_range` on
// the create, the read, the update and both lists, always null, and no client
// in the recording ever sends one.
//
// Null and present, not absent: an omitted key is a shape the cloud never
// answers. The doors are asserted one by one because the pack renders a rule
// through one function and five handlers, and a fix that touched the create
// alone would read as done.
func TestARuleAnswersTheDestinationRangeKeyTheCloudAnswers(t *testing.T) {
	ts := newTestServer(t)
	id := groupID(t, ts, `{"name":"web"}`)

	status, created := do(t, ts, "POST", zoneURL+"/security_groups/"+id+"/rules",
		`{"action":"accept","direction":"inbound","protocol":"TCP","ip_range":"198.18.1.0/24","dest_port_from":443}`)
	if status != http.StatusCreated {
		t.Fatalf("create rule: expected 201, got %d (%v)", status, created)
	}
	rule, _ := created["rule"].(map[string]any)
	ruleID, _ := rule["id"].(string)
	assertNullDestRange(t, "CreateSecurityGroupRule", rule)

	status, read := do(t, ts, "GET", zoneURL+"/security_groups/"+id+"/rules/"+ruleID, "")
	if status != http.StatusOK {
		t.Fatalf("get rule: expected 200, got %d (%v)", status, read)
	}
	one, _ := read["rule"].(map[string]any)
	assertNullDestRange(t, "GetSecurityGroupRule", one)

	status, patched := do(t, ts, "PATCH", zoneURL+"/security_groups/"+id+"/rules/"+ruleID, `{"dest_port_from":8443}`)
	if status != http.StatusOK {
		t.Fatalf("update rule: expected 200, got %d (%v)", status, patched)
	}
	updated, _ := patched["rule"].(map[string]any)
	assertNullDestRange(t, "UpdateSecurityGroupRule", updated)

	status, listed := do(t, ts, "GET", zoneURL+"/security_groups/"+id+"/rules", "")
	if status != http.StatusOK {
		t.Fatalf("list rules: expected 200, got %d (%v)", status, listed)
	}
	rules, _ := listed["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("the list answers %d rule(s), want the one created: %v", len(rules), listed)
	}
	first, _ := rules[0].(map[string]any)
	assertNullDestRange(t, "ListSecurityGroupRules", first)

	status, set := do(t, ts, "PUT", zoneURL+"/security_groups/"+id+"/rules",
		`{"rules":[{"action":"accept","direction":"inbound","protocol":"TCP","ip_range":"198.18.2.0/24","dest_port_from":80}]}`)
	if status != http.StatusOK {
		t.Fatalf("set rules: expected 200, got %d (%v)", status, set)
	}
	after, _ := set["rules"].([]any)
	if len(after) == 0 {
		t.Fatalf("SetSecurityGroupRules answered no rule: %v", set)
	}
	replaced, _ := after[0].(map[string]any)
	assertNullDestRange(t, "SetSecurityGroupRules", replaced)
}

func assertNullDestRange(t *testing.T, operation string, rule map[string]any) {
	t.Helper()
	value, present := rule["dest_ip_range"]
	if !present {
		t.Errorf("%s omits dest_ip_range; a real fr-par account answers the key on every rule", operation)
		return
	}
	if value != nil {
		t.Errorf("%s answers dest_ip_range = %v, want null as the recording carries", operation, value)
	}
}

// TestTheDefaultRuleSegmentIsARuleSetAndNotAGroup holds the door
// `scw instance security-group list-default-rules` knocks on.
//
// `default` is a literal segment of the SDK's own path — ListDefaultSecurityGroupRules
// builds "/security_groups/default/rules" with no identifier in it — and it used
// to match {id} on the neighbouring route, find no group and answer 404. That is
// how a decline written against one operation stayed invisible to a client
// asking for another: the coverage record said "declined" while a live route
// answered wrong.
//
// The rules asserted are the ones a real fr-par account answered on 2026-08-24
// and not a shape of this emulator's choosing, so the count, the ports and the
// two address families are all pinned. So is `editable: false`, because a client
// that believed it could edit them would ask this emulator to delete a rule it
// does not own.
func TestTheDefaultRuleSegmentIsARuleSetAndNotAGroup(t *testing.T) {
	ts := newTestServer(t)
	status, body := do(t, ts, "GET", zoneURL+"/security_groups/default/rules", "")
	if status != http.StatusOK {
		t.Fatalf("list default rules: expected 200, got %d (%v)", status, body)
	}
	rules, _ := body["rules"].([]any)
	if len(rules) != 6 {
		t.Fatalf("the default rule set answers %d rule(s), want the six a real fr-par account answers: %v", len(rules), body)
	}
	ports := map[float64]int{}
	families := map[string]int{}
	seen := map[string]bool{}
	for _, raw := range rules {
		rule, _ := raw.(map[string]any)
		if rule["direction"] != "outbound" || rule["action"] != "drop" || rule["protocol"] != "TCP" {
			t.Errorf("a default rule reads %v/%v/%v, want an outbound TCP drop",
				rule["direction"], rule["action"], rule["protocol"])
		}
		if rule["editable"] != false {
			t.Errorf("a default rule is editable = %v; a client cannot change these", rule["editable"])
		}
		assertNullDestRange(t, "ListDefaultSecurityGroupRules", rule)
		if port, ok := rule["dest_port_from"].(float64); ok {
			ports[port]++
		}
		if r, ok := rule["ip_range"].(string); ok {
			families[r]++
		}
		id, _ := rule["id"].(string)
		if seen[id] {
			t.Errorf("two default rules share the identifier %s", id)
		}
		seen[id] = true
	}
	for _, port := range []float64{25, 465, 587} {
		if ports[port] != 2 {
			t.Errorf("port %v appears %d time(s), want once per address family", port, ports[port])
		}
	}
	for _, family := range []string{"0.0.0.0/0", "::/0"} {
		if families[family] != 3 {
			t.Errorf("%s appears %d time(s), want the three submission ports", family, families[family])
		}
	}

	// The identifiers are stable across reads: a client that stored one and
	// read again must find the same rule set, and nothing here mints per call.
	status, again := do(t, ts, "GET", zoneURL+"/security_groups/default/rules", "")
	if status != http.StatusOK {
		t.Fatalf("second read: expected 200, got %d (%v)", status, again)
	}
	for _, raw := range again["rules"].([]any) {
		id, _ := raw.(map[string]any)["id"].(string)
		if !seen[id] {
			t.Errorf("a second read answers the rule %s the first did not: the identifiers are not stable", id)
		}
	}

	// And the segment is not a group: a group by that name does not exist, so
	// asking for it as one still refuses.
	if status, missing := do(t, ts, "GET", zoneURL+"/security_groups/default", ""); status != http.StatusNotFound {
		t.Errorf("GET security_groups/default answered %d (%v), want 404: `default` names a rule set, not a group",
			status, missing)
	}
}
