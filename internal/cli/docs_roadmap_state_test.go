package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The roadmap may say what LANDED. It may not say what is still open.
//
// #403 measured three claims on `docs/roadmap.md` that described a version of
// this project that shipped two releases ago: *"#72 done; #73, #74 remain"*
// with all three closed, *"Fault injection — #26 / Already open"* with #26
// landed in 0.11.0, and a 1.4 row listing #26 as future work. A public roadmap
// that undersells the product is worse than none — the reader sizing this
// project against their needs is reading a page that is wrong, and will not
// trust the pages that are right.
//
// Correcting the three sentences would leave the class. The class is that the
// page RESTATES issue state, so it has to be re-read every time an issue closes
// and nothing re-reads it. `docs/limits.md` has a tripwire for exactly this
// (tools/docs/limits-acks.py, #558), and that one needs GitHub to answer; this
// one does not, because the rule it enforces is about the shape of the sentence
// rather than about the state of the issue:
//
//	"landed", "shipped" — monotonic. Once true, true for ever. Allowed.
//	"remain", "still open", "not yet built" — rots the day the issue closes.
//
// So the roadmap states direction and history, and points at the issues and
// their milestones for what is still open. Those are live; this page is not.

// openStateClaims are the phrasings that assert an issue is still open. Each is
// paired with the sentence #403 measured, so a reader adding a phrasing knows
// what the list is for.
var openStateClaims = []*regexp.Regexp{
	// "#72 done; #73, #74 remain"
	regexp.MustCompile(`#\d+[^.\n]{0,40}\bremains?\b`),
	regexp.MustCompile(`\bremains?\b[^.\n]{0,40}#\d+`),
	// "Fault injection — #26 / Already open, and it stays third"
	regexp.MustCompile(`(?i)\balready open\b`),
	regexp.MustCompile(`(?i)\bstill open\b`),
	regexp.MustCompile(`(?i)\bnot (?:yet )?(?:built|shipped|landed|implemented)\b`),

	// The French page, because a check that reads two files and only understands
	// one of them measures one file while claiming two — which is this
	// repository's own favourite defect, in a test about that defect.
	regexp.MustCompile(`#\d+[^.\n]{0,40}\brestent?\b`),
	regexp.MustCompile(`\brestent?\b[^.\n]{0,40}#\d+`),
	regexp.MustCompile(`(?i)\bd[ée]j[àa] ouverte?s?\b`),
	regexp.MustCompile(`(?i)\btoujours ouverte?s?\b`),
	regexp.MustCompile(`(?i)\bpas encore (?:construit|livr[ée]|impl[ée]ment[ée])`),

	// "| **SW-5** | 6 | `lb/v1` ZonedAPI | open (#17) |" — a whole table of
	// them, and the first version of this test walked past every row. Seventeen
	// batches, five of them declared open, sixteen issues measured CLOSED.
	// The check found the prose and missed the table, which is the difference
	// between a rule and the shape a reader happened to think of.
	regexp.MustCompile(`(?i)\|\s*(?:open|ouverte?s?)\s*\(#\d+\)`),
}

func TestTheRoadmapDoesNotRestateIssueState(t *testing.T) {
	pages := []string{
		filepath.Join("..", "..", "docs", "roadmap.md"),
		filepath.Join("..", "..", "docs", "roadmap.fr.md"),
	}

	found := 0
	for _, page := range pages {
		source, err := os.ReadFile(page)
		if err != nil {
			t.Fatalf("read %s: %v", page, err)
		}
		found++
		for number, line := range strings.Split(string(source), "\n") {
			// Fenced quotations of what the page USED to say are kept on
			// purpose — the corrections cite the sentence they replace — and a
			// blockquote is not the page speaking.
			if strings.HasPrefix(strings.TrimSpace(line), ">") {
				continue
			}
			for _, claim := range openStateClaims {
				if claim.MatchString(line) {
					t.Errorf("%s:%d asserts an issue's state, which rots the day it closes (#403):\n  %s",
						filepath.Base(page), number+1, strings.TrimSpace(line))
				}
			}
		}
	}

	// The reader proves it can find before it judges: a path that resolved
	// nowhere would pass this test however wrong the page became.
	if found != len(pages) {
		t.Fatalf("read %d of %d roadmap pages, so this test measured less than it claims", found, len(pages))
	}
}
