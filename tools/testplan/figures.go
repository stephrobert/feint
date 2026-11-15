package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
)

// No document and no comment states the size of tools/falsify/specs by hand
// (#937).
//
// What happened. Four places argued from the same two numbers, and all four were
// remembered rather than recounted:
//
//	CONTRIBUTING.md                 "128 specs name 186 distinct files"
//	CONTRIBUTING.fr.md              "128 specs nomment 186 fichiers distincts"
//	tools/testplan/specs.go         "128 specs name 186 distinct files today"
//	.github/workflows/falsify.yml   "once per mutation, twenty-three times"
//
// Recounted on 2026-11-14 at 4a9c1b3, by
// `python3 -I tools/falsify/falsify.py --lint tools/falsify/specs` and by this
// file's own census: **253 specs, 295 distinct files, 1683 mutations**. The
// spec count had doubled, the mutation count was wrong by a factor of 73, and
// nothing said a word. The one in specs.go stings most: its number is the whole
// argument for why the specs-to-files mapping is generated instead of
// hand-maintained, and the file whose point is that a mapping must not be
// remembered carried a remembered number.
//
// Worse, and this is the measurement that settles the design question: #942
// corrected specs.go by hand on 2026-11-14 to "251 specs ... 295 distinct files
// ... 1665 mutations", and **one merge later it was wrong again** — 253, 295,
// 1683. A hand correction to a figure about a growing population has the
// lifetime of the next pull request. So the figures are recounted here, against
// the specs themselves, and a sentence that has drifted fails
// `mise run check` instead of ageing quietly.
//
// What makes it red, and each is a separate case in figures_test.go:
//
//   - a published figure that is not the live count;
//   - the sentence carrying a figure reworded, moved or duplicated, so that
//     nothing was judged — that is a refusal here, never a pass;
//   - a specs directory that cannot be read, holds no spec, holds a spec that
//     parses but declares no mutation, or a mutation naming no file;
//   - a site declared with a quantity this file cannot recount, or with a
//     capture group count that does not match its declaration.
//
// TestEveryPublishedSpecFigureMatchesTheLiveCount and the cases around it fail
// without this file, and tools/falsify/specs/published-spec-figures.json puts
// each half of it back.

// census is the live size of tools/falsify/specs: the three quantities the four
// sentences above argue from.
type census struct {
	specs     int
	mutations int
	files     int
}

// quantity resolves the name a site declares.
//
// An unrecognised name refuses rather than answering. Returning zero, or
// returning the spec count for anything unknown, would make a typo in a
// declaration read as a verdict about the thing the typo meant — and a `default`
// that accepts every spelling it does not recognise is a defect this repository
// has already paid for.
func (c census) quantity(name string) (int, bool) {
	switch name {
	case "specs":
		return c.specs, true
	case "mutations":
		return c.mutations, true
	case "distinct files":
		return c.files, true
	default:
		return 0, false
	}
}

// censusOfSpecs recounts the specs, their mutations, and the distinct files
// those mutations name.
//
// Three outcomes, never two: a count, or a refusal naming what could not be
// read. There is no third path where an unreadable corner of the directory is
// quietly left out of the total, because a total short by one spec is a wrong
// number that looks exactly like a right one. `falsifications` in specs.go
// skips a spec it cannot parse on purpose — it is answering "which replays has
// this diff earned", and a missing entry there costs a replay. This function
// answers "how many are there", where a missing entry corrupts the answer.
func censusOfSpecs(dir string) (census, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return census{}, fmt.Errorf("%s cannot be read, so no count was taken: %w", dir, err)
	}

	var c census
	files := make(map[string]bool)
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		body, readErr := os.ReadFile(filepath.Join(dir, entry.Name()))
		if readErr != nil {
			return census{}, fmt.Errorf("%s cannot be read, so any count would be short by at least one spec: %w", entry.Name(), readErr)
		}
		var spec specFile
		if err := json.Unmarshal(body, &spec); err != nil {
			return census{}, fmt.Errorf("%s is not readable JSON, so any count would be short by at least one spec: %w", entry.Name(), err)
		}
		if len(spec.Mutations) == 0 {
			return census{}, fmt.Errorf("%s declares no mutation: counting it would put a spec that falsifies nothing into a figure used to argue the specs are large", entry.Name())
		}
		c.specs++
		for i, m := range spec.Mutations {
			if strings.TrimSpace(m.File) == "" {
				return census{}, fmt.Errorf("%s mutation %d names no file, so the distinct-file count cannot be taken", entry.Name(), i)
			}
			c.mutations++
			files[m.File] = true
		}
	}
	c.files = len(files)

	// An empty directory is not a measurement of zero specs. `go test -run
	// <absent>` exits 0 over an empty set of tests and this repository has paid
	// for reading that zero as success; a census over no spec is the same
	// verdict drawn from the same absence.
	if c.specs == 0 {
		return census{}, fmt.Errorf("%s holds no spec: zero is the absence of a measurement, not a measurement of nothing", dir)
	}
	return c, nil
}

