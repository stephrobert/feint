package outscale_test

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
	"github.com/stephrobert/feint/internal/core/emulator"
	"github.com/stephrobert/feint/internal/providers/outscale"
)

// Every response this pack emits is checked against Outscale's own OpenAPI
// document. Not because unknown fields are untidy, but because 643 of their 650
// schemas declare additionalProperties: false — a field they do not define is a
// violation they wrote down, and one nobody here had ever read.
//
// Two shipped before this existed. CreateVms read a VmCount the API does not
// have, where it says MinVmsCount and MaxVmsCount, so every request created one
// machine whatever it asked for. And the Scaleway pack once served an invented
// private_ip that its own conformance suite read back, proving the emulator
// against itself. A real client catches neither: both are fields a client
// tolerates or never sends.

func contractDoc(t *testing.T) *contract.Doc {
	t.Helper()
	doc, err := contract.Load(filepath.Join("..", "..", "..", "contracts", "outscale.json"))
	if err != nil {
		t.Fatalf("load contract: %v", err)
	}
	return doc
}

func newServer(t *testing.T) *httptest.Server {
	t.Helper()
	env := emulator.DefaultEnv()
	srv, err := emulator.NewServer(env, outscale.New(env))
	if err != nil {
		t.Fatalf("build emulator: %v", err)
	}
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return ts
}

// call posts an action and returns the decoded body, checked against the
// operation's response schema. Every helper in this file goes through it, so a
// test cannot accidentally assert on a shape the contract forbids.
func call(t *testing.T, ts *httptest.Server, doc *contract.Doc, action, body string) map[string]any {
	t.Helper()

	res, err := http.Post(ts.URL+"/api/v1/"+action, "application/json", strings.NewReader(body)) //nolint:noctx // test client
	if err != nil {
		t.Fatalf("%s: %v", action, err)
	}
	defer func() { _ = res.Body.Close() }()

	var decoded map[string]any
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		t.Fatalf("%s: decode: %v", action, err)
	}
	if res.StatusCode != http.StatusOK {
		t.Fatalf("%s: status %d: %v", action, res.StatusCode, decoded)
	}
	if violations := doc.ValidateResponse(action, decoded); len(violations) > 0 {
		t.Errorf("%s does not match the contract:\n  %s", action, strings.Join(reasons(violations), "\n  "))
	}
	return decoded
}

func reasons(vs contract.Violations) []string {
	out := make([]string, 0, len(vs))
	for _, v := range vs {
		out = append(out, v.String())
	}
	return out
}

// TestEveryResponseMatchesTheContract walks a whole lifecycle and validates each
// answer. Written as one sequence rather than a case per route because the
// interesting responses need real identifiers, and threading them is what a
// client does too.
func TestEveryResponseMatchesTheContract(t *testing.T) {
	ts := newServer(t)
	doc := contractDoc(t)

	// The inventory a client reads before it creates anything.
	call(t, ts, doc, "ReadVmTypes", `{}`)
	call(t, ts, doc, "ReadImages", `{}`)
	call(t, ts, doc, "ReadRegions", `{}`)
	call(t, ts, doc, "ReadSubregions", `{}`)
	call(t, ts, doc, "ReadVms", `{}`)
	call(t, ts, doc, "ReadKeypairs", `{}`)

	call(t, ts, doc, "CreateKeypair",
		`{"KeypairName":"conformance","PublicKey":"ssh-ed25519 AAAAC3Nz fake@feint"}`)

	created := call(t, ts, doc, "CreateVms",
		`{"ImageId":"ami-00000001","VmType":"tinav6.c1r1p2","KeypairName":"conformance"}`)
	id := firstVMID(t, created)

	call(t, ts, doc, "ReadVms", `{"Filters":{"VmIds":["`+id+`"]}}`)
	call(t, ts, doc, "StopVms", `{"VmIds":["`+id+`"]}`)
	call(t, ts, doc, "UpdateVm", `{"VmId":"`+id+`","VmType":"tinav6.c2r2p2"}`)
	call(t, ts, doc, "StartVms", `{"VmIds":["`+id+`"]}`)
	call(t, ts, doc, "RebootVms", `{"VmIds":["`+id+`"]}`)
	call(t, ts, doc, "DeleteVms", `{"VmIds":["`+id+`"]}`)
	call(t, ts, doc, "DeleteKeypair", `{"KeypairName":"conformance"}`)
}

// Paths and methods are checked for every pack that has a contract, in
// emulator's TestEveryRouteMatchesItsContract. What is specific here is the
// naming convention: the drift report joins on the operation name, so a route
// declaring the right path under a name that does not match the SDK scan counts
// as coverage of something else.
func TestEveryRouteNamesItsSDKOperation(t *testing.T) {
	pack := outscale.New(emulator.DefaultEnv())

	for _, r := range pack.Routes() {
		action := strings.TrimPrefix(r.Path, "/api/v1/")
		if want := "osc/Client." + action; r.Operation != want {
			t.Errorf("route %s declares operation %q, want %q", r.Path, r.Operation, want)
		}
	}
}

// TestTheContractIsTheOneWeShip guards against a stale artefact: the emulator
// would validate against a version of the API nobody serves.
func TestTheContractIsTheOneWeShip(t *testing.T) {
	doc := contractDoc(t)
	if doc.Provider != "outscale" {
		t.Errorf("provider = %q", doc.Provider)
	}
	if doc.APIVersion == "" {
		t.Error("the contract carries no API version")
	}
	if len(doc.Operations) < 200 {
		t.Errorf("%d operations: the extraction lost most of the surface", len(doc.Operations))
	}
	closed := 0
	for _, s := range doc.Schemas {
		if s.Closed {
			closed++
		}
	}
	if closed == 0 {
		t.Error("no schema is closed: unknown fields would never be reported")
	}
}

func firstVMID(t *testing.T, body map[string]any) string {
	t.Helper()
	vms, ok := body["Vms"].([]any)
	if !ok || len(vms) == 0 {
		t.Fatalf("no Vms in %v", body)
	}
	vm, _ := vms[0].(map[string]any)
	id, _ := vm["VmId"].(string)
	if id == "" {
		t.Fatalf("no VmId in %v", vm)
	}
	return id
}
