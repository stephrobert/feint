package emulator_test

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// A comment citing a test names a test that exists.
//
// This repository's flagship defect is a comment standing in for a control, and
// its most literal form is a fix whose comment cites the test that would fail
// without it — when that test was never written. Three consecutive audits found
// one: two in the commit that invoked the rule while breaking it, and a third
// that survived the commit claiming to have fixed both.
//
// The rule was written down each time and broken each time, which is the
// argument for a check instead: a citation is now falsifiable by construction,
// because deleting a cited test fails this test.
//
// It does not check that the cited test is *about* the fix — nothing mechanical
// can. It checks the half that is mechanical, which is the half that failed.
func TestEveryCitedTestExists(t *testing.T) {
	root := repositoryRoot(t)

	// A citation is a Test-prefixed identifier inside a comment. The length
	// bound keeps `TestMain` and the httptest types out; the word boundary
	// keeps `newTestServer` out.
	cited := regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9]{7,}\b`)
	declared := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9]+)\(`)

	tests := map[string]bool{}
	citations := map[string][]string{} // test name -> files citing it

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// The vendored SDKs are read by the drift scan, not written here.
			if name := entry.Name(); name == ".git" || name == ".upstream" || name == "node_modules" {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(path, ".go") {
			return nil
		}
		body, readErr := os.ReadFile(path) //nolint:gosec // paths come from the walk
		if readErr != nil {
			return readErr
		}
		for _, match := range declared.FindAllStringSubmatch(string(body), -1) {
			tests[match[1]] = true
		}
		for _, line := range strings.Split(string(body), "\n") {
			comment := strings.Index(line, "//")
			if comment < 0 {
				continue
			}
			for _, name := range cited.FindAllString(line[comment:], -1) {
				rel, _ := filepath.Rel(root, path)
				citations[name] = append(citations[name], rel)
			}
		}
		return nil
	})
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	if len(citations) == 0 {
		t.Fatal("no citation found anywhere, which means this test measures nothing")
	}

	for name, files := range citations {
		if !tests[name] {
			t.Errorf("%s is cited in %s and does not exist: a comment that cannot fail is not a control",
				name, strings.Join(files, ", "))
		}
	}
}

// repositoryRoot walks up from the test's own directory to the module root,
// rather than hardcoding "../../..": a package that moves would otherwise make
// this test silently scan the wrong tree and pass.
func repositoryRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("working directory: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("no go.mod above the test's directory")
		}
		dir = parent
	}
}
