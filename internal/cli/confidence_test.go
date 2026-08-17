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
