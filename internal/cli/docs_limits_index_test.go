package cli

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/stephrobert/feint/internal/drift"
)

// The limits index (#872): the top of docs/limits.md answers "is my stack in
// scope" in one table, generated from coverage/ and guarded by docs --check.
//
// A reader with `scaleway_rdb_instance` could not learn from the 5,063-line
// page that their stack is outside the scan entirely — the fact sat at
// docs/limits.md:3316 while the first section started at line 7. The table
// names every product the scan covers and, loudly, every product it does not,
// because "not scanned" is the only row where a client silently leaves the
// machine (docs/adoption.md: the client reaches the real endpoint with
// whatever credentials the shell holds).
func TestRenderLimitsIndexNamesEveryProductAndItsStatus(t *testing.T) {
	reports := []drift.CoverageFile{
		{
			Provider:    "scaleway",
			Total:       548,
			Implemented: 204,
			Declined:    344,
			Products: []drift.ProductView{
				{Product: "instance", Total: 157, Implemented: 73, Declined: 84},
				{Product: "scw", Total: 1, Implemented: 1},
				{Product: "marketplace", Total: 8, Implemented: 1, Declined: 7},
			},
			Unscanned: []string{"rdb", "k8s"},
		},
		{
			Provider:    "outscale",
			Total:       263,
			Implemented: 100,
			Declined:    163,
			Products: []drift.ProductView{
				{Product: "oks", Total: 27, Implemented: 0, Declined: 27},
				{Product: "osc", Total: 236, Implemented: 100, Declined: 136},
			},
		},
	}

	out := renderLimitsIndex(reports)

	// The generated marker: this section is owned by `feint docs`, not by hand.
	if !strings.Contains(out, docsGenerated) {
		t.Errorf("the index carries no generated marker:\n%s", out)
	}
	// The population is named, not implied: 3 scanned of 5 the artefact knows.
	if !strings.Contains(out, "covers 3 of the 5 products") {
		t.Errorf("the index does not name the population it counted:\n%s", out)
	}
	// Scanned rows, alphabetical, with the status the counts earn.
	for _, want := range []string{
		"| `instance` | partially served | 73 | 84 | 0 | 157 |",
		"| `marketplace` | partially served | 1 | 7 | 0 | 8 |",
		"| `scw` | served | 1 | 0 | 0 | 1 |",
		"| `oks` | declined | 0 | 27 | 0 | 27 |",
		"| `osc` | partially served | 100 | 136 | 0 | 236 |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing row %q:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "`instance`"), strings.Index(out, "`marketplace`"); i < 0 || j < 0 || i > j {
		t.Errorf("scanned rows are not alphabetical:\n%s", out)
	}
	// The not-scanned rows are loud: bold status, no counts to mistake for a
	// measurement, and they come after the scanned rows, not mixed in.
	for _, want := range []string{
		"| `k8s` | **not scanned** | — | — | — | — |",
		"| `rdb` | **not scanned** | — | — | — | — |",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("missing not-scanned row %q:\n%s", want, out)
		}
	}
	if i, j := strings.Index(out, "`scw`"), strings.Index(out, "`k8s`"); i < 0 || j < 0 || i > j {
		t.Errorf("not-scanned rows do not follow the scanned rows:\n%s", out)
	}
	// The consequence is stated where the row is, not pages away: a stack on an
	// unscanned product leaves the machine.
	if !strings.Contains(out, "reaches the real endpoint") {
		t.Errorf("the index never says what not scanned costs:\n%s", out)
	}
	// A provider scanned whole carries no not-scanned warning of its own: a
	// warning that fires where nothing is unscanned is a warning people learn
	// to ignore. The Outscale section ends at the next provider heading.
	if idx := strings.Index(out, "### Outscale"); idx >= 0 {
		section := out[idx:]
		if next := strings.Index(section[1:], "\n### "); next >= 0 {
			section = section[:next+1]
		}
		if strings.Contains(section, "**Not scanned**") || strings.Contains(section, "reaches the real endpoint") {
			t.Errorf("a provider scanned whole carries a not-scanned warning:\n%s", section)
		}
	}
}

