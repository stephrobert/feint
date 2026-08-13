package cli_test

import (
	"os"
	"path/filepath"
	"regexp"
	"testing"
)

// A tool version written in two files drifts, and the failure is silent in the
// worst direction: the workstation and the runner install different things, and
// the artefacts the runner regenerates are produced by a tool nobody on the
// project is using.
//
// `.github/workflows/drift.yml` installed uv unpinned until 2026-08-13 — the
// version of the day, every week, in the very workflow that rewrites committed
// contracts and opens a pull request with them. Scorecard's Pinned-Dependencies
// flagged it and was right. Pinning it introduced the second copy this test
// exists to keep honest.
//
// mise.toml is the source: it is what a contributor's shell reads, and the
// workflow follows it rather than the other way round.
func TestTheWorkflowsPinTheSameToolsAsMise(t *testing.T) {
	root := moduleRoot(t)

	// The file that owns each version, and how to read it there.
	owners := map[string]struct {
		file    string
		pattern *regexp.Regexp
	}{
		// mise.toml: `uv = "0.11.26"` with a trailing comment.
		"uv": {"mise.toml", regexp.MustCompile(`(?m)^uv\s*=\s*"([^"]+)"`)},
		// The hook owns commitizen's: it is what refuses a message as it is
		// written, and tools/release/preflight.sh already reads the version
		// from here rather than keeping a third copy.
		"commitizen": {".pre-commit-config.yaml",
			regexp.MustCompile(`commitizen-tools/commitizen\s*\n\s*rev:\s*v?([0-9][^\s]*)`)},
	}
	// Where each version is repeated, and the pattern that reads it back.
	repeats := map[string][]struct {
		file    string
		pattern *regexp.Regexp
	}{
		"uv": {{
			file:    "tools/ci/uv-requirements.txt",
			pattern: regexp.MustCompile(`(?m)^uv==([^\s\\]+)`),
		}},
		"commitizen": {{
			file:    "tools/ci/commitizen-requirements.txt",
			pattern: regexp.MustCompile(`(?m)^commitizen==([^\s\\]+)`),
		}},
	}

	for tool, owner := range owners {
		source, err := os.ReadFile(filepath.Join(root, owner.file))
		if err != nil {
			t.Errorf("read %s: %v", owner.file, err)
			continue
		}
		want := owner.pattern.FindSubmatch(source)
		if want == nil {
			t.Errorf("%s no longer declares %s: this test cannot compare what it "+
				"cannot find, and would otherwise pass while measuring nothing",
				owner.file, tool)
			continue
		}
		for _, repeat := range repeats[tool] {
			raw, err := os.ReadFile(filepath.Join(root, repeat.file))
			if err != nil {
				t.Errorf("read %s: %v", repeat.file, err)
				continue
			}
			got := repeat.pattern.FindSubmatch(raw)
			if got == nil {
				t.Errorf("%s no longer pins %s: an unpinned install there takes "+
					"whatever is newest, in a workflow that rewrites committed artefacts",
					repeat.file, tool)
				continue
			}
			if string(got[1]) != string(want[1]) {
				t.Errorf("%s pins %s at %s, %s says %s: a workstation and the runner "+
					"would install different tools",
					repeat.file, tool, got[1], owner.file, want[1])
			}
		}
	}
}

// moduleRoot walks up to the module root rather than hardcoding "../..": a
// package that moves would otherwise make this test read the wrong tree, or no
// tree at all, and pass either way.
func moduleRoot(t *testing.T) string {
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
