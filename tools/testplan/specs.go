package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// The falsifications a diff has earned, read off the specs rather than
// remembered.
//
// This is a discipline every batch on this repository has followed by hand and
// stated in its own pull request — *"the 31 specs naming a touched file replayed
// in full"* — which means it has also been forgotten by hand. A guard whose test
// stopped biting is invisible to `mise run check`: the test still passes, and it
// passes over nothing. `falsify:lint` checks only that each declared fragment
// still applies textually; replaying the mutation is what proves the test bites.
//
// 128 specs name 186 distinct files today, so the mapping is far past the size
// anybody holds in their head, and it is exact: a spec says which file it
// mutates. Nothing here is a judgement — the specs are the source, and a spec
// that stops naming a file stops earning its replay on the same commit.
//
// `falsify:all` is the whole set and belongs to the night. What this prints is
// the subset the diff can possibly have broken.

// falsifications lists the specs whose mutations name one of these paths.
func falsifications(root string, paths []string) []string {
	changed := make(map[string]bool, len(paths))
	for _, p := range paths {
		changed[p] = true
	}

	dir := filepath.Join(root, "tools", "falsify", "specs")
	entries, err := os.ReadDir(dir)
	if err != nil {
		// No specs directory: a worktree without them, or a caller pointing at
		// a synthetic tree. Silence is right here and nowhere else in this
		// tool — the specs are an artefact this reads, not a claim it makes.
		return nil
	}

	var earned []string
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		body, err := os.ReadFile(filepath.Join(dir, entry.Name()))
		if err != nil {
			continue
		}
		var spec struct {
			Mutations []struct {
				File string `json:"file"`
			} `json:"mutations"`
		}
		if json.Unmarshal(body, &spec) != nil {
			continue
		}
		for _, m := range spec.Mutations {
			if changed[m.File] {
				earned = append(earned, "mise run falsify -- tools/falsify/specs/"+entry.Name())
				break
			}
		}
	}
	sort.Strings(earned)
	return earned
}

// testOnly reports whether a path is compiled into tests and nothing else.
//
// A `_test.go` file and a `testdata/` tree cannot change an answer a real client
// reads, so no conformance leg can judge them — the same argument that makes
// prose prose wherever it lives. What judges them is `mise run check`, which
// prepush already runs, and the falsifications above, which is the half worth
// having: a change to a test is exactly the change that can stop a guard biting
// without turning anything red.
func testOnly(path string) bool {
	return strings.HasSuffix(path, "_test.go") ||
		strings.HasPrefix(path, "testdata/") ||
		strings.Contains(path, "/testdata/")
}
