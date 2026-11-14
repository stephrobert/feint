package main

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// #937's search, run as a test (#951).
//
// publishedSpecFigures recounts every sentence that stated the size of the
// falsify specs when it was written, and its list came from one search run by
// hand on one day. A sentence written the next day stating "N specs" in the
// present tense was judged by nothing, and goes stale the way the five of #937
// did. This test runs the same search on every pull request, and every match
// has to be accounted for in one of two ways:
//
//   - it sits inside a sentence publishedSpecFigures recounts, or
//   - its whole line is in datedSpecFigureLines below: a figure that names the
//     day, the commit or the event it was counted at, or a planted example.
//
// The rule #937 wrote in prose separates the two by tense: present belongs to
// the recount, a date belongs to the record. That rule is NOT implemented
// here, and on purpose. A tense detector would be a parser of English and
// French that can misread a sentence, and a misread present sentence passes
// silently, which is the failure this test exists to end. Instead the
// judgement is made by a person, once per line, when the line is written: a
// new match anywhere fails with its file and line, and the author either adds
// the sentence to publishedSpecFigures or dates it and lists it here. Nothing
// is inferred; a line edited around its figure leaves this list and fails
// until it is classified again.
//
// What it does NOT see, said rather than implied: a size written in words
// ("twenty-three times"), or a noun the search does not name ("255
// falsifications", "1709 times"). The search is the frontier, and it is the
// one #937 used, with one correction: a number that ends a version or a
// decimal ("the 0.9 specs", the specs of release 0.9) is not a count, so the
// digits must not follow a digit or a dot. #937's search matched "9 specs"
// there, and the first version of this list excused it as a dated line, which
// it is not; TestASpecFigureNobodyClassifiedIsRefused holds both halves.
var specFigureSearch = regexp.MustCompile(`(?:^|[^0-9.])([0-9]+ (?:specs?|mutations|fichiers distincts|distinct files))\b`)

// specFigures returns the figures of one line, without the character the
// search reads before each to tell a count from a version.
func specFigures(line string) []string {
	var out []string
	for _, m := range specFigureSearch.FindAllStringSubmatch(line, -1) {
		out = append(out, m[1])
	}
	return out
}

// Paths the search skips, and why. A changelog entry is dated by its version,
// in both languages: #951 found CHANGELOG.fr.md searched while CHANGELOG.md was
// not, one rule written two ways.
var specFigureSearchSkips = []string{
	"tools/falsify/specs/",                 // the population itself
	"docs/audits.md",                       // a register of what each audit read, dated per row
	"CHANGELOG.md",                         // dated by its version
	"CHANGELOG.fr.md",                      // the same rule, in French
	"tools/testplan/figures_test.go",       // planted figures, counted against planted specs
	"tools/testplan/figure_search_test.go", // this file quotes the lines it lists
}

