// Command surface answers, for a release, whether the note says what changed.
//
// It is a thin front for internal/release, which holds the judgement and its
// tests. The recovery of the previous release's artefacts is a git operation
// and lives in tools/release/surface.sh, so the judgement stays testable
// without a repository — the same split tools/compat/classify was written
// under, and for the same reason: a classifier written inline in a shell script
// is a classifier nothing can falsify.
//
//	go run ./tools/release/surface --since v0.9.0 --old <dir> --new coverage \
//	  --changelog CHANGELOG.md --exemptions tools/release/unnamed.json
//
// Exit: 0 every change is named or excused, 1 the run could not measure, 2 the
// release note is incomplete.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/stephrobert/feint/internal/drift"
	"github.com/stephrobert/feint/internal/release"
)

const (
	exitOK         = 0
	exitError      = 1
	exitIncomplete = 2
)

func main() { os.Exit(run(os.Args[1:], os.Stdout, os.Stderr)) }

func run(args []string, stdout, stderr *os.File) int {
	fs := flag.NewFlagSet("surface", flag.ContinueOnError)
	fs.SetOutput(stderr)
	oldDir := fs.String("old", "", "directory holding the compared release's coverage artefacts")
	newDir := fs.String("new", "coverage", "directory holding this tree's coverage artefacts")
	changelogPath := fs.String("changelog", "CHANGELOG.md", "the changelog whose newest sections are read")
	since := fs.String("since", "", "the tag the comparison is against, as vX.Y.Z")
	exemptPath := fs.String("exemptions", "tools/release/unnamed.json", "the signed 'not worth naming' list")
	if err := fs.Parse(args); err != nil {
		return exitError
	}
	if *oldDir == "" || *since == "" {
		fmt.Fprintln(stderr, "surface: --old and --since are required; tools/release/surface.sh fills both in")
		return exitError
	}

	before, err := loadDir(*oldDir)
	if err != nil {
		fmt.Fprintf(stderr, "surface: %v\n", err)
		return exitError
	}
	after, err := loadDir(*newDir)
	if err != nil {
		fmt.Fprintf(stderr, "surface: %v\n", err)
		return exitError
	}
	// The subject asserted rather than assumed. A run pointed at a directory
	// with no artefact in it would otherwise find no change, name nothing and
	// exit 0 — a gate that measures its own absence, which this repository has
	// published once already.
	if len(after) == 0 {
		fmt.Fprintf(stderr, "surface: no *-coverage.json in %s: there is nothing to compare, "+
			"which is not the same answer as nothing changed\n", *newDir)
		return exitError
	}

	body, err := os.ReadFile(*changelogPath) //nolint:gosec // a path this repository owns
	if err != nil {
		fmt.Fprintf(stderr, "surface: %v\n", err)
		return exitError
	}
	section, err := release.Section(string(body), strings.TrimPrefix(*since, "v"))
	if err != nil {
		fmt.Fprintf(stderr, "surface: %s: %v\n", *changelogPath, err)
		return exitError
	}

	exemptions, err := loadExemptions(*exemptPath)
	if err != nil {
		fmt.Fprintf(stderr, "surface: %v\n", err)
		return exitError
	}

	var changes []release.Change
	for _, provider := range providers(before, after) {
		if _, ok := before[provider]; !ok {
			fmt.Fprintf(stdout, "note: %s carried no coverage artefact at %s, so everything it "+
				"serves is new here\n", provider, *since)
		}
		changes = append(changes, release.Compare(provider, before[provider], after[provider])...)
	}

	verdict := release.Audit(changes, section, exemptions)
	report(stdout, *since, *changelogPath, verdict)
	if verdict.OK() {
		return exitOK
	}
	refuse(stderr, *since, *changelogPath, *exemptPath, verdict)
	return exitIncomplete
}

func report(w *os.File, since, changelog string, v release.Verdict) {
	fmt.Fprintf(w, "release surface since %s, against the sections of %s above it\n", since, changelog)
	fmt.Fprintf(w, "  %d change(s) a release note must carry: %d named, %d excused, %d neither\n",
		len(v.Named)+len(v.Excused)+len(v.Unnamed), len(v.Named), len(v.Excused), len(v.Unnamed))
	fmt.Fprintf(w, "  %d change(s) it need not: arrivals already declined, and declined operations upstream dropped\n",
		len(v.Reported))
	for _, c := range v.Named {
		fmt.Fprintf(w, "  named    %-18s %s/%s\n", c.Class, c.Provider, c.Operation)
	}
	for _, c := range v.Excused {
		fmt.Fprintf(w, "  excused  %-18s %s/%s\n", c.Class, c.Provider, c.Operation)
	}
}

func refuse(w *os.File, since, changelog, exemptPath string, v release.Verdict) {
	if len(v.Unnamed) > 0 {
		fmt.Fprintf(w, "\n%d operation(s) changed hands since %s and no section above ## [%s] names them:\n",
			len(v.Unnamed), since, strings.TrimPrefix(since, "v"))
		for _, c := range v.Unnamed {
			fmt.Fprintf(w, "  %-18s %s %s (%s → %s)\n", c.Class, c.Provider, c.Operation, c.From, c.To)
		}
		fmt.Fprintf(w, "\nName them in %s — the release body is that text — or sign each one in %s\n"+
			"with the reason it is not worth naming. Paste-ready:\n\n", changelog, exemptPath)
		for _, c := range v.Unnamed {
			fmt.Fprintf(w, "  `%s`\n", c.Operation)
		}
	}
	for _, e := range v.Stale {
		fmt.Fprintf(w, "\n%s excuses %s, which changed nothing in this window: an exemption that\n"+
			"matches nothing is a gate that quietly stopped covering what it names. Retire it.\n",
			exemptPath, e.Operation)
	}
	for _, e := range v.Unreasoned {
		fmt.Fprintf(w, "\n%s exempts %s with no reason. \"Not worth naming\" is a decision somebody\n"+
			"signs, never a silence.\n", exemptPath, e.Operation)
	}
}

// loadDir reads every coverage artefact in a directory, keyed by provider.
//
// An absent directory is empty rather than an error: the compared tag may
// predate a provider's artefact, and the caller says so in a line of its own.
func loadDir(dir string) (map[string][]drift.Entry, error) {
	out := map[string][]drift.Entry{}
	matches, err := filepath.Glob(filepath.Join(dir, "*-coverage.json"))
	if err != nil {
		return nil, err
	}
	for _, path := range matches {
		file, err := os.Open(path) //nolint:gosec // a path the caller handed us
		if err != nil {
			return nil, err
		}
		artefact, err := drift.LoadCoverage(file)
		_ = file.Close()
		if err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
		out[artefact.Provider] = artefact.Entries
	}
	return out, nil
}

func providers(before, after map[string][]drift.Entry) []string {
	seen := map[string]bool{}
	for name := range before {
		seen[name] = true
	}
	for name := range after {
		seen[name] = true
	}
	names := make([]string, 0, len(seen))
	for name := range seen {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// loadExemptions reads the signed list. The file carries a `comment` array
// beside its entries, the way tools/compat/accepted.json does: the rule a
// reader needs travels with the list rather than in a document they would have
// to find.
func loadExemptions(path string) ([]release.Exemption, error) {
	body, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var file struct {
		Comment []string            `json:"comment"`
		Unnamed []release.Exemption `json:"unnamed"`
	}
	if err := json.Unmarshal(body, &file); err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return file.Unnamed, nil
}
