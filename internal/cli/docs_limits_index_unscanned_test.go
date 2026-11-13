package cli

import (
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/drift"
)

// The index has two halves and #872 only joined one of them. The scanned rows
// are checked against `tools/drift/gate.sh products`; the 45 "not scanned" rows
// were checked against nothing, so the committed page could drift away from
// coverage/scaleway-coverage.json without a gate noticing.
//
// The review of #904 did this join by hand and it held. A measurement taken
// once is what this repository calls an assertion about the past, so here it
// is, both directions.
func TestEveryUnscannedRowIsTheArtefactsOwn(t *testing.T) {
	root := repoRoot(t)

	f, err := os.Open(filepath.Join(root, "coverage", "scaleway-coverage.json"))
	if err != nil {
		t.Fatalf("read the coverage artefact: %v", err)
	}
	defer func() { _ = f.Close() }()
	rep, err := drift.LoadCoverage(f)
	if err != nil {
		t.Fatalf("decode the coverage artefact: %v", err)
	}
	if len(rep.Unscanned) == 0 {
		t.Skip("the artefact lists no unscanned product, so the page has none to carry")
	}

	body, err := os.ReadFile(filepath.Join(root, "docs", "limits.md"))
	if err != nil {
		t.Fatal(err)
	}
	idx := strings.Index(string(body), "### Scaleway")
	if idx < 0 {
		t.Fatal("docs/limits.md carries no Scaleway section in its limits index")
	}
	section := string(body)[idx:]
	if next := strings.Index(section[1:], "\n### "); next >= 0 {
		section = section[:next+1]
	}

	var onPage []string
	for product, status := range indexRows(section) {
		if status == "**not scanned**" {
			onPage = append(onPage, product)
		}
	}
	sort.Strings(onPage)

	artefact := append([]string(nil), rep.Unscanned...)
	sort.Strings(artefact)

	for _, product := range artefact {
		if !slices.Contains(onPage, product) {
			t.Errorf("coverage/scaleway-coverage.json lists %q as unscanned and the page has no "+
				"row for it: a reader asking whether their stack is in scope gets no answer", product)
		}
	}
	for _, product := range onPage {
		if !slices.Contains(artefact, product) {
			t.Errorf("the page marks %q not scanned and the artefact does not list it: the table "+
				"names a product no measurement produced", product)
		}
	}
	if len(onPage) != len(artefact) {
		t.Errorf("the page carries %d unscanned rows for %d in the artefact",
			len(onPage), len(artefact))
	}
}
