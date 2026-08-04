package exoscale_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/core/store"
	"github.com/stephrobert/feint/internal/providers/exoscale"
)

// This pack had no tests at all while the two beside it had fourteen and one.
// What is pinned here is what an audit found missing or wrong, plus the two
// facts the official CLI depends on that nothing else in the repository states.

func serve(t *testing.T) http.Handler {
	t.Helper()
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, exoscale.New(env))
	if err != nil {
		t.Fatalf("build the server: %v", err)
	}
	return srv.Handler()
}

func call(t *testing.T, h http.Handler, method, path, body string) (*httptest.ResponseRecorder, map[string]any) {
	t.Helper()
	var reader *strings.Reader
	if body == "" {
		reader = strings.NewReader("")
	} else {
		reader = strings.NewReader(body)
	}
	req := httptest.NewRequest(method, path, reader)
	req.Header.Set("Content-Type", "application/json")
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

// An unserved route must answer in the API's own dialect. With 364 operations
// still untriaged this is the likeliest answer a client gets, and it used to be
// net/http's text/plain page, which no client can decode.
func TestAnUnservedRouteAnswersDecodableJSON(t *testing.T) {
	rec, body := call(t, serve(t), "GET", "/v2/dns-domain", "")

	if rec.Code != http.StatusNotFound {
		t.Fatalf("status %d, want 404", rec.Code)
	}
	if got := rec.Header().Get("Content-Type"); !strings.Contains(got, "application/json") {
		t.Fatalf("content type %q, want JSON: a client cannot decode anything else", got)
	}
	if _, ok := body["message"].(string); !ok {
		t.Fatalf("no message field in %v: that is the whole of Exoscale's error envelope", body)
	}
}

// The zone list is the first call the official CLI makes and the address every
// call after it uses. Two things must hold, and both were found by running the
// CLI rather than by reading the specification.
func TestZonesCarryThisEmulatorsAddress(t *testing.T) {
	rec, body := call(t, serve(t), "GET", "/v2/zone", "")
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200", rec.Code)
	}

	zones, ok := body["zones"].([]any)
	if !ok || len(zones) == 0 {
		t.Fatalf("no zones in %v", body)
	}

	// Exactly one zone, and it must be the CLI's own default.
	//
	// Not fewer, because a zone the CLI does not know about makes every unflagged
	// command fail with "find zone: not found in ListZonesResponse" before it
	// calls anything. Not more, because the CLI queries every zone it is told
	// about and merges the answers: eight zones pointing at one emulator listed
	// one instance eight times.
	if len(zones) != 1 {
		t.Fatalf("%d zones point at this emulator; every resource would be listed once per zone", len(zones))
	}
	names := map[string]bool{}
	for _, entry := range zones {
		zone, _ := entry.(map[string]any)
		name, _ := zone["name"].(string)
		names[name] = true

		endpoint, _ := zone["api-endpoint"].(string)
		if !strings.HasSuffix(endpoint, "/v2") {
			t.Fatalf("api-endpoint %q does not end in /v2: the CLI concatenates this value with the route it wants, so it would ask for /instance and take the 404 as an empty account", endpoint)
		}
		if strings.Contains(endpoint, "exoscale.com") {
			t.Fatalf("api-endpoint %q points at the real cloud: the next request would leave for a paying account", endpoint)
		}
	}
	if !names["ch-dk-2"] {
		t.Fatalf("the CLI default zone ch-dk-2 is missing from %v", names)
	}
}

// A create walks the catalogue first. Every one of these was declined until the
// CLI was watched making the calls.
func TestTheCatalogueACreateWalksIsServed(t *testing.T) {
	h := serve(t)
	for _, route := range []struct{ path, key string }{
		{"/v2/template", "templates"},
		{"/v2/instance-type", "instance-types"},
		{"/v2/ssh-key", "ssh-keys"},
	} {
		rec, body := call(t, h, "GET", route.path, "")
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s answered %d, want 200: a client cannot create anything without it", route.path, rec.Code)
		}
		if _, ok := body[route.key]; !ok {
			t.Fatalf("GET %s has no %q key: %v", route.path, route.key, body)
		}
	}
}