// flatten turns a wrapped sentence into one line, so a pattern can be written
// the way the sentence reads.
//
// Not a convenience. All four sentences are wrapped at eighty columns and two of
// them live inside a comment, so a pattern matched against raw bytes matched
// none of them: measured on 2026-11-14, the first run of
// TestEveryPublishedSpecFigureMatchesTheLiveCount reported "matches 0 time(s)"
// for three of the four sites while their figures were correct. A leading `#`,
// `//` or `*` goes with the line break for the same reason — in a comment, the
// marker is part of the wrapping, not part of the sentence.
//
// TestAWrappedSentenceIsRead and TestAWrappedCommentIsRead fail without it, and
// they fail the way that matters: the figure is right and the check reports that
// it judged nothing.
func flatten(body string) string {
	lines := strings.Split(body, "\n")
	for i, line := range lines {
		trimmed := strings.TrimLeft(line, " \t")
		for _, marker := range []string{"//", "#", "*"} {
			if strings.HasPrefix(trimmed, marker) {
				trimmed = strings.TrimPrefix(trimmed, marker)
				break
			}
		}
		lines[i] = trimmed
	}
	return strings.Join(strings.Fields(strings.Join(lines, " ")), " ")
}

// figureSite is one published sentence and the quantities it states, in the
// order its pattern captures them.
type figureSite struct {
	// file is relative to the repository root, so a failure names the place a
	// reader has to edit.
	file string
	// pattern must match exactly once in that file, with one capture group per
	// declared quantity. Matching nothing is a refusal: it means the sentence
	// was reworded and the check went blind, which is how a control of this
	// shape dies silently.
	pattern *regexp.Regexp
	// quantities names what each capture group states, in order.
	quantities []string
	// argument is what the sentence uses the figure for, printed with a
	// failure: the point of the gate is that the argument stays true, not that
	// the digits match.
	argument string
}

