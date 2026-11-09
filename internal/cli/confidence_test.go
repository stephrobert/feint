package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every row of docs/confidence.md resolves to something that exists (#130).
//
// The page answers "what can I validate here" in a user's vocabulary, which is
// exactly the shape that rots into a brochure: the rows are prose, the things
// they point at are files and sections that get renamed, and a link that stops
// resolving reads the same as one that never did. The issue states the rule —
// *a row without an anchor does not ship* — and this is the rule as a control.
//
// Three things are checked, and the third is the one that matters:
//
//  1. the page has rows at all, so a truncated file cannot pass by being empty;
//  2. every row carries at least one link;
//  3. every link resolves — the file exists, and when the link carries a `#`
//     fragment, some heading in that file really produces it.
//
// The heading-to-anchor rule is GitHub's: lowercase, spaces to hyphens,
// everything else that is not a letter, digit or hyphen dropped. It is
// reimplemented here rather than imported, which is worth stating: if GitHub
// changed it, this test would agree with itself and disagree with the web. The
// alternative is fetching the rendered page, and a documentation test that needs
// the network is a test that gets skipped.
func TestEveryConfidenceRowCarriesItsProof(t *testing.T) {
	root := repoRoot(t)
	page := filepath.Join(root, "docs", "confidence.md")
	body, err := os.ReadFile(page)
	if err != nil {
		t.Fatalf("read the confidence page: %v", err)
	}

	link := regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
	rows := 0
	for _, line := range strings.Split(string(body), "\n") {
		// A table row of the confidence table: three cells, and the header and
		// separator lines are not rows.
		if !strings.HasPrefix(line, "| ") || strings.HasPrefix(line, "|---") ||
			strings.Contains(line, "What you want to validate") {
			continue
		}
		rows++

		matches := link.FindAllStringSubmatch(line, -1)
		if len(matches) == 0 {
			t.Errorf("this row points at nothing, so nobody can check it:\n  %s", line)
			continue
		}
		for _, m := range matches {
			target := m[1]
			if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
				// An external link cannot be resolved offline, and a
				// documentation test must not need the network.
				continue
			}
			file, fragment, _ := strings.Cut(target, "#")
			if file == "" {
				file = "confidence.md"
			}
			path := filepath.Join(root, "docs", file)
			content, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
			if err != nil {
				t.Errorf("row links %q and that file does not exist:\n  %s", target, line)
				continue
			}
			if fragment == "" {
				continue
			}
			if !hasAnchor(string(content), fragment) {
				t.Errorf("row links %q and no heading in %s produces that anchor:\n  %s",
					target, file, line)
			}
		}
	}

	// A page whose table vanished would otherwise pass by having no row to fail.
	if rows < 10 {
		t.Fatalf("the confidence table carries %d row(s): the page is the answer to "+
			"one question and cannot answer it in that few", rows)
	}
}

// hasAnchor reports whether any Markdown heading in the document produces the
// fragment, by GitHub's own slug rule.
func hasAnchor(document, fragment string) bool {
	for _, line := range strings.Split(document, "\n") {
		if !strings.HasPrefix(line, "#") {
			continue
		}
		heading := strings.TrimLeft(line, "#")
		if slugOf(heading) == fragment {
			return true
		}
	}
	return false
}

// slugOf renders a heading the way GitHub anchors it: trimmed, lowercased,
// spaces to hyphens, and every other character that is not a letter, a digit or
// a hyphen dropped. Backticks, commas and colons are common in this repository's
// headings and all three disappear.
func slugOf(heading string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(strings.TrimSpace(heading)) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-':
			b.WriteRune(r)
		case r == ' ':
			b.WriteRune('-')
		}
	}
	return b.String()
}

// TestTheHardcodedEndpointRowIsOwnedByAnInstrument is #850's criterion 59 as a
// control rather than as a promise.
//
// That criterion asks for "a generated/bounded capability statement, not
// hand-edited marketing prose". The row cannot be GENERATED from
// capabilityMatrix: that matrix admits one axis, a client against a pack, and
// this is a different one — a data-plane capability for a pair already marked
// supported on the control plane. Widening it for one row would mean a new mode
// and a new proof kind, and capability.go records why that is the wrong move
// (#644 deleted a proof kind precisely because no row rested on it, leaving a
// resolver nobody exercised).
//
// So the row is BOUNDED instead, which is the other half of the criterion and
// the part that actually prevents the defect. The claim and the instrument are
// tied: the row may say "opt-in" only while the conformance workflow runs the
// witness that establishes it, and the witness must exist. Delete the job, or
// rename the test, and this fails — which is what "a sentence nobody owns
// cannot be contradicted" was asking for.
func TestTheHardcodedEndpointRowIsOwnedByAnInstrument(t *testing.T) {
	const witness = "TestScalewayTerraformStorageThroughFeintRunOnTheHostNetwork"
	root := repoRoot(t)

	page, err := os.ReadFile(filepath.Join(root, "docs", "confidence.md"))
	if err != nil {
		t.Fatal(err)
	}
	var row string
	for _, line := range strings.Split(string(page), "\n") {
		if strings.Contains(line, "Hardcoded Scaleway Terraform Object Storage endpoints") {
			row = line
			break
		}
	}
	if row == "" {
		t.Fatal("docs/confidence.md no longer carries the hardcoded-endpoint row; if the subject is gone, this control goes with it")
	}
	if strings.Contains(row, "**no**") {
		// Nothing more to check: a refusal needs no instrument, and the row
		// below is about what a CLAIM costs.
		return
	}

	if !strings.Contains(row, witness) {
		t.Fatalf("the row claims a capability without naming the witness that establishes it:\n%s", row)
	}
	workflow, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "conformance.yml"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(workflow), witness) {
		t.Fatalf("docs/confidence.md claims the hardcoded-endpoint path while conformance.yml no longer runs %s: "+
			"the claim would rest on a measurement nobody takes", witness)
	}
	source, err := os.ReadFile(filepath.Join(root, "internal", "cli", "run_terraform_conformance_test.go"))
	if err != nil {
		t.Fatalf("the witness file the row and the workflow both name is unreadable: %v", err)
	}
	if !strings.Contains(string(source), "func "+witness+"(") {
		t.Fatalf("%s is named by the row and by the workflow and defined nowhere: `go test -run` on a name that "+
			"matches nothing exits 0, so this would be a green job proving nothing", witness)
	}
	// The row must say what it does NOT cover. The witness is not the whole
	// story — OpenTofu is untested on this path — and a capability row that
	// only lists what works is the brochure this page exists to avoid.
	if !strings.Contains(strings.ToLower(row), "opentofu") {
		t.Fatalf("the row claims the path without naming what it leaves out:\n%s", row)
	}
}
