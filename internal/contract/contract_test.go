package contract_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
)

// A validator nobody validates is worse than none: it reports success on
// everything and buys a false sense of coverage. These cases are each a defect
// this package exists to catch, plus the ones it must not invent.

const doc = `{
  "provider": "test",
  "apiVersion": "1.0",
  "operations": {
    "ReadThings": {"path": "/ReadThings", "method": "POST",
                   "request": "ReadThingsRequest", "response": "ReadThingsResponse"},
    "Silent":     {"path": "/Silent", "method": "POST"}
  },
  "schemas": {
    "ReadThingsRequest":  {"closed": true, "properties": {"Filters": {"ref": "Filters"}}},
    "Filters":            {"closed": true, "properties": {"Names": {"type": "array", "items": {"type": "string"}}}},
    "ReadThingsResponse": {"closed": true, "properties": {
        "Things":          {"type": "array", "items": {"ref": "Thing"}},
        "ResponseContext": {"ref": "ResponseContext"}}},
    "ResponseContext":    {"closed": true, "properties": {"RequestId": {"type": "string"}}},
    "Thing":              {"closed": true, "required": ["ThingId"], "properties": {
        "ThingId": {"type": "string"},
        "Size":    {"type": "integer"},
        "Ready":   {"type": "boolean"},
        "State":   {"type": "string", "enum": ["pending", "running"]},
        "Tags":    {"type": "array", "items": {"ref": "Tag"}}}},
    "Tag":                {"closed": true, "properties": {"Key": {"type": "string"}}},
    "Open":               {"closed": false, "properties": {"Known": {"type": "string"}}}
  }
}`

func load(t *testing.T) *contract.Doc {
	t.Helper()
	d, err := contract.Read(strings.NewReader(doc))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return d
}

func value(t *testing.T, raw string) any {
	t.Helper()
	var v any
	if err := json.Unmarshal([]byte(raw), &v); err != nil {
		t.Fatalf("bad fixture: %v", err)
	}
	return v
}

func TestValidateAcceptsWhatTheContractDefines(t *testing.T) {
	d := load(t)
	body := value(t, `{
      "Things": [{"ThingId": "t-1", "Size": 3, "Ready": true, "State": "running",
                  "Tags": [{"Key": "env"}]}],
      "ResponseContext": {"RequestId": "abc"}
    }`)
	if vs := d.ValidateResponse("ReadThings", body); len(vs) > 0 {
		t.Fatalf("valid body rejected: %v", vs)
	}
}

func TestValidateReportsAnInventedField(t *testing.T) {
	// The defect this package was written for. A private_ip on Scaleway's
	// PrivateNIC and a VmCount on Outscale's CreateVms both looked like this.
	d := load(t)
	body := value(t, `{"Things": [{"ThingId": "t-1", "PrivateIp": "10.0.0.2"}]}`)

	vs := d.ValidateResponse("ReadThings", body)
	if len(vs) != 1 {
		t.Fatalf("got %d violations, want 1: %v", len(vs), vs)
	}
	if vs[0].Path != "Things[0].PrivateIp" {
		t.Errorf("path = %q, want Things[0].PrivateIp", vs[0].Path)
	}
	if !strings.Contains(vs[0].Reason, "does not define") {
		t.Errorf("reason = %q", vs[0].Reason)
	}
}

func TestValidateLeavesAnOpenSchemaAlone(t *testing.T) {
	// Seven of Outscale's schemas do not set additionalProperties: false. Where
	// the provider allowed an extra field, refusing it would be this package
	// inventing a rule rather than enforcing one.
	d := load(t)
	if vs := d.Validate("Open", value(t, `{"Known": "a", "Extra": "b"}`)); len(vs) > 0 {
		t.Fatalf("an open schema must accept an extra field, got %v", vs)
	}
}

func TestValidateReportsAMissingRequiredField(t *testing.T) {
	d := load(t)
	vs := d.Validate("Thing", value(t, `{"Size": 1}`))
	if len(vs) != 1 || vs[0].Path != "ThingId" {
		t.Fatalf("got %v, want one violation on ThingId", vs)
	}
}

func TestValidateReportsAWrongType(t *testing.T) {
	d := load(t)
	cases := map[string]string{
		`{"ThingId": 7}`:                  "ThingId",
		`{"ThingId": "t", "Size": "3"}`:   "Size",
		`{"ThingId": "t", "Ready": "no"}`: "Ready",
		`{"ThingId": "t", "Tags": {}}`:    "Tags",
	}
	for body, want := range cases {
		vs := d.Validate("Thing", value(t, body))
		if len(vs) != 1 || vs[0].Path != want {
			t.Errorf("%s: got %v, want one violation on %s", body, vs, want)
		}
	}
}

