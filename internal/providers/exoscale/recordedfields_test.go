package exoscale_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"regexp"
	"strings"
	"testing"
)

// The three fields the 2026-08-21 recording of a real ch-gva-2 account proved
// this emulator was omitting (#370, #371).
//
// They are together because they are one defect with one cause, stated in
// tools/contract/exoscale-recorded-fields.yaml: the cloud answers fields its own
// published API description does not declare, so the contract, the shapes gate,
// the probe and this pack all agreed with one another and all four were wrong.
// corpus/exoscale/exo-cli.jsonl is the only thing in the repository that could
// disagree, and `feint corpus --check` is where it does — these tests are the
// per-field half, close enough to the handler to say which line is wrong.

// uuidShaped is the form every identifier these APIs publish takes. Asserted
// rather than assumed: "stable" is satisfied by the empty string too, and an
// empty id would pass a comparison of two reads while telling a client nothing.
var uuidShaped = regexp.MustCompile(`^[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}$`)

// Every zone row carries an id, and it is the same id on the next read.
//
// #370 states the requirement and states why it is the interesting half: an
// identifier that changed per request would be worse than none at all, because
// a client that stored it would hold a value naming nothing. The zone list is
// also the most-answered operation of the whole recording — `exo` calls it
// before very nearly every command — so this is 51 of that file's 105
// divergences on its own.
//
// Both zone lists are read, because they are built by different code: the CLI
// gets the one row this deployment serves, and a split client gets that row plus
// a signpost per published zone (#284). An id on the first and not the second
// would be a hole exactly one client family falls into.
func TestZonesCarryAStableIdentifier(t *testing.T) {
	h := serve(t)

	for _, agent := range []struct{ name, userAgent string }{
		{"the CLI", "exoscale-cli"},
		{"a split client", "Exoscale-Terraform-Provider/0.70.0 (something) Terraform-SDK/2.31.0"},
	} {
		t.Run(agent.name, func(t *testing.T) {
			// The split client is refused by user agent unless the operator
			// opts in, because the Terraform provider only honours
			// EXOSCALE_API_ENDPOINT for half its calls (docs/limits.md). The
			// opt-in is what gets this test to the zone list at all.
			t.Setenv("FEINT_EXOSCALE_ALLOW_TERRAFORM", "1")

			first := zoneIdentifiers(t, h, agent.userAgent)
			if len(first) == 0 {
				t.Fatal("the zone list answered no row, so this measured nothing")
			}
			for name, id := range first {
				if !uuidShaped.MatchString(id) {
					t.Errorf("zone %s answers id %q, which is not the UUID shape every "+
						"identifier of this API takes: a client storing it holds nothing", name, id)
				}
			}

			second := zoneIdentifiers(t, h, agent.userAgent)
			for name, id := range first {
				if second[name] != id {
					t.Errorf("zone %s answered id %q and then %q: an identifier that moves "+
						"between two reads of one emulator is worse than none (#370)", name, id, second[name])
				}
			}
		})
	}
}

// zoneIdentifiers reads GET /v2/zone as the named client and returns the id of
// every row, by zone name.
func zoneIdentifiers(t *testing.T, h http.Handler, userAgent string) map[string]string {
	t.Helper()
	rec, body := callAs(t, h, "GET", "/v2/zone", userAgent)
	if rec.Code != http.StatusOK {
		t.Fatalf("GET /v2/zone: status %d, want 200", rec.Code)
	}
	rows, _ := body["zones"].([]any)
	out := map[string]string{}
	for _, raw := range rows {
		row, _ := raw.(map[string]any)
		name, _ := row["name"].(string)
		id, ok := row["id"].(string)
		if !ok {
			t.Fatalf("zone %s answers no id, which is what the real list carries on every row (#370): %v", name, row)
		}
		out[name] = id
	}
	return out
}