// publishedSpecFigures is every sentence that stated the size of the
// falsification specs as a fact about today when this list was last searched
// for, and nothing finds the next one.
//
// The search, on 2026-11-14 at 4a9c1b3:
//
//	git grep -nIE '[0-9]+ (specs?|mutations|fichiers distincts|distinct files)\b' \
//	  -- . ':!tools/falsify/specs' ':!docs/audits.md' ':!CHANGELOG.md'
//
// It found these four in the present tense, and four more that were present
// tense too and were dated instead of listed (#937), because each one is the
// population of a timing taken on that day and recounting the population alone
// would pair a live count with a stale cost:
//
//	.github/workflows/falsify.yml        "it now carries 1271 mutations across 199 specs"
//	tools/ci/nightly_reporting_test.go   "1271 mutations", and "for 199 specs" in a failure message
//	mise.toml                            "the 13 mutations that name a SUBTEST ... Three pairs today"
//	tools/falsify/falsify.py             "once per spec, 251 of them"
//
// The SUBTEST count is also a quantity census does not take: recounting it here
// would mean a second definition of "names a subtest" beside falsify.py's
// names_a_subtest, which is one more thing to drift.
//
// Not in this list either, and deliberately: a figure that carries its date and
// the station it was taken on. `mise.toml` records "measured on 2026-11-14 on
// Stéphane's station, warm cache, 251 specs and 1665 mutations: 2.5 s to 2.6 s",
// and `docs/audits.md` records what an audit read. Those name the population a
// past measurement was drawn from; recomputing them would destroy the only
// thing they say. The rule that separates the two is tense: a sentence in the
// present belongs here, a sentence with a date belongs to the record.
//
// A site that was never added is no longer judged by nothing (#951): the
// search above runs as TestEverySpecFigureIsRecountedOrDated
// (figure_search_test.go), and a match that is neither inside a sentence of
// this list nor a line declared dated there fails with its file and line. The
// tense is judged by whoever writes the line, not inferred, and the test says
// why. TestTheFigureCheckNamesEveryPlaceThatPublishesASpecCount still holds
// this list against a second one, which catches a site dropped from here.
var publishedSpecFigures = []figureSite{
	{
		file: "CONTRIBUTING.md",
		pattern: regexp.MustCompile(
			`(\d+) specs name (\d+) distinct files in (\d+) mutations`),
		quantities: []string{"specs", "distinct files", "mutations"},
		argument:   "the argument for reading the mapping off the specs instead of remembering it",
	},
	{
		file: "CONTRIBUTING.fr.md",
		pattern: regexp.MustCompile(
			`(\d+) specs nomment (\d+) fichiers distincts en (\d+) mutations`),
		quantities: []string{"specs", "distinct files", "mutations"},
		argument:   "la même phrase en français, qui doit porter le même chiffre",
	},
	{
		file: filepath.Join("tools", "testplan", "specs.go"),
		pattern: regexp.MustCompile(
			`(\d+) specs name (\d+) distinct files today, in (\d+) mutations`),
		quantities: []string{"specs", "distinct files", "mutations"},
		argument:   "why this very file generates the mapping rather than holding it by hand",
	},
	{
		file: filepath.Join(".github", "workflows", "falsify.yml"),
		pattern: regexp.MustCompile(
			`copies the tree once per spec \((\d+) copies\) and runs ` +
				"`go test`" + ` once per mutation, (\d+) times`),
		quantities: []string{"specs", "mutations"},
		argument:   "why the replay is nightly rather than on every pull request",
	},
}

// specFigureProblems reports every published figure that the specs no longer
// support, one line each, sorted so two runs read the same.
//
// The empty slice means every declared sentence was found exactly once and every
// figure in it matched a recount. It never means "nothing was looked at": a
// census that could not be taken, a file that could not be read and a sentence
// that could not be found all produce a line.
func specFigureProblems(root, specsDir string, sites []figureSite) []string {
	c, err := censusOfSpecs(specsDir)
	if err != nil {
		return []string{fmt.Sprintf("no figure was judged, because the specs were not counted: %v", err)}
	}
	if len(sites) == 0 {
		return []string{"no site declared: a run over no sentence is not a verdict about the published figures"}
	}

	var problems []string
	for _, site := range sites {
		problems = append(problems, site.problems(root, c)...)
	}
	sort.Strings(problems)
	return problems
}

// problems judges one site against one census.
func (s figureSite) problems(root string, c census) []string {
	if got := s.pattern.NumSubexp(); got != len(s.quantities) {
		return []string{fmt.Sprintf(
			"%s: the declaration is malformed, %d capture group(s) for %d declared quantity(ies), so nothing was judged",
			s.file, got, len(s.quantities))}
	}

	body, err := os.ReadFile(filepath.Join(root, s.file))
	if err != nil {
		return []string{fmt.Sprintf("%s: cannot be read, so its figure was not judged: %v", s.file, err)}
	}

	found := s.pattern.FindAllStringSubmatch(flatten(string(body)), -1)
	if len(found) != 1 {
		return []string{fmt.Sprintf(
			"%s: the sentence this check reads matches %d time(s), want exactly 1, so nothing was judged there "+
				"(%s). Pattern: %s",
			s.file, len(found), s.argument, s.pattern)}
	}

	var problems []string
	for i, name := range s.quantities {
		want, known := c.quantity(name)
		if !known {
			problems = append(problems, fmt.Sprintf(
				"%s: %q is not a quantity this check knows how to recount, so the figure beside it was not judged",
				s.file, name))
			continue
		}
		got, convErr := strconv.Atoi(found[0][i+1])
		if convErr != nil {
			problems = append(problems, fmt.Sprintf(
				"%s: %q is not a number, so the %s it states was not judged",
				s.file, found[0][i+1], name))
			continue
		}
		if got != want {
			problems = append(problems, fmt.Sprintf(
				"%s: publishes %d %s, the specs hold %d — %s. Recount: mise run falsify:lint",
				s.file, got, name, want, s.argument))
		}
	}
	return problems
}
