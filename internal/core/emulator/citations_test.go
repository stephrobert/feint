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

	// A citation is a Test-prefixed identifier inside a comment. The word
	// boundary keeps `newTestServer` out; the length bound keeps `TestMain`
	// out, and is deliberately short — five characters after the prefix — so a
	// briefly named test is still checked. An audit found the first version
	// required twelve, which made short citations invisible.
	cited := regexp.MustCompile(`\bTest[A-Z][A-Za-z0-9]{4,}\b`)
	declared := regexp.MustCompile(`(?m)^func (Test[A-Za-z0-9]+)\(`)

	// Indexed by directory, not flat across the module: an audit renamed a
	// cited test in one package, dropped a stub of the same name in another,
	// and this test passed — a homonym elsewhere satisfied a citation that
	// pointed at nothing local. A citation is checked against the package that
	// carries it, then against the module, because a comment in the core may
	// legitimately name a test that proves the point in a pack.
	homes := map[string][]string{} // test name -> the directories declaring it
	byDir := map[string]map[string]bool{}
	citations := map[string][]citation{} // "dir\x00name" -> where it is cited

	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.IsDir() {
			// The vendored SDKs are read by the drift scan, not written here.
			//
			// Worktrees are skipped for a different reason, learned the hard way:
			// a checkout under .claude/worktrees is another branch's source, and
			// walking it made this tree fail on a citation an agent had written
			// minutes earlier in a branch nobody had merged. A test that reports
			// on code outside its own checkout measures the wrong tree.
			switch entry.Name() {
			case ".git", ".upstream", "node_modules", "worktrees":
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
		dir := filepath.Dir(path)
		for _, match := range declared.FindAllStringSubmatch(string(body), -1) {
			homes[match[1]] = append(homes[match[1]], dir)
			if byDir[dir] == nil {
				byDir[dir] = map[string]bool{}
			}
			byDir[dir][match[1]] = true
		}
		// Comment lines are joined before matching: a citation split across two
		// of them escaped the line-by-line scan, and the comment style in this
		// repository makes that likely.
		rel, _ := filepath.Rel(root, path)
		var block strings.Builder
		harvest := func() {
			text := block.String()
			for _, name := range cited.FindAllString(text, -1) {
				key := dir + "\x00" + name
				citations[key] = append(citations[key], citation{file: rel, text: text})
			}
			block.Reset()
		}
		for _, line := range strings.Split(string(body), "\n") {
			comment := strings.Index(line, "//")
			if comment < 0 {
				harvest()
				continue
			}
			block.WriteString(strings.TrimPrefix(line[comment+2:], " "))
			block.WriteString(" ")
		}
		harvest()
		return nil
	})
	if err != nil {
		t.Fatalf("walk the repository: %v", err)
	}
	if len(citations) == 0 {
		t.Fatal("no citation found anywhere, which means this test measures nothing")
	}

	for key, where := range citations {
		dir, name, _ := strings.Cut(key, "\x00")
		files := make([]string, 0, len(where))
		for _, c := range where {
			files = append(files, c.file)
		}
		switch {
		case byDir[dir][name]:
			// The ordinary case: the test lives beside the comment citing it.
		case len(homes[name]) == 0:
			t.Errorf("%s is cited in %s and does not exist: a comment that cannot fail is not a control",
				name, strings.Join(files, ", "))
		default:
			// It lives elsewhere, which is legitimate — the core cites a test a
			// pack runs — but only when the comment says where. Otherwise a
			// homonym in any package satisfies a citation pointing at nothing:
			// an audit renamed a cited test, dropped a stub of the same name in
			// another package, and this test went green.
			for _, c := range where {
				if !namesItsHome(c.text, homes[name]) {
					t.Errorf("%s is cited in %s, lives in %s, and the comment does not say so: "+
						"name the package, or a homonym anywhere satisfies this citation",
						name, c.file, strings.Join(homes[name], ", "))
				}
			}
		}
	}
}

type citation struct{ file, text string }

// namesItsHome reports whether a comment citing a test from another package
// says which one. The package name alone is enough — "emulator's TestX", "TestX
// in internal/cli" — because that is what a reader needs to find it.
func namesItsHome(comment string, homes []string) bool {
	for _, home := range homes {
		if strings.Contains(comment, filepath.Base(home)) || strings.Contains(comment, home) {
			return true
		}
	}
	return false
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
