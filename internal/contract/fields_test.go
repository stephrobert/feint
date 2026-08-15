package contract_test

import (
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
)

// The flattening is the source the omission check (#88) holds answers to, so
// what it declares must be exactly what the document declares: every ref
// followed, arrays of objects opened, arrays of scalars and opaque objects
// stopped at — a path invented here would become a false omission on every
// run, and a path dropped here a blind spot forever.

const fieldsContract = `{
  "provider": "stub",
  "operations": {
    "GetThing": {"method": "GET", "path": "/thing", "response": "ThingView"},
    "NoBody": {"method": "DELETE", "path": "/thing", "response": ""},
    "Recurse": {"method": "GET", "path": "/tree", "response": "Node"}
  },
  "schemas": {
    "ThingView": {"closed": true, "properties": {
      "name": {"type": "string"},
      "state": {"ref": "ThingState"},
      "image": {"ref": "Image"},
      "volumes": {"type": "array", "items": {"ref": "Volume"}},
      "tags": {"type": "array", "items": {"type": "string"}},
      "metadata": {"type": "object"}
    }},
    "ThingState": {"type": "string", "enum": ["running", "stopped"]},
    "Image": {"closed": true, "properties": {"id": {"type": "string"}, "arch": {"type": "string"}}},
    "Volume": {"closed": true, "properties": {"id": {"type": "string"}, "size": {"type": "integer"}}},
    "Node": {"closed": false, "properties": {
      "value": {"type": "string"},
      "children": {"type": "array", "items": {"ref": "Node"}}
    }}
  }
}`

func fieldsDoc(t *testing.T) *contract.Doc {
	t.Helper()
	doc, err := contract.Read(strings.NewReader(fieldsContract))
	if err != nil {
		t.Fatalf("read the stub contract: %v", err)
	}
	return doc
}

func TestResponseFieldsFollowRefsAndOpenArraysOfObjects(t *testing.T) {
	fields := fieldsDoc(t).ResponseFields("GetThing")

	want := map[string]string{
		"name":           "string",
		"state":          "string", // a ref to a named scalar carries its type
		"image":          "object",
		"image.id":       "string",
		"image.arch":     "string",
		"volumes":        "array",
		"volumes[]":      "object",
		"volumes[].id":   "string",
		"volumes[].size": "integer",
		"tags":           "array",  // scalars declare nothing below themselves
		"metadata":       "object", // opaque: its keys are data, not declaration
	}
	for path, typ := range want {
		if got := fields[path]; got != typ {
			t.Errorf("field %q: got %q, want %q", path, got, typ)
		}
	}
	for path := range fields {
		if _, expected := want[path]; !expected {
			t.Errorf("field %q was declared by nothing in the document", path)
		}
	}
}

func TestAnOperationWithoutAResponseSchemaDeclaresNothing(t *testing.T) {
	if fields := fieldsDoc(t).ResponseFields("NoBody"); fields != nil {
		t.Errorf("no response schema must declare no fields, got %v", fields)
	}
	if fields := fieldsDoc(t).ResponseFields("NoSuchOperation"); fields != nil {
		t.Errorf("an unknown operation must declare no fields, got %v", fields)
	}
}

// TestResponseFieldsSurviveARecursiveSchema is the guard flattenSchema cites: a
// schema reachable from itself — and the providers publish them — must be
// recorded where first met and never descended into again, or the flattening
// recurses forever.
func TestResponseFieldsSurviveARecursiveSchema(t *testing.T) {
	fields := fieldsDoc(t).ResponseFields("Recurse")

	if fields["value"] != "string" || fields["children"] != "array" {
		t.Fatalf("the first level must be declared, got %v", fields)
	}
	if fields["children[]"] != "object" {
		t.Errorf("the element of the recursive array is still an object, got %v", fields)
	}
	for path := range fields {
		if strings.Contains(path, "children[].children[]") {
			t.Errorf("the recursion was followed into itself: %q", path)
		}
	}
}
