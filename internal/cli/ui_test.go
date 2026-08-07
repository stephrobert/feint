package cli

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stephrobert/feint/internal/drift"
)

// The boundary between the versioned artefact and the page carries the reasons.
//
// This is where a drop would be silent. The page's own test uses a stub reader,
// so it proves the endpoint publishes what it is handed and nothing about what
// this function hands it; and the artefact's test proves the file carries the
// reasons and nothing about who reads them. Neither notices if the crossing
// forgets them, which is exactly the shape of the defect that made `feint
// status` print zero: two ends that were each correct.
//
// The fixture is written by the producer rather than typed here. A hand-written
// coverage file would be a fixture made of what somebody assumed the format is,
// and this repository has already paid for one of those.
func TestTheUpstreamGapCarriesTheDeclineReasons(t *testing.T) {
	dir := t.TempDir()
	report := drift.Compare("a-cloud",
		[]drift.Operation{
			{Name: "compute/v1/API.List", Product: "compute", Version: "v1"},
			{Name: "compute/v1/API.Drop", Product: "compute", Version: "v1"},
			{Name: "compute/v1/API.New", Product: "compute", Version: "v1"},
		},
		[]string{"compute/v1/API.List"},
		map[string]string{"compute/v1/API.Drop": "the emulator runs nothing this would drop"},
	)
	f, err := os.Create(filepath.Join(dir, "a-cloud-coverage.json"))
	if err != nil {
		t.Fatalf("write the fixture: %v", err)
	}
	if err := report.WriteJSON(f); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}
	if err := f.Close(); err != nil {
		t.Fatalf("write the fixture: %v", err)
	}

	view := upstreamGap(dir)()
	if !view.Available {
		t.Fatal("the artefact was not read at all")
	}
	if len(view.Products) != 1 || view.Products[0].Provider != "a-cloud" {
		t.Fatalf("the products did not cross: %+v", view.Products)
	}
	if len(view.Operations) != 3 {
		t.Fatalf("the page was handed %d operations behind the counts, want 3", len(view.Operations))
	}

	var declined, served, untriaged int
	for _, op := range view.Operations {
		if op.Provider != "a-cloud" || op.Product != "compute" {
			t.Errorf("an operation crossed without the product it belongs to: %+v", op)
		}
		switch op.Status {
		case "declined":
			declined++
			if op.Reason == "" {
				t.Error("a decline crossed without its reason, which is the only thing that makes the count actionable")
			}
		case "implemented":
			served++
		case "unknown":
			untriaged++
		}
	}
	if declined != 1 || served != 1 || untriaged != 1 {
		t.Errorf("the verdicts did not survive: %d declined, %d served, %d untriaged", declined, served, untriaged)
	}

	// And the accepting half of the other direction: no artefact means unknown,
	// never a page full of zeroes that reads as a fully covered API.
	empty := upstreamGap(t.TempDir())()
	if empty.Available || len(empty.Operations) != 0 {
		t.Errorf("an empty directory produced a gap that claims to be measured: %+v", empty)
	}
}
