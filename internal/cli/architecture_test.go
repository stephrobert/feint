package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The architecture page's tree names every package that actually exists.
//
// docs/architecture.md opens with "the page to read before changing anything
// under internal/", and its tree is the map a newcomer trusts. On 2026-08-18
// six packages under internal/ and two under internal/core/ were absent from
// it — compat, proxy, shape, trace, transcript, upstream, serialise, sshkey —
// because the tree was written once and nothing compared it to the directory
// it describes. A map that omits a package reads as that package not existing,
// which for internal/compat meant the consumer-versioning gate (#170) was
// invisible on the one page that claims to show the shape.
//
// The comparison is one-directional on purpose: every real package must be
// named, but the page may name paths that are not packages (coverage/,
// contracts/, tools/conformance/<name>/), because the tree legitimately shows
// artefact directories too. Provider packs are covered by their placeholder,
// `internal/providers/<name>/`: listing each pack would make adding a fourth
// one a documentation chore this test exists to keep honest, not to create.
func TestTheArchitecturePageNamesEveryPackage(t *testing.T) {
	root := repoRoot(t)
	body, err := os.ReadFile(filepath.Join(root, "docs", "architecture.md"))
	if err != nil {
		t.Fatalf("read the architecture page: %v", err)
	}
	page := string(body)

	required := packageDirs(t, filepath.Join(root, "internal"), "internal")
	if len(required) < 8 {
		t.Fatalf("only %d packages found under internal/: the scan is broken, "+
			"not the page, and would otherwise pass while measuring nothing", len(required))
	}

	for _, dir := range required {
		if strings.HasPrefix(dir, "internal/providers/") {
			// The tree writes the packs as a placeholder, and that is right:
			// one line per pack would rot the day a pack is added.
			if !strings.Contains(page, "internal/providers/") {
				t.Errorf("the page never names internal/providers/, so no pack is on the map")
			}
			continue
		}
		if !strings.Contains(page, dir+"/") {
			t.Errorf("docs/architecture.md never names %s/ — a package the map "+
				"omits reads as a package that does not exist", dir)
		}
	}
}

// packageDirs lists the directories holding Go files directly under dir, one
// level of nesting deep for internal/core and internal/providers, which is the
// granularity the page's tree uses.
func packageDirs(t *testing.T, dir, rel string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		child := filepath.Join(dir, e.Name())
		childRel := rel + "/" + e.Name()
		if hasGoFiles(t, child) {
			out = append(out, childRel)
		}
		// One level down for the two grouping directories the tree expands.
		if e.Name() == "core" || e.Name() == "providers" {
			sub, err := os.ReadDir(child)
			if err != nil {
				t.Fatalf("read %s: %v", child, err)
			}
			for _, s := range sub {
				if s.IsDir() && hasGoFiles(t, filepath.Join(child, s.Name())) {
					out = append(out, childRel+"/"+s.Name())
				}
			}
		}
	}
	return out
}

func hasGoFiles(t *testing.T, dir string) bool {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".go") {
			return true
		}
	}
	return false
}