// Every security group answers visibility, on the list and on the read of one
// group alike — 46 of the recording's divergences, and two entries in
// corpus/accepted.json rather than one, precisely so that fixing the list could
// not quietly leave the get behind (#371).
func TestASecurityGroupPublishesItsVisibility(t *testing.T) {
	h := serve(t)

	_, listed := call(t, h, "GET", "/v2/security-group", "")
	groups, _ := listed["security-groups"].([]any)
	if len(groups) == 0 {
		t.Fatal("a fresh account listed no group, so this measured nothing")
	}
	for _, raw := range groups {
		group, _ := raw.(map[string]any)
		if group["visibility"] != "private" {
			t.Errorf("the listed group %v answers visibility %v, want private", group["name"], group["visibility"])
		}
	}

	// The default group, read on its own. Same view, different door, and the
	// door is what #371 keeps a separate exemption for.
	first, _ := groups[0].(map[string]any)
	id, _ := first["id"].(string)
	_, read := call(t, h, "GET", "/v2/security-group/"+id, "")
	if read["visibility"] != "private" {
		t.Errorf("the group read on its own answers visibility %v, want private: %v", read["visibility"], read)
	}
}

// A rule whose target is another group publishes that group's name beside its
// id, which is what the wire carries and what a consumer of the rule reads.
//
// It is the shape examples/stacks/exoscale/main.tf writes as
// user_security_group_id — "the application tier accepts the web tier and nobody
// else". What it is not is a client symptom: `exo compute security-group show`
// prints the right name either way, because it resolves the reference by id
// itself, and that was measured before the claim was written down. The subject
// is the wire, which is the only thing corpus/ compares.
//
// The reference carries id and name and nothing else, and that is measured too:
// their document declares visibility on the same schema, and no recorded rule
// reference has one. Answering it would be a field this emulator invented.
func TestARuleThatNamesAGroupPublishesThatGroupsName(t *testing.T) {
	h := serve(t)

	web := createGroup(t, h, "web")
	app := createGroup(t, h, "app")

	rec, _ := call(t, h, "POST", "/v2/security-group/"+app+"/rules",
		`{"flow-direction":"ingress","protocol":"tcp","start-port":8080,"end-port":8080,`+
			`"security-group":{"id":"`+web+`"}}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("adding a group-to-group rule answered %d", rec.Code)
	}

	_, group := call(t, h, "GET", "/v2/security-group/"+app, "")
	rules, _ := group["rules"].([]any)
	if len(rules) != 1 {
		t.Fatalf("the rule did not land: %v", group)
	}
	rule, _ := rules[0].(map[string]any)
	target, _ := rule["security-group"].(map[string]any)
	if target["id"] != web {
		t.Fatalf("the rule points at %v, want the web group %s", target["id"], web)
	}
	if target["name"] != "web" {
		t.Errorf("the rule's target publishes name %v, want web: the cloud sends a name "+
			"beside the id and this emulator sent the id alone (#371)", target["name"])
	}
	if len(target) != 2 {
		t.Errorf("the rule's target carries %v, want id and name alone: no recorded "+
			"reference carries anything else, and a third key would be invented", target)
	}

	// The name follows the group rather than a copy of it: renaming is not
	// served here, but deleting is, and a reference to a group that is gone
	// must publish its id alone rather than a name from nowhere.
	rec, _ = call(t, h, "DELETE", "/v2/security-group/"+web, "")
	if rec.Code != http.StatusOK {
		t.Fatalf("deleting the unworn web group answered %d", rec.Code)
	}
	_, group = call(t, h, "GET", "/v2/security-group/"+app, "")
	rules, _ = group["rules"].([]any)
	rule, _ = rules[0].(map[string]any)
	target, _ = rule["security-group"].(map[string]any)
	if _, named := target["name"]; named {
		t.Errorf("the rule still names a group that no longer exists: %v", target)
	}
}

// callAs is call() with a User-Agent, which is what tells the two client
// families apart on the zone list (splitClient, #284).
func callAs(t *testing.T, h http.Handler, method, path, userAgent string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(""))
	req.Header.Set("User-Agent", userAgent)
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)

	decoded := map[string]any{}
	if rec.Body.Len() > 0 {
		if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
			t.Fatalf("%s %s answered something that is not a JSON object: %s", method, path, rec.Body.String())
		}
	}
	return rec, decoded
}

// createGroup creates one security group and returns its identifier.
func createGroup(t *testing.T, h http.Handler, name string) string {
	t.Helper()
	rec, op := call(t, h, "POST", "/v2/security-group", `{"name":"`+name+`","description":"`+name+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("creating the %s group answered %d: %s", name, rec.Code, rec.Body.String())
	}
	ref, _ := op["reference"].(map[string]any)
	id, _ := ref["id"].(string)
	if id == "" {
		t.Fatalf("the create names no group: %v", op)
	}
	return id
}