// The fingerprint has to be computed from the key, because a client comparing it
// against `ssh-keygen -l -E md5` would catch anything else.
func TestRegisteringAKeyComputesItsFingerprint(t *testing.T) {
	h := serve(t)
	// A real key from ssh-keygen. The fixture used to be a plausible string whose
	// material is not valid base64, and it passed because nothing checked.
	const key = "ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"

	rec, _ := call(t, h, "POST", "/v2/ssh-key",
		`{"name":"k1","public-key":"`+key+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("status %d, want 200: %s", rec.Code, rec.Body.String())
	}

	_, listed := call(t, h, "GET", "/v2/ssh-key", "")
	keys, _ := listed["ssh-keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("want one key, got %v", listed)
	}
	entry, _ := keys[0].(map[string]any)
	fingerprint, _ := entry["fingerprint"].(string)
	// Colon-separated MD5 of the decoded blob: sixteen bytes, fifteen colons.
	if strings.Count(fingerprint, ":") != 15 {
		t.Fatalf("fingerprint %q is not a colon-separated MD5 digest", fingerprint)
	}
	// The public key must not come back: their ssh-key schema declares a name
	// and a fingerprint, and the contract check fails a response that adds one.
	if _, leaked := entry["public-key"]; leaked {
		t.Fatalf("the response carries public-key, which their schema does not declare: %v", entry)
	}
}

// What a client attaches to an instance must come back on the instance. Dropping
// it crashed the official CLI outright, and the unread-field report named the
// fields in one run.
func TestCreateGivesBackWhatTheClientAttached(t *testing.T) {
	h := serve(t)
	call(t, h, "POST", "/v2/ssh-key",
		`{"name":"k1","public-key":"ssh-ed25519 AAAAC3NzaC1lZDI1NTE5AAAAIIr6pEFlAFO3YU0DNW/r8SkpjdbptN9ockkO2BtIolSD conformance@feint"}`)

	rec, _ := call(t, h, "POST", "/v2/instance", `{
		"name": "demo",
		"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template": {"id": "11111111-1111-4111-8111-111111111111"},
		"disk-size": 10,
		"ssh-keys": [{"name": "k1"}],
		"public-ip-assignment": "inet4"
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create answered %d: %s", rec.Code, rec.Body.String())
	}

	_, listed := call(t, h, "GET", "/v2/instance", "")
	instances, _ := listed["instances"].([]any)
	if len(instances) != 1 {
		t.Fatalf("want one instance, got %v", listed)
	}
	instance, _ := instances[0].(map[string]any)

	keys, _ := instance["ssh-keys"].([]any)
	if len(keys) != 1 {
		t.Fatalf("the keys the client attached did not come back: %v", instance)
	}
	if instance["public-ip-assignment"] != "inet4" {
		t.Fatalf("public-ip-assignment is %v, want inet4", instance["public-ip-assignment"])
	}

	// instance-type and template are full objects upstream, not bare ids. A
	// client reading instance-type.size off a {"id": ...} finds nothing.
	instanceType, _ := instance["instance-type"].(map[string]any)
	if instanceType["size"] == nil {
		t.Fatalf("instance-type came back as a bare reference: %v", instanceType)
	}
	template, _ := instance["template"].(map[string]any)
	if template["default-user"] == nil {
		t.Fatalf("template came back as a bare reference: %v", template)
	}
}

// With no runtime configured the control plane is the whole emulation, and it
// must still report a machine that runs. This is what keeps the suites runnable
// in CI, where no runtime exists.
func TestWithoutARuntimeAnInstanceStillReachesRunning(t *testing.T) {
	h := serve(t)
	rec, created := call(t, h, "POST", "/v2/instance", `{
		"name": "demo",
		"instance-type": {"id": "21624abb-764e-4def-81d7-9fc54b5957fb"},
		"template": {"id": "11111111-1111-4111-8111-111111111111"},
		"disk-size": 10
	}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("create answered %d: %s", rec.Code, rec.Body.String())
	}
	// A mutation answers an Operation the client polls, not the resource.
	if created["state"] != "success" {
		t.Fatalf("create did not answer an operation: %v", created)
	}

	_, listed := call(t, h, "GET", "/v2/instance", "")
	instances, _ := listed["instances"].([]any)
	instance, _ := instances[0].(map[string]any)
	if instance["state"] != "running" {
		t.Fatalf("state is %v, want running", instance["state"])
	}
}

// Quotas and the organisation read stay on the work list.
//
// They were declined once, and an audit caught it: the roadmap keeps them in
// scope as small read-only calls several clients make, and `exo limits` — a
// first-class command of the official CLI — walks that path. The correction was
// recorded in a comment, which on this repository is the failure mode with the
// longest record. This is the falsifiable form: re-declining any of the three
// fails here.
func TestQuotasAndOrganisationAreNotDeclined(t *testing.T) {
	pack := exoscale.New(&emulator.Env{Store: store.New()})
	kept := map[string]bool{
		"exoscale/v2.get-quota":        true,
		"exoscale/v2.list-quotas":      true,
		"exoscale/v2.get-organization": true,
	}
	for _, d := range pack.Declined() {
		if kept[d.Operation] {
			t.Errorf("%s is declined; the roadmap keeps it in scope as a read several clients make", d.Operation)
		}
	}
}