// The README links the index, and the link resolves (#872: "linked from the
// README"). A link that stopped resolving reads exactly like one that never
// did, which is why the docs index test holds its own links to the same rule.
//
// README.md only, deliberately: README.fr.md is the shorter page and carries no
// Coverage section, so there is no French link to check. The day one is added
// it belongs here too, or it is a link nobody tests.
func TestTheReadmeLinksTheLimitsIndex(t *testing.T) {
	root := repoRoot(t)
	readme, err := os.ReadFile(filepath.Join(root, "README.md"))
	if err != nil {
		t.Fatal(err)
	}
	limits, err := os.ReadFile(filepath.Join(root, "docs", "limits.md"))
	if err != nil {
		t.Fatal(err)
	}
	const target = "docs/limits.md#product-coverage-at-a-glance"
	if !strings.Contains(string(readme), target) {
		t.Fatalf("the README does not link the limits index (%s)", target)
	}
	if !hasAnchor(string(limits), "product-coverage-at-a-glance") {
		t.Errorf("docs/limits.md carries no heading producing #product-coverage-at-a-glance")
	}
}

// TestTheLimitsIndexCoversEveryProductTheGateScans is the join the
// maintainer asked for on #872: `tools/drift/gate.sh products` is the single
// source both the gate and the nightly triage view read, and the page must
// not be a third copy that can drift from it. The defect it closes was
// measured on the triage view itself: it covered three products while the
// gate scanned eleven, for two months, without anything saying so (#886).
//
// What is asserted, in both directions: every product the gate scans has a
// scanned row in the Scaleway section of the committed page, and every
// scanned row there names a product the gate scans. The page is read as
// committed, not rendered, because failing to regenerate after a product
// joins the gate is exactly the defect — a rendered table would pass while
// the page a reader opens still missed it.
func TestTheLimitsIndexCoversEveryProductTheGateScans(t *testing.T) {
	root := repoRoot(t)

	cmd := exec.Command(filepath.Join(root, "tools", "drift", "gate.sh"), "products")
	// gate.sh reads SCALEWAY_PRODUCTS="${FEINT_PRODUCTS:-<default>}". An
	// exported FEINT_PRODUCTS would make this a test of whoever runs it, and a
	// harness that measures its own environment is the failure this repository
	// keeps paying for. Empty falls through to the default because of `:-`.
	cmd.Env = append(os.Environ(), "FEINT_PRODUCTS=")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("tools/drift/gate.sh products: %v (the page cannot name the products without it)", err)
	}
	gate := strings.Split(strings.TrimSpace(string(out)), ",")
	if len(gate) < 2 {
		t.Fatalf("gate.sh products answered %q, which is not a product list", string(out))
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
	rows := indexRows(section)

	for _, product := range gate {
		status, ok := rows[product]
		if !ok {
			t.Errorf("the gate scans %q and the limits index has no row for it: a product added to the gate is missing from the page", product)
			continue
		}
		if status == "**not scanned**" {
			t.Errorf("the gate scans %q and the limits index marks it not scanned: the page contradicts the gate", product)
		}
	}
	for product, status := range rows {
		if status == "**not scanned**" {
			continue
		}
		if !slices.Contains(gate, product) {
			t.Errorf("the limits index shows %q as scanned and the gate does not scan it: the page claims a product the scan never measured", product)
		}
	}
}

// indexRows reads a rendered index section into product -> status. A row is
// `| `name` | status | ... |`; anything else in the section is prose.
func indexRows(section string) map[string]string {
	rows := make(map[string]string)
	for _, line := range strings.Split(section, "\n") {
		if !strings.HasPrefix(line, "| `") {
			continue
		}
		fields := strings.Split(line, "|")
		if len(fields) < 3 {
			continue
		}
		product := strings.Trim(strings.TrimSpace(fields[1]), "`")
		rows[product] = strings.TrimSpace(fields[2])
	}
	return rows
}
