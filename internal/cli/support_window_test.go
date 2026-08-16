package cli

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The support window names exactly the surfaces that are frozen (#178).
//
// A policy with no control is prose, and this repository has measured what that
// costs: #171 was filed for two sentences that said the same thing in two files
// and were enforced in neither. The window below is cheap to keep honest, so it
// is kept honest.
//
// The failure this prevents is the one that will actually happen: somebody
// freezes a sixth surface, the fixture directory grows, and the policy keeps
// promising five. A user reading it would then depend on something the project
// never agreed to support — or miss something it does.
func TestTheSupportWindowNamesEveryFrozenSurface(t *testing.T) {
	root := repoRoot(t)

	entries, err := os.ReadDir(filepath.Join(root, "internal", "cli", "testdata", "frozen"))
	if err != nil {
		t.Skipf("no frozen fixtures to compare against: %v", err)
	}
	policy, err := os.ReadFile(filepath.Join(root, "RELEASING.md"))
	if err != nil {
		t.Fatalf("read the policy: %v", err)
	}
	section := between(string(policy), "## What you may depend on, and for how long", "\n## ")
	if section == "" {
		t.Fatal("RELEASING.md carries no support window")
	}

	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		if !strings.Contains(section, entry.Name()) {
			t.Errorf("%s is frozen and the support window does not name it: a user cannot tell "+
				"whether it is covered", entry.Name())
		}
	}

	// And the other direction: a surface the policy promises must exist, or the
	// promise is about a file nobody ships.
	for _, line := range strings.Split(section, "\n") {
		for _, field := range strings.Fields(line) {
			name := strings.Trim(field, "`|")
			if !strings.HasSuffix(name, ".json") || !strings.Contains(name, "frozen/") {
				continue
			}
			if _, err := os.Stat(filepath.Join(root, name)); err != nil {
				t.Errorf("the support window promises %s and it does not exist", name)
			}
		}
	}
}

// The window states a notice, a signal and an escape, because a policy missing
// any of the three answers nothing a consumer actually asks.
//
// Checked by their anchors rather than by their wording: the point is that the
// three questions are answered somewhere a reader can find, not that they are
// answered in a sentence this test dictates.
func TestTheSupportWindowAnswersTheThreeQuestions(t *testing.T) {
	root := repoRoot(t)
	policy, err := os.ReadFile(filepath.Join(root, "RELEASING.md"))
	if err != nil {
		t.Fatalf("read the policy: %v", err)
	}
	section := between(string(policy), "## What you may depend on, and for how long", "\n## ")

	for _, want := range []string{"### The notice", "### The signal", "### The escape"} {
		if !strings.Contains(section, want) {
			t.Errorf("the support window has no %q: a consumer is left to infer it", want)
		}
	}
	// The escape has to be a recipe rather than an intention, or it is the
	// "whatever happened last time" this issue was filed against.
	if !strings.Contains(section, "schema_version") || !strings.Contains(section, "exit 1") {
		t.Error("the escape names no version to read and no way to stop; it is an intention, not a path")
	}
}
