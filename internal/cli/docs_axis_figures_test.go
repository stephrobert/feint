package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// #406. A check whose success is "nothing was found" is indistinguishable from
// a check that looked nowhere, so the first test plants the three sentences
// this repository actually published and requires each of them to be named,
// and the second plants one in a tree and requires the walk to reach it.
// The third holds the documentation itself.

func TestAHandWrittenAxisPercentageIsRefused(t *testing.T) {
	for _, tc := range []struct {
		name     string
		body     string
		refused  bool
		mentions string
	}{
		{
			name: "the sentence docs/proxy.md published",
			body: "A refusal is the cheapest recording there is, and for a long time this\n" +
				"project had none. Six evidence axes stood between 85 and 100 % and\n" +
				"`negative` stood at 35 of 370.\n",
			refused: true, mentions: "100 %",
		},
		{
			name:    "the sentence docs/conformance.md published",
			body:    "It exists because six of the seven axes above stood over 85% while\n`negative` stood at 34 of 357.\n",
			refused: true, mentions: "85%",
		},
		{
			name:    "the sentence docs/roadmap.md published",
			body:    "- **#407** — `shape` is the weakest axis at 14 %, and 292 of its 318 zeros\n  are `unrecorded`.\n",
			refused: true, mentions: "14 %",
		},
		{
			// The one form that is allowed, and the reason the check is about
			// percentages: a count is what a work queue is made of, and it says
			// its own population out loud.
			name:    "a count beside an axis is not a percentage",
			body:    "`negative` stood at 35 of 370, and 138 operations gained it.\n",
			refused: false,
		},
		{
			// The generated table prints seven percentages per row and must not
			// report itself. This is the case that would make the check useless
			// if it were wrong in the other direction.
			name: "a percentage inside a generated block",
			body: "Before.\n\n<!-- axes:start -->\n| Cloud | `shape` |\n|---|---|\n| All | 14 % |\n<!-- axes:end -->\n\nAfter.\n",

			refused: false,
		},
		{
			name:    "a percentage inside a fenced code block",
			body:    "Reproduce it:\n\n```text\nall 370 shape 52 14%\n```\n\nDone.\n",
			refused: false,
		},
		{
			// Measured on docs/conformance.md: a percentage about required
			// schemas, forty characters from a link whose anchor contains the
			// word "contract". Flagging it would teach that the check cries
			// wolf, which is how a gate stops being read.
			name: "a percentage that is not about an axis",
			body: "It can only catch an omitted field where the provider marked it\n" +
				"`required` — Scaleway does on 11% of its schemas.\n" +
				"[limits.md](limits.md#what-a-green-contract-run-does-and-does-not-prove)\n" +
				"carries the measured detail.\n",
			refused: false,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			found := axisFiguresIn("docs/example.md", tc.body)
			switch {
			case tc.refused && len(found) == 0:
				t.Fatalf("this sentence was published and the check does not name it:\n%s", tc.body)
			case !tc.refused && len(found) > 0:
				t.Fatalf("nothing here states an axis percentage and the check names %v", found)
			}
			if tc.refused && !strings.Contains(found[0], tc.mentions) {
				t.Errorf("the report must quote the figure %q, and says: %s", tc.mentions, found[0])
			}
		})
	}
}

func TestTheAxisFigureScanReadsTheWholeDocumentationTree(t *testing.T) {
	root := t.TempDir()
	for _, dir := range []string{"docs", filepath.Join("corpus"), filepath.Join(".claude", "worktrees", "other")} {
		if err := os.MkdirAll(filepath.Join(root, dir), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	write := func(rel, body string) {
		t.Helper()
		if err := os.WriteFile(filepath.Join(root, rel), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	const sentence = "`shape` is the weakest axis at 14 %.\n"
	write("README.md", "Nothing measured here.\n")
	write(filepath.Join("docs", "roadmap.md"), sentence)
	write(filepath.Join("corpus", "README.md"), sentence)
	// A neighbouring worktree is a whole copy of this repository. Reported, it
	// would double every line and name a file the operator cannot edit from here.
	write(filepath.Join(".claude", "worktrees", "other", "roadmap.md"), sentence)

	found := axisFigureProblems(root)
	if len(found) != 2 {
		t.Fatalf("two documents state it and the scan reports %d: %v", len(found), found)
	}
	if !strings.HasPrefix(found[0], "corpus/README.md:1") || !strings.HasPrefix(found[1], "docs/roadmap.md:1") {
		t.Errorf("the report must name the file and the line, sorted; it says %v", found)
	}
}

// The line number must survive a generated block, and it did not at first: the
// first version replaced a whole block with spaces, newlines included, so the
// sentence really on line 792 of docs/proxy.md was reported as line 682. A
// report that names the wrong line sends the reader to the wrong sentence, and
// they conclude the check is broken rather than the page.
func TestTheReportedLineSurvivesAGeneratedBlock(t *testing.T) {
	body := "one\n<!-- axes:start -->\ntwo\nthree\nfour\n<!-- axes:end -->\nsix\n" +
		"`shape` is the weakest axis at 14 %.\n"
	found := axisFiguresIn("docs/example.md", body)
	if len(found) != 1 {
		t.Fatalf("one sentence states it and the check reports %d: %v", len(found), found)
	}
	if !strings.HasPrefix(found[0], "docs/example.md:8 ") {
		t.Errorf("the sentence is on line 8 and the report says: %s", found[0])
	}
}

// The documentation itself, held to the rule. This is the assertion that fails
// the day somebody writes the next one, and `feint docs --check` carries the
// same call so a pull request fails on it too.
func TestNoDocumentInThisRepositoryStatesAnAxisPercentage(t *testing.T) {
	if found := axisFigureProblems(repoRoot(t)); len(found) > 0 {
		t.Errorf("an axis percentage belongs in a generated block or nowhere (#406):\n%s",
			strings.Join(found, "\n"))
	}
}
