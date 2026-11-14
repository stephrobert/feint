package main

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// The cases below are one per question figures.go asks, and that is the whole
// design constraint. A single obviously-bad world — an empty directory, say —
// fails every question at once, so a table built on it cannot tell them apart:
// neutralising any one guard would leave every case red for some other reason
// and the suite would pass for the wrong reason.
//
// So each case starts from `plantedWorld`, which is valid and whose three
// quantities are deliberately all different (2 specs, 4 mutations, 3 distinct
// files), and breaks exactly one thing. Every case asserts the reason, not only
// that something was reported, because one empty slice answers several
// questions here.

// plantedSpecs is the valid world: two specs, four mutations, three distinct
// files. No two of those numbers are equal, so a case that swapped two
// quantities would be caught rather than cancelling out.
var plantedSpecs = map[string]string{
	"first.json": `{"package":"./pkg/","mutations":[
		{"label":"a","file":"x.go","find":"a","replace":"b","test":"TestA"},
		{"label":"b","file":"y.go","find":"a","replace":"b","test":"TestB"},
		{"label":"c","file":"z.go","find":"a","replace":"b","test":"TestC"}]}`,
	"second.json": `{"package":"./pkg/","mutations":[
		{"label":"d","file":"x.go","find":"a","replace":"b","test":"TestD"}]}`,
}

// plantedSite is the sentence shape the real CONTRIBUTING.md carries, so the
// cases exercise the production pattern rather than a simpler cousin.
func plantedSite() figureSite {
	return figureSite{
		file:       "DOC.md",
		pattern:    regexp.MustCompile(`(\d+) specs name (\d+) distinct files in (\d+) mutations`),
		quantities: []string{"specs", "distinct files", "mutations"},
		argument:   "the planted argument",
	}
}

