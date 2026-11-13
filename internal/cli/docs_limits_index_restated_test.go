package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docs.go states the rule this test enforces: "a restated figure is a second
// source of truth that disagrees with the first by the next release".
//
// #872 generated "The scan covers 11 of the 56 products upstream declares" at
// the top of docs/limits.md, and left a hand-written "The Scaleway scan covers
// 11 of the 56 products in that SDK" at line 664 of the same file. Two sources
// for one number, in one page, and only one of them moves when the scan does.
//
// So: a count the index generates may not appear again in the prose around it.
// The numbers are read off the generated block rather than written here, so the
// day the scan covers 12 products this test follows without an edit.
func TestNoHandWrittenProseRestatesTheGeneratedCounts(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "docs", "limits.md")
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the limits page: %v", err)
	}
	page := string(body)

	const startMarker = "<!-- limits-index:start -->"
	const endMarker = "<!-- limits-index:end -->"
	index := between(page, startMarker, endMarker)
	if index == "" {
		t.Skip("docs/limits.md carries no generated index, so there is nothing to restate")
	}

	// "The scan covers 11 of the 56 products upstream declares."
	phrase := regexp.MustCompile(`covers (\d+) of the (\d+) products`)
	m := phrase.FindStringSubmatch(index)
	if m == nil {
		t.Skipf("the generated index no longer states a covered-of-total count; "+
			"update this check or drop it\n%s", index)
	}

	prose := strings.Replace(page, startMarker+index+endMarker, "", 1)
	restated := fmt.Sprintf("%s of the %s products", m[1], m[2])
	if strings.Contains(prose, restated) {
		t.Errorf("docs/limits.md restates %q by hand while the index generates it. Two sources for "+
			"one number disagree by the next release, and the hand-written one is the one nobody "+
			"regenerates. Point at the index instead:\n"+
			"  [Product coverage at a glance](#product-coverage-at-a-glance)", restated)
	}
}
