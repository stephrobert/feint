package shape

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/trace"
)

// A redaction erases a type; it does not report one.
//
// The recorder writes a string over whatever it replaced, so a bool comes back
// as "REDACTED-3f2a" and an array as "REDACTED-20". A catalogue whose entire
// content is paths and types must learn nothing from that: folding it in
// publishes a polymorphism the provider does not have.
//
// Measured on the committed corpora on 2026-08-23, before this guard existed:
// folding corpus/outscale/oapi-cli-lifecycle.jsonl turned
// `osc/Client.ReadKeypairs.Keypairs` from `array` into `array|string` and
// `osc/Client.ReadLoadBalancers.LoadBalancers[].SecuredCookies` from `bool` into
// `bool|string`, on top of types a `feint shapes --record` run had got right.
func TestARedactedValueTeachesNoType(t *testing.T) {
	cat := New("outscale")
	cat.Merge([]trace.Exchange{exchange("ReadKeypairs", map[string]any{
		"Keypairs": "REDACTED-20",
		"Name":     "redacted-7",
		"Count":    json.Number("2"),
	})})

	op := cat.Operations["osc/Client.ReadKeypairs"]
	if op == nil {
		t.Fatalf("the operation was dropped entirely; only the redacted field should be")
	}
	types := map[string]string{}
	for _, f := range op.Fields {
		types[f.Path] = f.Type
	}
	if got, present := types["Keypairs"]; present {
		t.Errorf("Keypairs was learned as %q from a value the recorder replaced", got)
	}
	// The witness: a field the recorder did not touch must still be learned, or
	// this test would pass on a walk that returned nothing at all.
	if types["Count"] != "number" {
		t.Errorf("Count = %q, want number: the walk stopped finding anything", types["Count"])
	}
	// The sanitiser's own token is a string that replaced a string, so the type
	// it reports is right and only the value is gone — but it is still not an
	// observation, and a catalogue that recorded it would say the API returns a
	// field whose only witness is an invention.
	if got, present := types["Name"]; present {
		t.Errorf("Name was learned as %q from the sanitiser's own token", got)
	}
}

// Folding a recording never unlearns a type a richer recording already had.
//
// The regression this pins is the one measured on the committed corpora: the
// catalogue held `Keypairs: array` from a direct `--record` read, and folding a
// sanitised corpus over it produced `array|string`. mergeType is right to keep
// a genuine conflict — the defect is upstream of it, in taking a redaction for
// an observation.
func TestFoldingACorpusDoesNotUnlearnAType(t *testing.T) {
	cat := New("outscale")
	cat.Merge([]trace.Exchange{exchange("ReadKeypairs", map[string]any{
		"Keypairs": []any{map[string]any{"KeypairName": "k"}},
	})})
	cat.Merge([]trace.Exchange{exchange("ReadKeypairs", map[string]any{
		"Keypairs": "REDACTED-20",
	})})

	for _, f := range cat.Operations["osc/Client.ReadKeypairs"].Fields {
		if f.Path == "Keypairs" && f.Type != "array" {
			t.Fatalf("Keypairs = %q after folding a redacted recording, want array", f.Type)
		}
	}
}

// A path the sanitiser rewrote keys nothing.
//
// corpus/scaleway/scw-cli.jsonl carries exchanges whose every path segment is
// `redacted-<n>`, and the number moves each time the corpus is re-sanitised.
// Keyed on that, the catalogue would carry a name that names no API and that
// changes under `git diff` for reasons unrelated to any cloud — the volatility
// this package's own header forbids storing.
func TestARedactedPathIsNeverAKey(t *testing.T) {
	cat := New("scaleway")
	ch := cat.Merge([]trace.Exchange{{
		Method: "GET", Path: "/redacted-749/redacted-750/redacted-751",
		Status: 200, Res: &trace.Message{Body: map[string]any{"total_count": json.Number("0")}},
	}, {
		// The witness: an ordinary unnamed exchange must still be kept under
		// its route, or a guard that dropped everything would look identical.
		Method: "GET", Path: "/instance/v1/zones/fr-par-1/servers",
		Status: 200, Res: &trace.Message{Body: map[string]any{"total_count": json.Number("0")}},
	}})

	for key := range cat.Operations {
		if strings.Contains(key, RedactionToken) {
			t.Errorf("the catalogue is keyed on %q, which names no API", key)
		}
	}
	if _, kept := cat.Operations["GET /instance/v1/zones/fr-par-1/servers"]; !kept {
		t.Fatalf("the unnamed exchange with a real path was dropped too: the guard is too wide")
	}
	if ch.Unkeyable != 1 {
		t.Errorf("Unkeyable = %d, want 1: an exchange left on the floor must be counted", ch.Unkeyable)
	}
}

// A named operation first met through a sanitised path carries no route until a
// real one turns up, and then carries that one whatever the order.
func TestAnOperationTakesTheRouteThatWasActuallyRecorded(t *testing.T) {
	redacted := trace.Exchange{
		Method: "GET", Path: "/redacted-1/redacted-2", Operation: "instance/v1/API.ListServers",
		Status: 200, Res: &trace.Message{Body: map[string]any{"total_count": json.Number("0")}},
	}
	real := trace.Exchange{
		Method: "GET", Path: "/instance/v1/zones/fr-par-1/servers", Operation: "instance/v1/API.ListServers",
		Status: 200, Res: &trace.Message{Body: map[string]any{"total_count": json.Number("0")}},
	}
	for _, order := range [][]trace.Exchange{{redacted, real}, {real, redacted}} {
		cat := New("scaleway")
		cat.Merge(order)
		op := cat.Operations["instance/v1/API.ListServers"]
		if op == nil {
			t.Fatalf("the named operation was dropped")
		}
		if op.Path != "/instance/v1/zones/fr-par-1/servers" {
			t.Errorf("Path = %q, want the recorded route", op.Path)
		}
	}
}

// The committed catalogues carry no redaction, read from the bytes on disk.
//
// The same disbelief TestNoCommittedCatalogueCarriesAnIdentifier applies to
// identifiers: this asserts the property the files must have, rather than the
// rule that is supposed to produce it. `mise run shapes:fold` writes these
// files from sanitised corpora, so the day a placeholder spelling changes it is
// this that goes red.
func TestNoCommittedCatalogueCarriesARedaction(t *testing.T) {
	dir := filepath.Join("..", "..", "shapes")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	scanned := 0
	for _, e := range entries {
		if !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, e.Name())) //nolint:gosec // a committed artefact of this repository
		if err != nil {
			t.Fatalf("read %s: %v", e.Name(), err)
		}
		scanned++
		for _, token := range []string{RedactionToken, recorderToken} {
			if i := strings.Index(string(body), token); i >= 0 {
				t.Errorf("%s carries %q at byte %d, which the sanitiser invented", e.Name(), token, i)
			}
		}
	}
	// Asserted rather than assumed: a scan that found no file would pass while
	// measuring nothing.
	if scanned == 0 {
		t.Fatalf("no catalogue found in %s: the scan is broken, not the files", dir)
	}
}
