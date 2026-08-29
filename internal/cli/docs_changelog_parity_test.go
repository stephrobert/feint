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
	// Two counts per file: what `## [Unreleased]` holds, which is legitimately
	// zero the minute a release is cut, and what the whole file holds, which is
	// how the reader proves it can find at all.
	count := func(name string) (unreleased, total int) {
		path := filepath.Join("..", "..", name)
		source, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		inside := false
		for _, line := range strings.Split(string(source), "\n") {
			switch {
			case strings.HasPrefix(line, "## [Unreleased]"):
				inside = true
			case strings.HasPrefix(line, "## ["):
				inside = false
			case strings.HasPrefix(line, changelogEntry):
				total++
				if inside {
					unreleased++
				}
			}
		}
		return unreleased, total
	}

	en, enTotal := count("CHANGELOG.md")
	fr, frTotal := count("CHANGELOG.fr.md")

	// The reader proves it can find before it judges. A prefix that matched
	// nothing would report 0 == 0 and pass over two empty files — and asserting
	// on the Unreleased count alone would do the same on the day of a release,
	// which is the day this test was written and the day it would have been
	// most useless.
	if enTotal == 0 || frTotal == 0 {
		t.Fatalf("entries found: %d in CHANGELOG.md, %d in CHANGELOG.fr.md — this test measured nothing",
			enTotal, frTotal)
	}

	if en != fr {
		t.Errorf("CHANGELOG.md carries %d unreleased entries and CHANGELOG.fr.md carries %d: "+
			"an entry written in one file and not the other is a change a reader of one "+
			"language never learns about", en, fr)
	}
}
