package cli_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// One run that changes two sections of the same page keeps both.
//
// `feint docs` writes the target from a copy it spliced at the top of the run,
// and several helpers below it re-read that same file and splice into what they
// find — the safety banner, the quick start, and the promise added by #592.
// Written last, the target's copy is one taken *before* those helpers ran, so it
// puts their sections back the way they were and the run reports success.
//
// Measured while adding the promise block: a single `feint docs` wrote the new
// promise and then reverted it, and the only symptom was `docs --check` still
// red after a regeneration that had said `README.md updated`. Nothing lied; the
// last writer won.
//
// The two markers are chosen so this test needs nothing but the coverage
// artefacts: the coverage table is spliced into the target's own copy, and the
// promise is written by a helper that re-reads the file. Move the target's write
// back below the helpers and this test fails on the promise.
func TestARunThatChangesTwoSectionsOfTheREADMEKeepsBoth(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "README.md")
	stale := "intro\n\n" +
		"<!-- coverage:start -->\nwritten by hand, and wrong\n<!-- coverage:end -->\n\n" +
		"<!-- promise:start -->\nalso written by hand, and also wrong\n<!-- promise:end -->\n\n" +
		"outro\n"
	if err := os.WriteFile(target, []byte(stale), 0o600); err != nil {
		t.Fatal(err)
	}

	coverage := repoPath(t, "coverage")
	if code, _, errOut := run("docs", "--file", target, "--coverage", coverage); code != 0 {
		t.Fatalf("docs exited %d: %s", code, errOut)
	}

	written, err := os.ReadFile(target) //nolint:gosec // a path this test just made
	if err != nil {
		t.Fatal(err)
	}
	body := string(written)

	// Both sections were regenerated, and the assertion is on the stale text
	// rather than on the new: a section that vanished entirely would satisfy
	// "the new text is there" for the other one and nothing else.
	if strings.Contains(body, "written by hand, and wrong") {
		t.Error("the coverage section was not regenerated at all")
	}
	if strings.Contains(body, "also written by hand, and also wrong") {
		t.Error("the promise was written and then reverted by the target's own write: one run " +
			"changed two sections and kept one, which reads as success and is not")
	}

	// And the run is idempotent, which is what a gate reads.
	if code, _, errOut := run("docs", "--file", target, "--coverage", coverage, "--check"); code != 0 {
		t.Fatalf("docs --check is red right after a write: %s", errOut)
	}
}
