package contract_test

import (
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/contract"
)

// A missing response schema has three causes, and only one of them is a
// statement about the wire. Folding them into one is what left thirty-one
// served Scaleway operations at zero on the probed and contract axes (#429),
// so each cause gets a case here.

const emptiness = `{
  "provider": "test",
  "operations": {
    "Erase":  {"path": "/Erase",  "method": "DELETE", "noContent": 204},
    "Silent": {"path": "/Silent", "method": "DELETE"},
    "Bodied": {"path": "/Bodied", "method": "GET", "response": "Thing"}
  },
  "schemas": {"Thing": {"closed": true, "properties": {"Id": {"type": "string"}}}}
}`

func emptinessDoc(t *testing.T) *contract.Doc {
	t.Helper()
	d, err := contract.Read(strings.NewReader(emptiness))
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	return d
}

func TestADeclaredEmptyAnswerIsValidated(t *testing.T) {
	d := emptinessDoc(t)

	vs, checkable := d.ValidateEmptyResponse("Erase", 204, 0)
	if !checkable {
		t.Fatal("a document declaring 204 with no content states something, and it must be checkable")
	}
	if len(vs) > 0 {
		t.Fatalf("204 with no body against a document declaring exactly that: %v", vs)
	}
}

func TestADeclaredEmptyAnswerIsBrokenByABodyAndByAStatus(t *testing.T) {
	d := emptinessDoc(t)

	for _, tc := range []struct {
		name        string
		status, len int
	}{
		{"a body where none is declared", 204, 17},
		{"a status the document does not name", 200, 0},
		{"both at once", 200, 17},
	} {
		t.Run(tc.name, func(t *testing.T) {
			vs, checkable := d.ValidateEmptyResponse("Erase", tc.status, tc.len)
			if !checkable {
				t.Fatal("checkable must stay true: the document has not stopped declaring anything")
			}
			if len(vs) == 0 {
				t.Fatal("want a violation, got none")
			}
		})
	}
}

// The witness. `Silent` and `Bodied` are the two causes that are not a
// statement about an empty answer, and neither may be read as one: the second
// return says so, and a caller that ignores it records a validation nobody
// performed.
func TestAnUnnameableResponseIsNotReadAsNoContent(t *testing.T) {
	d := emptinessDoc(t)

	for _, op := range []string{"Silent", "Bodied"} {
		if vs, checkable := d.ValidateEmptyResponse(op, 204, 0); checkable || len(vs) > 0 {
			t.Errorf("%s: the document declares no empty answer, want (nil, false), got (%v, %v)", op, vs, checkable)
		}
	}
}

// The live half of the same distinction, held against the committed artefacts
// rather than a fixture, because the fixture cannot go stale and the artefacts
// can.
//
// Exoscale's list-events declares a 200 carrying a top-level array of $ref,
// which this extraction cannot name, so its `response` is empty for a reason
// that has nothing to do with emptiness. Scaleway's DeleteServer declares
// `204: {description: ”}`. Both artefacts must say which is which.
func TestTheCommittedContractsSeparateSilenceFromDeclaredEmptiness(t *testing.T) {
	for _, tc := range []struct {
		file, operation string
		wantNoContent   int
	}{
		{"scaleway.json", "instance/v1.DeleteServer", 204},
		{"scaleway.json", "instance/v1.SetServerUserData", 204},
		{"scaleway.json", "instance/v1.GetServerUserData", 0},
		{"exoscale.json", "list-events", 0},
	} {
		d, err := contract.Load(filepath.Join("..", "..", "contracts", tc.file))
		if err != nil {
			t.Fatalf("load %s: %v", tc.file, err)
		}
		op, ok := d.Operations[tc.operation]
		if !ok {
			t.Fatalf("%s: the artefact no longer defines %s", tc.file, tc.operation)
		}
		if op.NoContent != tc.wantNoContent {
			t.Errorf("%s %s: noContent %d, want %d", tc.file, tc.operation, op.NoContent, tc.wantNoContent)
		}
	}
}
