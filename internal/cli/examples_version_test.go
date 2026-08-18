package cli

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// Every example and issue template names the released version, the same one
// the generated install blocks name.
//
// The generated pages cannot be a release behind, because `feint docs --check`
// exits 2 the day the CHANGELOG moves. The examples could, and were — in both
// directions at once on 2026-08-18: `examples/gitlab-ci` pulled v0.8.0 while
// `examples/README.md` and `examples/github-actions` told the reader to
// install 0.9.0, a release that did not exist, so the copy-paste this
// directory exists for downloaded a 404. The issue templates carried v0.1.0.
//
// The rule is the one latestReleased documents: the version a page names is
// the CHANGELOG's latest released heading, which the release workflow itself
// reads. Cutting a release moves the heading, which turns this red until the
// examples move with it — the same coupling the generated blocks already have,
// extended to the files nothing generates.
func TestEveryExampleNamesTheReleasedVersion(t *testing.T) {
	root := repoRoot(t)
	released := latestReleased(filepath.Join(root, changelogPath))
	if released == "" {
		t.Fatal("no released version in the CHANGELOG: the reader is broken, not the examples")
	}

	// The shapes a feint version takes in a hand-written file. Each pattern
	// captures the version and nothing else, so a Terraform or provider pin in
	// the same file cannot trip it.
	patterns := []*regexp.Regexp{
		regexp.MustCompile(`ghcr\.io/stephrobert/feint:v(\d+\.\d+\.\d+)`),
		regexp.MustCompile(`releases/download/v(\d+\.\d+\.\d+)`),
		// The setup-feint input. \b keeps `terraform_version:` out: an
		// underscore is a word character, so no boundary precedes this v.
		regexp.MustCompile(`\bversion:\s*(\d+\.\d+\.\d+)`),
		regexp.MustCompile(`feint v?(\d+\.\d+\.\d+)`), // prose and placeholders
	}

	var files []string
	for _, dir := range []string{"examples", filepath.Join(".github", "ISSUE_TEMPLATE")} {
		err := filepath.WalkDir(filepath.Join(root, dir), func(path string, d os.DirEntry, err error) error {
			if err != nil || d.IsDir() {
				return err
			}
			switch filepath.Ext(path) {
			case ".md", ".yml", ".yaml", ".tf":
				files = append(files, path)
			}
			return nil
		})
		if err != nil {
			t.Fatalf("walk %s: %v", dir, err)
		}
	}
	if len(files) < 5 {
		t.Fatalf("only %d files found: the walk is broken, not the examples", len(files))
	}

	seen := 0
	for _, path := range files {
		body, err := os.ReadFile(path) //nolint:gosec // paths from our own walk
		if err != nil {
			t.Fatal(err)
		}
		for _, pattern := range patterns {
			for _, m := range pattern.FindAllStringSubmatch(string(body), -1) {
				seen++
				if m[1] != released {
					rel, _ := filepath.Rel(root, path)
					t.Errorf("%s names feint %s where the released version is %s (%q)",
						rel, m[1], released, strings.TrimSpace(m[0]))
				}
			}
		}
	}
	if seen == 0 {
		t.Fatal("no version reference matched at all: the patterns are broken, not the examples")
	}
}
