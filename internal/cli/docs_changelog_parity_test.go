package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The two changelogs carry the same entries, section by section.
//
// Measured on 2026-08-29, minutes before tagging 0.12.0: `## [Unreleased]` held
// **18 entries in English and 15 in French**. Three had been written in one file
// and not the other — the Account product's projects (#372), the four response
// shapes (#366/#367/#368), and the block volume returning to `available` (#365).
//
// Nothing caught it. `feint docs --check` verifies the GENERATED blocks, and a
// changelog entry is prose somebody writes by hand in two places. So the two
// files drifted for as long as it took somebody to count them, which happened
// once, by accident, on the day of a release.
//
// This is the class this repository names first: a control everybody assumes
// exists because a neighbouring one does. The remedy is not to write more
// carefully — it is to count.

// changelogEntry is what a released line looks like in both files: a bolded
// claim opening a list item. Sub-paragraphs are indented continuations and are
// deliberately not counted, because a translator may split a paragraph.
const changelogEntry = "- **"

func TestBothChangelogsCarryTheSameUnreleasedEntries(t *testing.T) {
	count := func(name string) int {
		path := filepath.Join("..", "..", name)
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		n, inside := 0, false
		for _, line := range strings.Split(string(source), "\n") {
			switch {
			case strings.HasPrefix(line, "## [Unreleased]"):
				inside = true
			case inside && strings.HasPrefix(line, "## ["):
				inside = false
			case inside && strings.HasPrefix(line, changelogEntry):
				n++
			}
		}
		return n
	}

	en, fr := count("CHANGELOG.md"), count("CHANGELOG.fr.md")

	// The reader proves it can find before it judges. A prefix that matched
	// nothing would report 0 == 0 and pass over two empty files.
	if en == 0 {
		t.Fatal("no entry was found in CHANGELOG.md, so this test measured nothing")
	}

	if en != fr {
		t.Errorf("CHANGELOG.md carries %d unreleased entries and CHANGELOG.fr.md carries %d: "+
			"an entry written in one file and not the other is a change a reader of one "+
			"language never learns about", en, fr)
	}
}
