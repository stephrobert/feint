package cli

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
)

// No document states an axis percentage by hand (#406).
//
// What happened. `docs/proxy.md` said "Six evidence axes stood between 85 and
// 100 %", `docs/conformance.md` and both CHANGELOGs said the same in other
// words, and #390's table published `probed`, `contract` and `shape` at 100 %.
// Measured against the very artefact they described, with the command that
// exists to read it — `feint coverage --evidence coverage/evidence.json` — the
// three were **89 %, 89 % and 14 %**. One was wrong by a factor of six.
//
// Nobody lied, and that is the point: the figures came from a throwaway script
// that read each axis as a boolean, `if o.get(axis)`, when three of the seven
// are verdicts rather than booleans. `"unobserved"` is a non-empty string, so
// every operation whose shape had never been compared to a real cloud answer
// counted as one that had. That is rule 2 of the measurement-integrity skill —
// three outcomes, never two — met on this project's own headline numbers.
//
// Why a correction was not enough, and it is the argument docs.go makes twice
// already for the coverage tables and the contract policy table: a hand-written
// number about a measured quantity is a claim nothing compares to anything. #402
// shipped the mechanism — a generated block held by `feint docs --check` — and
// it held one block in docs/routes.md. Everywhere else, any page could still
// print an axis percentage no gate ever read.
//
// So: an axis percentage lives inside a generated block or it does not exist.
// Prose may say "measured per provider, see the table"; it may not carry the
// number. Counts are left alone deliberately — "35 of 370" is what a work queue
// is made of, and the defect measured here was a percentage.
//
// The check is crude on purpose. It does not recompute anything, because the
// failure was somebody typing a number rather than somebody computing a wrong
// one: a percentage sitting next to an axis name, outside a generated block, is
// refused whatever its value. Being crude is what makes it hold for a figure
// nobody has invented yet.
//
// TestAHandWrittenAxisPercentageIsRefused fails without it, and
// tools/falsify/specs/axis-figures.json puts the sentence back.

// axisFigureWindow is how close a percentage must sit to an axis name to count
// as a figure about that axis, in bytes of the stripped document.
//
// 120 is not arbitrary: it is one wrapped Markdown line plus a margin, which is
// the distance across which the ten real sentences in this repository stated
// theirs, and it leaves the one percentage here that is *not* about an axis
// alone — "Scaleway marks 11 % of its schemas required", forty characters from
// a link whose anchor happens to contain the word contract.
const axisFigureWindow = 120

var (
	// The seven axis names, as a document writes them: in backticks, which is
	// how every page here names an axis, and which keeps the ordinary English
	// words "shape", "contract" and "negative" from anchoring a percentage that
	// has nothing to do with the record.
	axisNamePattern = regexp.MustCompile("`(?:driven|probed|contract|dataplane|shape|behaviour|negative)`")
	// And the word itself, in both languages this repository publishes in.
	axisWordPattern = regexp.MustCompile(`(?i)\b(?:axis|axes|axe)\b`)
	// A percentage, however it is spaced or spelled.
	percentagePattern = regexp.MustCompile(`\d+(?:[.,]\d+)?\s*%`)
	// A generated block, whatever it is called: every one of them is a marker
	// pair, and a check that listed them by name would miss the next one.
	generatedBlockPattern = regexp.MustCompile(`(?s)<!--\s*[a-z-]+:start\s*-->.*?<!--\s*[a-z-]+:end\s*-->`)
	// A fenced code block. A sample of command output is not prose, and the
	// generated blocks print one.
	codeFencePattern = regexp.MustCompile("(?sm)^```.*?^```")
)

// axisFigureProblems reports every hand-written axis percentage under root, one
// line per occurrence, sorted so two runs read the same.
//
// A tree with no Markdown reports nothing rather than failing: `feint docs` must
// keep working for somebody who installed the binary and has no docs/ directory.
func axisFigureProblems(root string) []string {
	var found []string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil //nolint:nilerr // an unreadable corner of the tree is not this check's verdict
		}
		if d.IsDir() {
			// .git and .upstream are not this repository's prose, and .claude
			// holds the worktrees other agents work in — walking it would scan
			// whole copies of this tree and report the same sentence twice.
			switch d.Name() {
			case ".git", ".upstream", ".claude", ".terraform", "node_modules", "dist":
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".md") {
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // a documentation tree, walked by design
		if readErr != nil {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			rel = path
		}
		found = append(found, axisFiguresIn(rel, string(body))...)
		return nil
	})
	sort.Strings(found)
	return found
}

// axisFiguresIn is the reader, separated from the walk so a test can hand it a
// document rather than a tree.
func axisFiguresIn(name, body string) []string {
	stripped := blankOut(body, generatedBlockPattern)
	stripped = blankOut(stripped, codeFencePattern)

	anchors := append(axisNamePattern.FindAllStringIndex(stripped, -1),
		axisWordPattern.FindAllStringIndex(stripped, -1)...)

	var found []string
	for _, at := range percentagePattern.FindAllStringIndex(stripped, -1) {
		for _, anchor := range anchors {
			if abs(anchor[0]-at[0]) > axisFigureWindow {
				continue
			}
			found = append(found, fmt.Sprintf(
				"%s:%d states %q beside %q by hand; an axis percentage belongs in a generated "+
					"block or nowhere (#406) — say \"see the per-provider table in docs/routes.md\", "+
					"or quote a count instead",
				name, lineOf(stripped, at[0]),
				strings.TrimSpace(stripped[at[0]:at[1]]),
				strings.TrimSpace(stripped[anchor[0]:anchor[1]])))
			break
		}
	}
	return found
}

// blankOut replaces every match with spaces, keeping newlines so the offsets and
// the line numbers of everything after it stay true. Replacing the whole match
// with spaces was the first version and it reported docs/proxy.md:682 for a
// sentence on line 792.
func blankOut(s string, re *regexp.Regexp) string {
	return re.ReplaceAllStringFunc(s, func(m string) string {
		var b strings.Builder
		b.Grow(len(m))
		// Byte by byte, not rune by rune: a multi-byte character replaced by one
		// space shortens the document and moves every offset after it.
		for i := range len(m) {
			if m[i] == '\n' {
				b.WriteByte('\n')
				continue
			}
			b.WriteByte(' ')
		}
		return b.String()
	})
}

func lineOf(s string, offset int) int {
	return strings.Count(s[:offset], "\n") + 1
}

func abs(n int) int {
	if n < 0 {
		return -n
	}
	return n
}
