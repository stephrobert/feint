package cli

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"testing"

	"github.com/stephrobert/feint/internal/core/emulator"
)

// The seam #26 and #356 both name: the core decides *when* a call fails and
// with which status, the pack decides *what that failure looks like*.
//
// This is where the second half is held, because it is the only place all three
// packs and all three committed contracts are in the same room. Two questions,
// and both have to be asked:
//
//   - does every pack render a failure at all, and in a body its own client can
//     decode? An injected fault whose body no SDK reads is worse than no
//     injection: the client meets a decoding error where it should meet an API
//     error, and the emulator looks broken rather than unavailable. That is
//     #26's open question 4, answered against the artefact rather than by
//     reading.
//   - is the answer the provider's own dialect, or one shape wearing three
//     names? A 503 that reached all three clients identically would be a
//     failure of *this tool*, which is the one thing a client never sees from
//     its cloud.

// TestEveryPackRendersItsFaultsInItsOwnDialect asserts the presence of the
// subject before measuring it: a pack that stopped implementing Faulter would
// otherwise make this test pass by having nothing to check.
func TestEveryPackRendersItsFaultsInItsOwnDialect(t *testing.T) {
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}
	docs, err := loadContracts(filepath.Join("..", "..", "contracts"))
	if err != nil {
		t.Fatalf("load the contracts: %v", err)
	}

	packs := srv.Packs()
	if len(packs) == 0 {
		t.Fatal("no pack is mounted, so this test measures nothing")
	}

	// One body per pack for the same status, so the dialects can be compared
	// against each other rather than each against itself.
	rendered := map[string]string{}
	for _, p := range packs {
		faulter, renders := p.(emulator.Faulter)
		if !renders {
			t.Errorf("the %s pack renders no error dialect, so no status can be injected on any of "+
				"its operations: a fault is only worth injecting if the client decodes it", p.Name())
			continue
		}
		statuses := faulter.FaultStatuses()
		if len(statuses) == 0 {
			t.Errorf("the %s pack declares no renderable status", p.Name())
		}

		doc := docs[p.Name()]
		if doc == nil {
			t.Fatalf("no contract for %s: the check below would pass by having nothing to compare", p.Name())
		}
		if doc.ErrorSchema == "" {
			t.Fatalf("the %s contract declares no error schema, so an injected refusal cannot be "+
				"validated at all", p.Name())
		}

		for _, status := range statuses {
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodGet, "/some/path", nil)
			faulter.WriteFault(rec, req, status)

			if rec.Code != status {
				t.Errorf("%s: a %d fault rendered %d", p.Name(), status, rec.Code)
			}
			var decoded any
			if err := json.Unmarshal(rec.Body.Bytes(), &decoded); err != nil {
				t.Errorf("%s: the %d body is not JSON its client could decode: %v", p.Name(), status, err)
				continue
			}
			// The provider's own declared refusal shape. An injected 429 the
			// SDK cannot decode is worse than no injection.
			if vs := doc.Validate(doc.ErrorSchema, decoded); len(vs) > 0 {
				t.Errorf("%s: the %d body does not match %s, the shape the provider declares for a "+
					"refusal: %v", p.Name(), status, doc.ErrorSchema, vs)
			}
			if status == http.StatusServiceUnavailable {
				rendered[p.Name()] = rec.Body.String()
			}
		}
	}

	if len(rendered) < 2 {
		t.Fatalf("only %d pack rendered a 503, so no two dialects can be compared", len(rendered))
	}
	seen := map[string]string{}
	for name, body := range rendered {
		if other, clash := seen[body]; clash {
			t.Errorf("%s and %s render an identical 503:\n  %s\nthat is one shape wearing two names, "+
				"and a client would be meeting a failure of this tool rather than of its cloud",
				name, other, body)
		}
		seen[body] = name
	}
}

// The statuses the packs offer are the same set on purpose — the core asks for
// one vocabulary — and a pack that quietly narrowed its own would make a rule
// that works against Scaleway fail against Exoscale for no reason a user could
// see. Held here rather than in each pack, for the reason CLAUDE.md gives about
// the shared layer: a control copied into three packs is a control the fourth
// forgets.
func TestThePacksOfferTheSameFaultVocabulary(t *testing.T) {
	srv, _, err := newServer(nil)
	if err != nil {
		t.Fatalf("build the emulator: %v", err)
	}

	var reference []int
	var referenceName string
	for _, p := range srv.Packs() {
		faulter, renders := p.(emulator.Faulter)
		if !renders {
			continue
		}
		statuses := faulter.FaultStatuses()
		if reference == nil {
			reference, referenceName = statuses, p.Name()
			continue
		}
		if len(statuses) != len(reference) {
			t.Errorf("%s offers %v and %s offers %v", p.Name(), statuses, referenceName, reference)
			continue
		}
		for i := range statuses {
			if statuses[i] != reference[i] {
				t.Errorf("%s offers %v and %s offers %v", p.Name(), statuses, referenceName, reference)
				break
			}
		}
	}
	if reference == nil {
		t.Fatal("no pack declares a fault vocabulary, so this test measures nothing")
	}
}