// datedSpecFigureLines are the matching lines that state a past count, each
// with its date, commit or event on the line or in its sentence. Keyed by
// file and by the line as written, trimmed.
var datedSpecFigureLines = map[string][]string{
	".github/workflows/falsify.yml": {
		"# margin on a suite that grew from 199 to 207 specs in the same week. A",
		"# carried 1271 mutations across 199 specs. That figure is dated on purpose:",
	},
	"CONTRIBUTING.md": {
		"(`tools/testplan/figures.go`, #937). The sentence said *128 specs name 186",
		"survivable; the fifth named four runs and 27 specs, every one of them green,",
	},
	"CONTRIBUTING.fr.md": {
		"(`tools/testplan/figures.go`, #937). La phrase disait *128 specs nomment 186",
		"est survivable ; la cinquième, il a nommé quatre exécutions et 27 specs, toutes",
	},
	"mise.toml": {
		"#     passes and exits 0. At ef7e03b (#942, 2026-11-14) that was 13 mutations",
		"#     in three pairs, 0.9 s; at 4a9c1b3, one merge later, 14 mutations in four",
		"# Measured on 2026-11-14 on Stéphane's station, warm cache, 251 specs and 1665",
	},
	"tools/ci/night_report_test.go": {
		"// the 0.16 batch added 23 specs and 263 mutations, and that night was the first",
	},
	"tools/ci/nightly_reporting_test.go": {
		"// suite grew to 1271 mutations across 199 specs. It was killed by its own",
		"// mutations across 199 specs, each a tree copy plus a build plus one test run,",
		`t.Errorf("falsify.yml allows %s minutes; the 199 specs counted on 2026-09-14 were clocked at ~29s each, "+`,
	},
	"tools/falsify/falsify.py": {
		"~123-minute replay on one station — 60 specs name ./internal/cli/ and each",
		"2026-11-14 on a copy of the 254 specs: one spec naming",
		"Measured on eight ./internal/cli/ specs, 54 mutations, every one still",
		"Measured on 2026-11-14, warm build cache, on the 1665 mutations committed",
		"Measured on 2026-11-14 on Stéphane's station, 251 specs and 1665 mutations,",
	},
	"tools/testplan/figures.go": {
		`//	CONTRIBUTING.md                 "128 specs name 186 distinct files"`,
		`//	CONTRIBUTING.fr.md              "128 specs nomment 186 fichiers distincts"`,
		`//	tools/testplan/specs.go         "128 specs name 186 distinct files today"`,
		"// file's own census: **253 specs, 295 distinct files, 1683 mutations**. The",
		`// corrected specs.go by hand on 2026-11-14 to "251 specs ... 295 distinct files`,
		`// ... 1665 mutations", and **one merge later it was wrong again** — 253, 295,`,
		`//	.github/workflows/falsify.yml        "it now carries 1271 mutations across 199 specs"`,
		`//	tools/ci/nightly_reporting_test.go   "1271 mutations", and "for 199 specs" in a failure message`,
		`//	mise.toml                            "the 13 mutations that name a SUBTEST ... Three pairs today"`,
		"// Stéphane's station, warm cache, 251 specs and 1665 mutations: 2.5 s to 2.6 s\",",
	},
	"tools/testplan/plan.go": {
		"// `fields`, `runtime`, `environment` and 27 specs, every one of them green,",
		`"on the paths a diff touched: on #521 it named four runs and 27 specs, all\n" +`,
	},
	"tools/testplan/plan_test.go": {
		"// survivable; the fifth (#521) named four runs and 27 specs, all of which went",
	},
	"tools/testplan/specs.go": {
		"// stated in its own pull request — *\"the 31 specs naming a touched file replayed",
		"// the mapping. It read \"128 specs name 186 distinct files\" for long enough to be",
	},
}

// unaccountedSpecFigures runs the search over the given files and returns
// every match that neither a recounted sentence nor the dated list accounts
// for, then every dated line that no longer exists, sorted.
func unaccountedSpecFigures(root string, files []string, sites []figureSite, dated map[string][]string) ([]string, int, error) {
	recounted := make(map[string][]string)
	for _, site := range sites {
		body, err := os.ReadFile(filepath.Join(root, site.file))
		if err != nil {
			return nil, 0, fmt.Errorf("%s: cannot be read, so its recounted sentence is unknown: %w", site.file, err)
		}
		for _, m := range site.pattern.FindAllString(flatten(string(body)), -1) {
			recounted[filepath.ToSlash(site.file)] = append(recounted[filepath.ToSlash(site.file)], m)
		}
	}
	listed := make(map[string]map[string]bool)
	for file, lines := range dated {
		listed[file] = make(map[string]bool)
		for _, line := range lines {
			listed[file][line] = false
		}
	}

	var problems []string
	matches := 0
	for _, file := range files {
		file = filepath.ToSlash(file)
		if skippedBySpecFigureSearch(file) {
			continue
		}
		body, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			if os.IsNotExist(err) {
				continue // listed by git, deleted in the work tree
			}
			return nil, 0, fmt.Errorf("%s: cannot be read, so its figures were not searched: %w", file, err)
		}
		if bytes.IndexByte(body, 0) >= 0 {
			continue // binary, as `git grep -I` skips it
		}
		for n, line := range strings.Split(string(body), "\n") {
			for _, m := range specFigures(line) {
				matches++
				if inRecountedSentence(recounted[file], m) {
					continue
				}
				trimmed := strings.TrimSpace(line)
				if _, ok := listed[file][trimmed]; ok {
					listed[file][trimmed] = true
					continue
				}
				problems = append(problems, fmt.Sprintf(
					"%s:%d: %q states a size of the falsify specs that nothing recounts. "+
						"Declare its sentence in publishedSpecFigures (figures.go), or, if it is dated, "+
						"add the line to datedSpecFigureLines (figure_search_test.go): %s",
					file, n+1, m, trimmed))
			}
		}
	}
	for file, lines := range listed {
		for line, seen := range lines {
			if !seen {
				problems = append(problems, fmt.Sprintf(
					"%s: the dated line %q is no longer there: drop it from datedSpecFigureLines, "+
						"or classify the line that replaced it", file, line))
			}
		}
	}
	sort.Strings(problems)
	return problems, matches, nil
}

