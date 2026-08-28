package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// docs/README.md sends a reader down one of four paths, and every one of them
// has to arrive (#591).

// An index is a second copy of the directory's shape, and a second copy is what
// this repository keeps paying for: the pages it names get renamed, merged and
// moved, and a link that stopped resolving reads exactly like one that never
// did. `docs/limits.md` alone has been split and re-headed enough times that
// TestEveryConfidenceRowCarriesItsProof exists for the same reason one storey
// down.
//
// So the index is held to the rule confidence.md is held to: every link
// resolves, and a fragment resolves to a heading that really produces it. The
// difference is the subject — that test reads the rows of one table, this one
// reads the whole page, because an index is nothing but links.
//
// What is deliberately *not* asserted is that the index names every file in
// `docs/`. It is a guide with four paths, and a check demanding completeness
// would turn it back into the listing GitHub already renders — which is the
// thing that guides nobody, and the reason the maintainer refused a restructure
// on the same day this landed.
func TestTheDocsIndexResolvesEveryPathItOffers(t *testing.T) {
	root := repoRoot(t)
	index := filepath.Join(root, "docs", "README.md")
	body, err := os.ReadFile(index)
	if err != nil {
		t.Fatalf("read the docs index: %v", err)
	}

	link := regexp.MustCompile(`\[[^\]]+\]\(([^)]+)\)`)
	targets := link.FindAllStringSubmatch(string(body), -1)
	// An index whose links vanished would otherwise pass by having nothing left
	// to resolve, which is the shape of every control that looks for an absence.
	if len(targets) < 8 {
		t.Fatalf("the docs index carries %d link(s): it exists to point a reader at a page, "+
			"and it cannot do that in that few", len(targets))
	}

	for _, m := range targets {
		target := m[1]
		if strings.HasPrefix(target, "http://") || strings.HasPrefix(target, "https://") {
			// An external link cannot be resolved offline, and a documentation
			// test that needs the network is a test that gets skipped.
			continue
		}
		file, fragment, _ := strings.Cut(target, "#")
		if file == "" {
			file = "README.md"
		}
		// Relative to docs/, which is where the index lives: a path may leave it
		// (../CONTRIBUTING.md) and must still be checked where it lands.
		path := filepath.Join(root, "docs", file)
		content, err := os.ReadFile(path) //nolint:gosec // a path this repository owns
		if err != nil {
			t.Errorf("the index offers %q and that file does not exist: a path that does not "+
				"arrive is worse than no index, because a reader trusts it", target)
			continue
		}
		if fragment == "" {
			continue
		}
		if !hasAnchor(string(content), fragment) {
			t.Errorf("the index offers %q and no heading in %s produces that anchor: "+
				"the reader lands on the page and not on the part of it they were sent to",
				target, file)
		}
	}
}