func TestIntegerAndNumberAreOneJSONType(t *testing.T) {
	// JSON has a single number type. A client decoding into an int does not care
	// that the emulator wrote 3 rather than 3.0, and reporting it would be noise
	// that trains people to ignore the report.
	d := load(t)
	if vs := d.Validate("Thing", value(t, `{"ThingId": "t", "Size": 3.0}`)); len(vs) > 0 {
		t.Fatalf("3.0 rejected for an integer field: %v", vs)
	}
}

func TestNullMeansUnset(t *testing.T) {
	// Almost nothing in the document is marked non-nullable, and an omitted
	// optional field is how every one of these APIs says "not applicable".
	d := load(t)
	if vs := d.Validate("Thing", value(t, `{"ThingId": "t", "Size": null, "Tags": null}`)); len(vs) > 0 {
		t.Fatalf("null rejected: %v", vs)
	}
}

func TestValidateChecksAnEnumeratedValue(t *testing.T) {
	d := load(t)
	if vs := d.Validate("Thing", value(t, `{"ThingId": "t", "State": "running"}`)); len(vs) > 0 {
		t.Fatalf("a documented state was rejected: %v", vs)
	}
	vs := d.Validate("Thing", value(t, `{"ThingId": "t", "State": "poweredoff"}`))
	if len(vs) != 1 || !strings.Contains(vs[0].Reason, "not one of") {
		t.Fatalf("got %v, want the state refused", vs)
	}
}

func TestValidateFollowsNestedSchemas(t *testing.T) {
	d := load(t)
	vs := d.Validate("Thing", value(t, `{"ThingId": "t", "Tags": [{"Key": "a"}, {"Nope": "b"}]}`))
	if len(vs) != 1 || vs[0].Path != "Tags[1].Nope" {
		t.Fatalf("got %v, want one violation on Tags[1].Nope", vs)
	}
}

func TestAnUnknownOperationIsItselfAViolation(t *testing.T) {
	// A route claiming an operation the API does not have is the same defect the
	// drift report calls an orphan, caught here from the other side.
	d := load(t)
	vs := d.ValidateResponse("ReadNothing", value(t, `{}`))
	if len(vs) != 1 || !strings.Contains(vs[0].Reason, "no such operation") {
		t.Fatalf("got %v, want the operation refused", vs)
	}
}

func TestAnOperationWithNoResponseSchemaValidatesNothing(t *testing.T) {
	d := load(t)
	if vs := d.ValidateResponse("Silent", value(t, `{"anything": true}`)); len(vs) > 0 {
		t.Fatalf("got %v, want nothing checked", vs)
	}
}

func TestAnEmptyContractIsRefused(t *testing.T) {
	// Loaded silently, it would validate everything and report perfect health.
	if _, err := contract.Read(strings.NewReader(`{"provider":"x"}`)); err == nil {
		t.Fatal("a contract with no schema must not load")
	}
}

func TestViolationsAreOrdered(t *testing.T) {
	// Two runs must report the same thing in the same order, or a diff of the
	// output is unreadable.
	d := load(t)
	body := value(t, `{"Things": [{"ThingId": "t", "B": 1, "A": 2, "C": 3}]}`)
	for range 5 {
		vs := d.ValidateResponse("ReadThings", body)
		if len(vs) != 3 {
			t.Fatalf("got %d violations, want 3", len(vs))
		}
		if vs[0].Path != "Things[0].A" || vs[2].Path != "Things[0].C" {
			t.Fatalf("unordered: %v", vs)
		}
	}
}

// TestAProbePlansOnlyTheRoutesItsDocumentDescribes: resolving a name is not
// owning a route. Outscale's contract keys on bare operationIds, so Scaleway's
// instance/v1/API.CreateImage resolved in it too, and the probe planned a
// Scaleway route against the Outscale path — the verdict then landed on
// whichever route the call actually hit. Owns demands the method and the path,
// not the name alone.
func TestAProbePlansOnlyTheRoutesItsDocumentDescribes(t *testing.T) {
	doc, err := contract.Read(strings.NewReader(`{
	  "provider": "stub",
	  "pathPrefix": "/api/v1",
	  "operations": {"CreateImage": {"method": "POST", "path": "/CreateImage", "response": "V"}},
	  "schemas": {"V": {"closed": false, "properties": {"ok": {"type": "boolean"}}}}
	}`))
	if err != nil {
		t.Fatal(err)
	}

	owned := contract.MountedRoute{Method: "POST", Path: "/api/v1/CreateImage", Operation: "osc/Client.CreateImage"}
	if !doc.Owns(owned) {
		t.Errorf("the route the document describes is owned: %+v", owned)
	}
	foreign := contract.MountedRoute{Method: "POST", Path: "/instance/v1/zones/{zone}/images", Operation: "instance/v1/API.CreateImage"}
	if doc.Owns(foreign) {
		t.Errorf("a route that only shares the operation's bare name is not owned: %+v", foreign)
	}
}