func inRecountedSentence(sentences []string, match string) bool {
	for _, s := range sentences {
		if strings.Contains(s, match) {
			return true
		}
	}
	return false
}

func skippedBySpecFigureSearch(file string) bool {
	for _, skip := range specFigureSearchSkips {
		if file == skip || (strings.HasSuffix(skip, "/") && strings.HasPrefix(file, skip)) {
			return true
		}
	}
	return false
}

// TestEverySpecFigureIsRecountedOrDated holds #951 on the repository.
//
// Zero matches is a refusal, not a pass: the search finds the recounted
// sentences themselves, so a run that found nothing searched nothing.
func TestEverySpecFigureIsRecountedOrDated(t *testing.T) {
	problems, matches, err := unaccountedSpecFigures(root(t), tracked(t), publishedSpecFigures, datedSpecFigureLines)
	if err != nil {
		t.Fatal(err)
	}
	if matches == 0 {
		t.Fatal("the search matched nothing, not even the recounted sentences, so it searched nothing")
	}
	for _, p := range problems {
		t.Error(p)
	}
}

// One case per question the search asks, each on a planted tree wrong in that
// one way only, and the accepting half.
func TestASpecFigureNobodyClassifiedIsRefused(t *testing.T) {
	site := figureSite{
		file:       "DOC.md",
		pattern:    regexp.MustCompile(`(\d+) specs name (\d+) distinct files in (\d+) mutations`),
		quantities: []string{"specs", "distinct files", "mutations"},
	}
	recounted := "It reads them off the specs: 2 specs name 3 distinct files\nin 4 mutations, today.\n"
	dated := "On 2026-11-14 the replay read 9 specs.\n"
	present := "The directory holds 9 specs.\n"
	for _, tc := range []struct {
		name  string
		files map[string]string
		dated map[string][]string
		want  string
	}{
		{"a recounted sentence, wrapped, and a dated line it lists", map[string]string{"DOC.md": recounted, "LOG.md": dated},
			map[string][]string{"LOG.md": {"On 2026-11-14 the replay read 9 specs."}}, ""},
		{"a present-tense figure nobody classified", map[string]string{"DOC.md": recounted, "NEW.md": present},
			nil, `NEW.md:1: "9 specs" states a size of the falsify specs that nothing recounts`},
		{"the same figure outside the sentence the site recounts", map[string]string{"DOC.md": recounted + present},
			nil, `DOC.md:3: "9 specs"`},
		{"a dated line that is no longer there", map[string]string{"DOC.md": recounted},
			map[string][]string{"LOG.md": {"On 2026-11-14 the replay read 9 specs."}}, "is no longer there"},
		{"a skipped path is not searched", map[string]string{"DOC.md": recounted, "CHANGELOG.fr.md": present}, nil, ""},
		{"a version before the noun is not a count", map[string]string{"DOC.md": recounted, "NOTE.md": "The style the 0.9 specs carry.\n"}, nil, ""},
		{"a count after a version is still a count", map[string]string{"DOC.md": recounted, "NOTE.md": "Since 0.9, 9 specs.\n"},
			nil, `NOTE.md:1: "9 specs"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			var files []string
			for name, body := range tc.files {
				if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
				files = append(files, name)
			}
			sort.Strings(files)
			problems, matches, err := unaccountedSpecFigures(dir, files, []figureSite{site}, tc.dated)
			if err != nil {
				t.Fatal(err)
			}
			if matches == 0 {
				t.Fatal("the search matched nothing on a tree planted with figures")
			}
			got := strings.Join(problems, "\n")
			switch {
			case tc.want == "" && got != "":
				t.Errorf("refused a tree every figure of which is accounted for:\n%s", got)
			case tc.want != "" && !strings.Contains(got, tc.want):
				t.Errorf("want a problem saying %q, got:\n%s", tc.want, got)
			}
		})
	}
}