// plant writes a specs directory and a document tree, and answers the two paths
// specFigureProblems takes.
func plant(t *testing.T, specs map[string]string, docs map[string]string) (root, specsDir string) {
	t.Helper()
	root = t.TempDir()
	specsDir = filepath.Join(root, "specs")
	if err := os.MkdirAll(specsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range specs {
		if err := os.WriteFile(filepath.Join(specsDir, name), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	for name, body := range docs {
		path := filepath.Join(root, name)
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	return root, specsDir
}

func doc(sentence string) map[string]string {
	return map[string]string{
		"DOC.md": "Some prose first.\n\nIt reads them off the specs: " + sentence + ",\nand then says something else.\n",
	}
}

// the accepting half. A check that refuses everything passes every attack case
// above and stops the project: it would refuse every correct sentence anybody
// ever writes.
func TestARecountedFigureIsAccepted(t *testing.T) {
	root, specs := plant(t, plantedSpecs, doc("2 specs name 3 distinct files in 4 mutations"))
	if found := specFigureProblems(root, specs, []figureSite{plantedSite()}); len(found) > 0 {
		t.Errorf("a sentence stating the live count was refused: %v", found)
	}
}

// Wrapped at eighty columns, which is how every one of these sentences is
// actually written. Three of the four real sites reported "matches 0 time(s)"
// before flatten existed, with their figures correct.
func TestAWrappedSentenceIsRead(t *testing.T) {
	root, specs := plant(t, plantedSpecs, map[string]string{
		"DOC.md": "It reads them off the specs: 2 specs name 3 distinct files\nin 4 mutations, and a spec that names a file you touched is yours.\n",
	})
	if found := specFigureProblems(root, specs, []figureSite{plantedSite()}); len(found) > 0 {
		t.Errorf("a wrapped sentence stating the live count was refused: %v", found)
	}
}

// And the same wrapped inside a comment, where the line break carries a marker:
// two of the four real sites are a Go comment and a YAML comment.
func TestAWrappedCommentIsRead(t *testing.T) {
	root, specs := plant(t, plantedSpecs, map[string]string{
		"DOC.md": "# It reads them off the specs: 2 specs name 3 distinct\n# files in 4 mutations, and that is the whole mapping.\n",
	})
	if found := specFigureProblems(root, specs, []figureSite{plantedSite()}); len(found) > 0 {
		t.Errorf("a sentence wrapped inside a comment was refused: %v", found)
	}
}

func TestAPublishedSpecCountThatDriftedIsRefused(t *testing.T) {
	// Wrong only on the spec count: 5 is neither the mutation count nor the
	// distinct-file count, so no other question can be the one that fires.
	root, specs := plant(t, plantedSpecs, doc("5 specs name 3 distinct files in 4 mutations"))
	assertOneProblem(t, specFigureProblems(root, specs, []figureSite{plantedSite()}),
		"publishes 5 specs, the specs hold 2")
}

func TestAPublishedFileCountThatCountsAFileTwiceIsRefused(t *testing.T) {
	// 4 is the mutation count, and x.go is named twice: this is the figure a
	// census that forgot to deduplicate would produce, which is why it is the
	// value chosen here rather than any wrong number.
	root, specs := plant(t, plantedSpecs, doc("2 specs name 4 distinct files in 4 mutations"))
	assertOneProblem(t, specFigureProblems(root, specs, []figureSite{plantedSite()}),
		"publishes 4 distinct files, the specs hold 3")
}

func TestAPublishedMutationCountThatDriftedIsRefused(t *testing.T) {
	root, specs := plant(t, plantedSpecs, doc("2 specs name 3 distinct files in 7 mutations"))
	assertOneProblem(t, specFigureProblems(root, specs, []figureSite{plantedSite()}),
		"publishes 7 mutations, the specs hold 4")
}

// The case this check exists to survive. Every figure is right, the census is
// right, the file is right — and the sentence has been reworded, so there is no
// figure left to judge. Reporting nothing here is what makes a control of this
// shape die without anybody noticing.
func TestARewordedSentenceIsRefusedRatherThanUnjudged(t *testing.T) {
	root, specs := plant(t, plantedSpecs, map[string]string{
		"DOC.md": "It reads them off the specs, and there are a couple of hundred of them.\n",
	})
	assertOneProblem(t, specFigureProblems(root, specs, []figureSite{plantedSite()}),
		"matches 0 time(s), want exactly 1")
}

// Two correct copies, so every other question passes. A verdict read off
// whichever copy came first is a verdict about half the file.
func TestADuplicatedSentenceIsRefused(t *testing.T) {
	right := "2 specs name 3 distinct files in 4 mutations"
	root, specs := plant(t, plantedSpecs, map[string]string{
		"DOC.md": "First: " + right + ".\n\nAnd again later: " + right + ".\n",
	})
	assertOneProblem(t, specFigureProblems(root, specs, []figureSite{plantedSite()}),
		"matches 2 time(s), want exactly 1")
}

func TestAMissingDocumentIsRefusedRatherThanSkipped(t *testing.T) {
	root, specs := plant(t, plantedSpecs, nil)
	assertOneProblem(t, specFigureProblems(root, specs, []figureSite{plantedSite()}),
		"DOC.md: cannot be read")
}

// A published zero over an empty directory agrees with itself, which is exactly
// why the directory has to refuse before the comparison happens.
func TestACensusOverNoSpecIsRefusedEvenWhenTheFigureAgrees(t *testing.T) {
	root, specs := plant(t, nil, doc("0 specs name 0 distinct files in 0 mutations"))
	assertOneProblem(t, specFigureProblems(root, specs, []figureSite{plantedSite()}),
		"holds no spec: zero is the absence of a measurement")
}

// The reason asserted is the one only the ReadDir guard gives. The shared
// prefix "no figure was judged, because the specs were not counted" is also what
// the "holds no spec" guard produces, and a missing directory without the
// ReadDir guard is a loop over no entry, which that guard catches: asserting the
// prefix alone left this case green with the ReadDir guard removed.
func TestAnUnreadableSpecsDirectoryIsRefused(t *testing.T) {
	root, _ := plant(t, plantedSpecs, doc("2 specs name 3 distinct files in 4 mutations"))
	assertOneProblem(t, specFigureProblems(root, filepath.Join(root, "nowhere"), []figureSite{plantedSite()}),
		"cannot be read, so no count was taken")
}

// A file that is not a spec sits beside the specs and is not counted. The
// sentence states the count of the two planted specs, so this passes only if
// README.md is left out: read as a spec, it is refused as unreadable JSON.
// Every other question passes here, so this case is about the .json filter and
// nothing else.
func TestAFileThatIsNotASpecIsLeftOutOfTheCount(t *testing.T) {
	specs := map[string]string{"README.md": "# The specs\n\nOne JSON file per guard.\n"}
	for k, v := range plantedSpecs {
		specs[k] = v
	}
	root, dir := plant(t, specs, doc("2 specs name 3 distinct files in 4 mutations"))
	if found := specFigureProblems(root, dir, []figureSite{plantedSite()}); len(found) > 0 {
		t.Errorf("a README beside the specs changed the verdict on a right sentence: %v", found)
	}
}

// A spec that does not parse, beside two that do, with the document stating the
// count of the two that do. Skipping it would make this world green on a figure
// that is short by one spec — the exact false verdict this file is written
// against, and the reason the census errors where specs.go's reader continues.
func TestASpecThatDoesNotParseIsNamedRatherThanLeftOut(t *testing.T) {
	specs := map[string]string{"broken.json": "{\"mutations\": ["}
	for k, v := range plantedSpecs {
		specs[k] = v
	}
	root, dir := plant(t, specs, doc("2 specs name 3 distinct files in 4 mutations"))
	assertOneProblem(t, specFigureProblems(root, dir, []figureSite{plantedSite()}),
		"broken.json is not readable JSON")
}

func TestASpecDeclaringNoMutationIsNamed(t *testing.T) {
	specs := map[string]string{"empty.json": `{"package":"./pkg/","mutations":[]}`}
	for k, v := range plantedSpecs {
		specs[k] = v
	}
	root, dir := plant(t, specs, doc("2 specs name 3 distinct files in 4 mutations"))
	assertOneProblem(t, specFigureProblems(root, dir, []figureSite{plantedSite()}),
		"empty.json declares no mutation")
}

func TestAMutationNamingNoFileIsNamed(t *testing.T) {
	specs := map[string]string{
		"blank.json": `{"package":"./pkg/","mutations":[
			{"label":"e","file":"  ","find":"a","replace":"b","test":"TestE"}]}`,
	}
	for k, v := range plantedSpecs {
		specs[k] = v
	}
	root, dir := plant(t, specs, doc("2 specs name 3 distinct files in 4 mutations"))
	assertOneProblem(t, specFigureProblems(root, dir, []figureSite{plantedSite()}),
		"mutation 0 names no file")
}

// An unrecognised quantity must refuse, not pass. "spec count" is the plausible
// misspelling of a name this check does know, and a `default` answering zero or
// answering the spec count would make it read as a verdict about specs.
func TestAnUnrecognisedQuantityIsRefused(t *testing.T) {
	root, specs := plant(t, plantedSpecs, doc("2 specs name 3 distinct files in 4 mutations"))
	site := plantedSite()
	site.pattern = regexp.MustCompile(`(\d+) specs name`)
	site.quantities = []string{"spec count"}
	assertOneProblem(t, specFigureProblems(root, specs, []figureSite{site}),
		`"spec count" is not a quantity this check knows how to recount`)
}

func TestADeclarationWhoseGroupsDoNotMatchItsQuantitiesIsRefused(t *testing.T) {
	root, specs := plant(t, plantedSpecs, doc("2 specs name 3 distinct files in 4 mutations"))
	site := plantedSite()
	site.quantities = []string{"specs"}
	assertOneProblem(t, specFigureProblems(root, specs, []figureSite{site}),
		"the declaration is malformed, 3 capture group(s) for 1 declared quantity(ies)")
}

func TestACapturedFigureThatIsNotANumberIsRefused(t *testing.T) {
	root, specs := plant(t, plantedSpecs, map[string]string{
		"DOC.md": "It reads them off the specs: many specs name them.\n",
	})
	site := plantedSite()
	site.pattern = regexp.MustCompile(`(\w+) specs name`)
	site.quantities = []string{"specs"}
	assertOneProblem(t, specFigureProblems(root, specs, []figureSite{site}),
		`"many" is not a number`)
}

// A run over no site is a run that compared nothing, and this repository reads
// that as red on purpose elsewhere (`corpus --check`). It is reachable by one
// deletion from the real table.
func TestARunOverNoSiteIsNotAPass(t *testing.T) {
	root, specs := plant(t, plantedSpecs, doc("2 specs name 3 distinct files in 4 mutations"))
	assertOneProblem(t, specFigureProblems(root, specs, nil), "no site declared")
}

// The real verdict: every sentence in this repository that states the size of
// the specs is recounted against the specs.
func TestEveryPublishedSpecFigureMatchesTheLiveCount(t *testing.T) {
	repo := root(t)
	found := specFigureProblems(repo, filepath.Join(repo, "tools", "falsify", "specs"), publishedSpecFigures)
	for _, problem := range found {
		t.Errorf("%s", problem)
	}
}

// And the other half of the same question: the table still names every place
// that publishes one. A site quietly dropped from the declaration takes its
// sentence out of the gate without turning anything red, which is the failure
// mode of every check driven by a list.
func TestTheFigureCheckNamesEveryPlaceThatPublishesASpecCount(t *testing.T) {
	want := map[string]bool{
		"CONTRIBUTING.md":                                    false,
		"CONTRIBUTING.fr.md":                                 false,
		filepath.Join("tools", "testplan", "specs.go"):       false,
		filepath.Join(".github", "workflows", "falsify.yml"): false,
	}
	for _, site := range publishedSpecFigures {
		if _, declared := want[site.file]; !declared {
			t.Errorf("%s publishes a spec count and this test does not know about it", site.file)
			continue
		}
		want[site.file] = true
	}
	for file, seen := range want {
		if !seen {
			t.Errorf("%s publishes a spec count and no site declares it, so nothing recounts it", file)
		}
	}
}

func assertOneProblem(t *testing.T, found []string, reason string) {
	t.Helper()
	if len(found) == 0 {
		t.Fatalf("nothing was reported, want one problem saying %q", reason)
	}
	if len(found) != 1 {
		t.Fatalf("want exactly one problem saying %q, got %d: %v", reason, len(found), found)
	}
	if !strings.Contains(found[0], reason) {
		t.Fatalf("the problem reported does not say why:\n  got  %s\n  want it to contain %q", found[0], reason)
	}
}
